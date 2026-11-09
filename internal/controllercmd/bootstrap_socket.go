package controllercmd

import (
	"bytes"
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"io"
	"net"
	"net/http"
)

// requestOwnerSetup uses only the local daemon protocol without linking the laptop client package.
func requestOwnerSetup(ctx context.Context, socket string, input model.OwnerSetupRequest, result *model.OwnerSetupResult) error {
	data, err := json.Marshal(input)
	// Encoding errors cannot produce a partial administrator request.
	if err != nil {
		return err
	}
	transport := &http.Transport{
		// The URL host is a placeholder; every connection goes to the protected local socket.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport,
		// Administrator requests must never follow a redirect outside the original socket endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/v1/access/setup", bytes.NewReader(data))
	// Construction failure must not fall back to another transport.
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	// No network credential or state-file access can substitute for socket permission.
	if err != nil {
		return model.Err("CONTROLLER_UNAVAILABLE", "Cannot reach the controller administration socket; check that the service is running and socket permissions allow access")
	}
	defer response.Body.Close()
	return decodeOwnerSetupResponse(response, result)
}

// decodeOwnerSetupResponse bounds local protocol output and propagates only structured safe errors.
func decodeOwnerSetupResponse(response *http.Response, result *model.OwnerSetupResult) error {
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	// Invalid or excessive responses cannot expose raw secret-bearing output in an error.
	if err != nil || len(data) > 65536 {
		return model.Err("BOOTSTRAP_RESPONSE", "Invalid controller bootstrap response")
	}
	// Refuse redirects and semantic failures rather than decoding them as enrollment success.
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error *model.Error `json:"error"`
		}
		// Known safe controller errors explain origin mismatch, permissions or missing requests.
		if json.Unmarshal(data, &failure) == nil && failure.Error != nil {
			return failure.Error
		}
		return model.Err("BOOTSTRAP_RESPONSE", "Controller rejected owner setup; ensure the running controller supports bootstrap")
	}
	// Only the typed bootstrap response may leave the protected local command.
	if json.Unmarshal(data, result) != nil {
		return model.Err("BOOTSTRAP_RESPONSE", "Invalid controller bootstrap response")
	}
	return nil
}
