package runtime

import (
	"encoding/json"
	"hostlier/internal/model"
	"strings"
	"testing"
)

// TestCaddyGRPCPolicyUsesExactCaseSensitiveMethodsAndInheritedTimeouts checks actual proxy configuration.
func TestCaddyGRPCPolicyUsesExactCaseSensitiveMethodsAndInheritedTimeouts(t *testing.T) {
	e := model.Endpoint{Exposure: "public", Route: "https://grpc.example.com", Protocol: "grpc", IdleTimeoutSeconds: 900, GRPC: &model.GRPCPolicy{Services: map[string]model.GRPCServicePolicy{"engine.v1.EngineService": {IdleTimeoutSeconds: 300, Methods: map[string]model.GRPCMethodPolicy{"Connect": {IdleTimeoutSeconds: 60}, "SubscribeWebhooks": {}}}}}}
	route, err := BuildCaddyRoute(model.Project{Name: "engine", Route: e.Route, Endpoint: &e}, "127.0.0.1:50051")
	data, _ := json.Marshal(route)
	// MatchPath is case-insensitive, so protobuf identities must use anchored case-sensitive regexes.
	if err != nil || !strings.Contains(string(data), `"path_regexp"`) || strings.Contains(string(data), `"flush_interval"`) {
		t.Fatalf("%s %v", data, err)
	}
	// Both inheritance levels must produce real upstream I/O budgets and a native denial status.
	for _, want := range []string{`"read_timeout":60000000000`, `"read_timeout":300000000000`, `"Grpc-Status":["7"]`, `"protocol":"grpc"`} {
		// A missing rendered setting means the edge would enforce a different reviewed contract.
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
}

// TestCaddyHTTPPolicyEnforcesUpgradeAndRouteHTTPVersions covers the generated request admission matchers.
func TestCaddyHTTPPolicyEnforcesUpgradeAndRouteHTTPVersions(t *testing.T) {
	e := model.Endpoint{Exposure: "public", Route: "https://app.example.com", Protocol: "http", HTTP: &model.HTTPPolicy{Routes: map[string]model.HTTPRoutePolicy{
		"socket": {Path: "/ws", Methods: []string{"GET"}, Protocol: "websocket"},
		"events": {Path: "/events", Methods: []string{"GET"}, Protocol: "sse", HTTPVersions: []string{"h3"}},
	}}}
	route, err := BuildCaddyRoute(model.Project{Name: "app", Route: e.Route, Endpoint: &e}, "127.0.0.1:8081")
	data, _ := json.Marshal(route)
	// A per-route QUIC requirement must activate UDP and reject TCP for that exact resource.
	if err != nil || !endpointRequiresH3(&e) || !strings.Contains(string(data), `"protocol":"http/3"`) {
		t.Fatalf("%s %v", data, err)
	}
	// WebSocket-only routes require a real upgrade handshake, while unknown resources are denied.
	for _, want := range []string{`"header_regexp"`, `"protocol":"http/1.1"`, `"status_code":403`, `"flush_interval":-1`} {
		// These fields distinguish enforced transport policies from decorative schema metadata.
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
}

// TestCaddyExtendedWebSocketMatchersDistinguishPseudoHeaders tests the exact wire admission contract.
func TestCaddyExtendedWebSocketMatchersDistinguishPseudoHeaders(t *testing.T) {
	rule := model.HTTPRoutePolicy{Path: "/ws", Protocol: "websocket", Methods: []string{"GET", "CONNECT"}, HTTPVersions: []string{"h1", "h2", "h3"}}
	matches := httpPolicyMatches(rule)
	data, _ := json.Marshal(matches)
	// HTTP/1 Upgrade, HTTP/2 :protocol and HTTP/3 Request.Proto must remain separate matcher sets.
	for _, want := range []string{`"method":["GET"]`, `"method":["CONNECT"]`, `":protocol":["websocket"]`, `"protocol":"http/3"`, `http.request.proto`} {
		// Generic CONNECT or a header forged on ordinary HTTP cannot satisfy these matchers.
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
}
