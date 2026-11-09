package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hostlier/internal/model"
)

// An apt-installed Caddy may have autosaved the package's default site before
// Hostlier installs its unit. Load the approved initial edge explicitly; --resume
// then preserves future application routes rather than reverting to that default.
func ensureEdge(ctx context.Context, p Plan, initial string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	// Use a single private Caddy client for inspection and approved activation.
	fetch := func(method string, body io.Reader) ([]byte, error) { return fetchEdge(ctx, client, method, body) }
	raw, e := fetch("GET", nil)
	// Do not repair an edge whose current configuration cannot be fetched.
	if e != nil {
		return e
	}
	var config map[string]any
	// Require decodable current Caddy configuration before ownership checks.
	if json.Unmarshal(raw, &config) != nil {
		return model.Err("CADDY_CONFIG", "Invalid current Caddy configuration")
	}
	apps, _ := config["apps"].(map[string]any)
	httpApp, _ := apps["http"].(map[string]any)
	servers, _ := httpApp["servers"].(map[string]any)
	// Initialize the Hostlier server only when its owned server block is absent.
	if _, exists := servers["hostlier"]; !exists {
		// Require unused package-default configuration before replacing the current edge.
		if err := validateEdgeRepair(p, servers, raw); err != nil {
			return err
		}
		// Stop if Caddy rejects the approved initial configuration.
		if _, e = fetch("POST", strings.NewReader(initial)); e != nil {
			return e
		}
		raw, e = fetch("GET", nil)
		// Fetch activated state again instead of assuming the load request took effect.
		if e != nil {
			return e
		}
	}
	return verifyActivatedEdge(p, raw)
}

// fetchEdge talks only to private Caddy administration with bounded responses.
func fetchEdge(ctx context.Context, client *http.Client, method string, body io.Reader) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:2019/"+map[bool]string{true: "load", false: "config/"}[method == "POST"], body)
	// Send JSON content type only for the approved configuration load request.
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, e := client.Do(req)
	// Normalize private administration connectivity failures.
	if e != nil {
		return nil, model.Err("CADDY_NOT_READY", "Cannot reach private Caddy administration")
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, 1024*1024))
	// Do not accept interrupted output or a rejected Caddy configuration.
	if e != nil || res.StatusCode >= 400 {
		return nil, model.Err("CADDY_NOT_READY", "Caddy rejected edge configuration")
	}
	return raw, nil
}

// validateEdgeRepair permits replacement only of an unused package-default edge.
func validateEdgeRepair(p Plan, servers map[string]any, raw []byte) error {
	// Only the package default can be replaced, and no application runtime may exist.
	var appState struct{ Apps map[string]json.RawMessage }
	stateRaw, statErr := os.ReadFile(filepath.Join(p.StateDir, "runtime-"+p.Runtime+".json"))
	// Existing application state forbids repairing away missing owned routes.
	if statErr == nil && (json.Unmarshal(stateRaw, &appState) != nil || len(appState.Apps) > 0) {
		return model.Err("CADDY_CONFIG_DRIFT", "Owned application routes are absent from Caddy; inspect before repairing")
	}
	// Replace only the recognized unused package-default server configuration.
	if len(servers) > 1 || len(servers) == 1 && !bytes.Contains(raw, []byte("/usr/share/caddy")) {
		return model.Err("CADDY_CONFLICT", "Existing nondefault Caddy configuration requires explicit review")
	}
	return nil
}

// verifyActivatedEdge requires the approved controller route and an HTTPS listener.
func verifyActivatedEdge(p Plan, raw []byte) error {
	// Do not report init ready without both the allocated controller route and HTTPS listener.
	var parsed map[string]any
	// Verify the activated configuration independently of the load response.
	if json.Unmarshal(raw, &parsed) != nil {
		return model.Err("CADDY_CONFIG", "Invalid activated edge configuration")
	}
	apps, _ := parsed["apps"].(map[string]any)
	httpApp, _ := apps["http"].(map[string]any)
	servers, _ := httpApp["servers"].(map[string]any)
	server, _ := servers["hostlier"].(map[string]any)
	serverRaw, _ := json.Marshal(server)
	// Require the approved controller route, hostname and HTTPS listener declaration.
	if !bytes.Contains(serverRaw, []byte(`"hostlier-controller"`)) || !bytes.Contains(serverRaw, []byte(`"`+p.ControllerHostname+`"`)) || !bytes.Contains(serverRaw, []byte(`":443"`)) {
		return model.Err("CADDY_CONFIG_DRIFT", "Controller HTTPS route does not match the approved installation")
	}
	connection, e := net.DialTimeout("tcp", "127.0.0.1:443", 2*time.Second)
	// Confirm the declared HTTPS listener actually accepts local connections.
	if e != nil {
		return model.Err("CADDY_NOT_READY", "Caddy HTTPS listener is not ready")
	}
	connection.Close()
	return nil
}
