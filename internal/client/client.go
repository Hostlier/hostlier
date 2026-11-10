package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"hostlier/internal/model"
)

// ControllerURL validates a credential-free HTTPS origin before any remote request.
func ControllerURL(value string) (string, error) { return model.ControllerURL(value) }

// New constructs verified HTTPS or protected Unix transport without redirecting credentials.
func New(options Options) (*Client, error) {
	token, deviceKey, err := decodeDeviceCredential(options.Token)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	base := "http://localhost"
	target := "unix:" + options.SocketPath
	// Use the protected Unix socket when local administration was explicitly selected.
	if options.SocketPath != "" {
		// Dial only that socket, ignoring the HTTP hostname placeholder.
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", options.SocketPath)
		}
	} else {
		var e error
		base, e = ControllerURL(options.URL)
		// Stop before constructing transport when the remote origin is invalid.
		if e != nil {
			return nil, e
		}
		target = base
		roots, e := x509.SystemCertPool()
		// Use an empty trust pool only when the operating system pool is unavailable.
		if e != nil {
			roots = x509.NewCertPool()
		}
		ca := options.CAFile
		// Honor the process trust-file setting only when no explicit CA file was supplied.
		if ca == "" {
			ca = os.Getenv("HOSTLIER_CA_FILE")
		}
		// Extend certificate trust only from the configured CA file.
		if ca != "" {
			pem, e := os.ReadFile(ca)
			// Report unreadable trust material without exposing file contents.
			if e != nil {
				return nil, model.Err("CA_FILE", "Cannot read trusted CA file")
			}
			// Reject malformed trust material instead of silently trusting an empty certificate set.
			if !roots.AppendCertsFromPEM(pem) {
				return nil, model.Err("CA_FILE", "Trusted CA file contains no certificates")
			}
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	timeout := options.Timeout
	// Apply the normal request deadline when the caller has not supplied one.
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	// Refuse redirects so bearer credentials cannot be forwarded to another origin.
	return &Client{HTTP: &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, URL: base, Token: token, deviceKey: deviceKey, Target: target}, nil
}

// Request sends one bounded JSON request and decodes a sanitized controller response.
func (c *Client) Request(ctx context.Context, method, path string, body any, result any) error {
	// Reject path forms that could override the configured server authority.
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return model.Err("API_PATH", "Invalid API path")
	}
	var data []byte
	var e error
	// Encode a body only for requests that carry a payload.
	if body != nil {
		data, e = json.Marshal(body)
		// Stop before transmission if the payload cannot be serialized.
		if e != nil {
			return e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(data))
	// Return request-construction failures before any network activity.
	if e != nil {
		return e
	}
	req.Header.Set("Accept", "application/json")
	// Declare JSON only when a request payload is present.
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Attach the credential only when this client has been enrolled.
	if err := c.authorizeRequest(req, data); err != nil {
		return err
	}
	if c.grpc {
		return c.rpcCommand(req, data, result, false)
	}
	httpClient := c.requestTransport(method, path)
	response, e := httpClient.Do(req)
	// Convert transport failures to a safe connection error without leaking response data.
	if e != nil {
		return model.Err("CONNECTION", "Cannot reach controller; verify HTTPS certificate, address, and server availability")
	}
	defer response.Body.Close()
	return decodeResponse(response, path, result)
}

// RequestID generates an unpredictable identity for safely replayable mutations.
func RequestID() string {
	data := make([]byte, 16)
	// Fail immediately if cryptographic randomness is unavailable.
	if _, e := rand.Read(data); e != nil {
		panic(e)
	}
	return hex.EncodeToString(data)
}

// decodeResponse bounds response sizes and decodes controller errors consistently.
func decodeResponse(response *http.Response, path string, result any) error {
	limit := responseLimit(path)
	raw, e := io.ReadAll(io.LimitReader(response.Body, limit+1))
	// Translate interrupted reads to a safe connection failure.
	if e != nil {
		return model.Err("CONNECTION", "Controller response interrupted")
	}
	// Reject oversized bodies before decoding JSON.
	if int64(len(raw)) > limit {
		return model.Err("RESPONSE_LIMIT", "Controller response too large")
	}
	// Preserve the controller error contract for unsuccessful HTTP responses.
	if err := responseError(response.StatusCode, raw); err != nil {
		return err
	}
	// Decode a result only when a destination and response body are present.
	if result != nil && len(raw) > 0 {
		// Reject malformed JSON instead of accepting a partial result.
		if json.Unmarshal(raw, result) != nil {
			return model.Err("RESPONSE", "Invalid controller JSON response")
		}
	}
	return nil
}

// requestTransport grants backup creation its bounded operation timeout.
func (c *Client) requestTransport(method, path string) *http.Client {
	httpClient := c.HTTP
	// Root inspection and verified release staging may outlive an ordinary read.
	route, _, _ := strings.Cut(path, "?")
	if method == "GET" && (route == "/v1/controller/update/plan" || route == "/v1/controller/migration/plan") {
		bounded := *c.HTTP
		bounded.Timeout = 15 * time.Minute
		return &bounded
	}
	// Give resource backup creation a bounded maintenance deadline without changing other requests.
	if method == "POST" && ((strings.HasPrefix(path, "/v1/resources/") && strings.HasSuffix(path, "/backups")) || (strings.HasPrefix(path, "/v1/approvals/") && strings.HasSuffix(path, "/execute")) || strings.HasSuffix(path, "/artifacts/pull")) {
		bounded := *c.HTTP
		bounded.Timeout = 10 * time.Minute
		httpClient = &bounded
	}
	return httpClient
}

// responseError decodes structured API failures without exposing raw response bodies.
func responseError(status int, raw []byte) error {
	// Decode structured failures only for unsuccessful HTTP responses.
	if status >= 300 {
		var envelope errorEnvelope
		// Return a recognized API error while withholding arbitrary raw response bodies.
		if json.Unmarshal(raw, &envelope) == nil && envelope.Error.Code != "" {
			return &envelope.Error
		}
		return model.Err("CONTROLLER", fmt.Sprintf("Controller request failed (%d)", status))
	}
	return nil
}

// errorEnvelope accepts only the structured controller error contract.
type errorEnvelope struct {
	Error model.Error `json:"error"`
}

// responseLimit reserves larger replies for approved execution and backup exports.
func responseLimit(path string) int64 {
	if strings.HasSuffix(path, "/export") || strings.HasSuffix(path, "/execute") {
		return 90 << 20
	}
	return 4 << 20
}
