package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"hostlier/internal/controledge"
	"hostlier/internal/model"
)

// Upgrade only the named controller route in Caddy's active (autosaved) config.
// An ETag protects concurrent updates and unrelated application routes are
// outside the PATCH scope. Unknown controller customizations fail closed.
func upgradeCaddyRPC(ctx context.Context, client *http.Client, endpoint string, p Plan) error {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var current map[string]any
	// Caddy sends its revision as an HTTP trailer, available only after EOF.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if resp.StatusCode != 200 || readErr != nil || len(body) > 65536 || json.Unmarshal(body, &current) != nil {
		return model.Err("CADDY_CONFIG_DRIFT", "Cannot read owned controller route")
	}
	next, changed, err := controllerRPCUpgrade(p, current)
	if err != nil || !changed {
		return err
	}
	revision := resp.Trailer.Get("ETag")
	if revision == "" {
		revision = resp.Header.Get("ETag")
	}
	if revision == "" {
		return model.Err("CADDY_CONFIG_DRIFT", "Missing controller route revision")
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	patch, err := http.NewRequestWithContext(ctx, "PATCH", endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	patch.Header.Set("Content-Type", "application/json")
	patch.Header.Set("If-Match", revision)
	updated, err := client.Do(patch)
	if err != nil {
		return err
	}
	defer updated.Body.Close()
	if updated.StatusCode >= 300 {
		return model.Err("CADDY_CONFIG_DRIFT", "Could not update the owned controller route; inspect Caddy configuration and rerun initialization")
	}
	return nil
}

// Recognize only routes emitted by the installer/runtime. Gateway proxy
// headers are reapplied to both upstreams; customized policies are never reset.
func controllerRPCUpgrade(p Plan, current map[string]any) (map[string]any, bool, error) {
	ordinary, rpc := "127.0.0.1:7463", "127.0.0.1:7463"
	if p.Proxy == "nginx-gateway" {
		ordinary, rpc = "127.0.0.1:14443", "127.0.0.1:14444"
	}
	next := controledge.CaddyRoute(p.ControllerHostname, ordinary, rpc)
	drift := model.Err("CADDY_CONFIG_DRIFT", "Unexpected owned controller route; inspect the Caddy configuration before upgrading")
	if current["@id"] != next["@id"] || current["terminal"] != true || !sameConfig(current["match"], next["match"]) {
		return nil, false, drift
	}
	if sameConfig(current["handle"], next["handle"]) {
		return current, false, nil
	}
	if p.Proxy == "nginx-gateway" {
		for _, sub := range next["handle"].([]any)[0].(map[string]any)["routes"].([]any) {
			proxy := sub.(map[string]any)["handle"].([]any)[0].(map[string]any)
			proxy["headers"] = map[string]any{"request": map[string]any{"set": controledge.GatewayHeaders()}}
		}
		if sameConfig(current["handle"], next["handle"]) {
			return current, false, nil
		}
	}
	httpRoute := next["handle"].([]any)[0].(map[string]any)["routes"].([]any)[1].(map[string]any)
	bootstrap := []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": "127.0.0.1:7463"}}}}
	if !sameConfig(current["handle"], bootstrap) && !sameConfig(current["handle"], httpRoute["handle"]) {
		return nil, false, drift
	}
	current["handle"] = next["handle"]
	return current, true, nil
}

// Compare the actual Caddy JSON (including nested header arrays), independent
// of the Go slice types used by the configuration renderers.
func sameConfig(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}
