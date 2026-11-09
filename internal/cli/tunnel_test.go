package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTunnelRegistrationIsConnectorNeutral prevents Hostlier from owning local service settings or exposing credentials.
func TestTunnelRegistrationIsConnectorNeutral(t *testing.T) {
	token := strings.Repeat("secret", 8)
	t.Setenv("TEST_TUNNEL_TOKEN", token)
	// Return only the server-owned publication metadata.
	remote := &fakeClient{reply: func(c call) (any, error) {
		return map[string]any{"name": "demo", "bridgeId": "laptop", "url": "https://demo.tunnels.example.com", "connectUrl": "https://controller.example.com/v1/tunnels/demo/connect", "connections": 4, "expiresAt": 9999999999999}, nil
	}}
	args := []string{"tunnel", "create", "demo", "--bridge-id", "laptop", "--from-env", "TEST_TUNNEL_TOKEN", "--socket", "/test/socket", "--json"}
	text, err := invoke(args, "", Options{Client: remote})
	// Public output must be machine-readable and credential-free.
	if err != nil || !json.Valid([]byte(text)) || strings.Contains(text, token) {
		t.Fatalf("registration output: %v", err)
	}
	request, _ := json.Marshal(remote.calls[0].body)
	// The API receives only the token verifier, never the forwarding target or token.
	if strings.Contains(string(request), token) || strings.Contains(string(request), "target") {
		t.Fatal("private connector data in registration")
	}
	before := len(remote.calls)
	_, err = invoke(append(args, "--target", "localhost:3000"), "", Options{Client: remote})
	// The removed handoff path must fail before issuing a remote request.
	if err == nil || len(remote.calls) != before {
		t.Fatal("Hostlier accepted local service configuration")
	}
}
