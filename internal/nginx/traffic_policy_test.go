package nginx

import (
	"hostlier/internal/model"
	"strings"
	"testing"
)

// TestNginxGRPCPolicyUsesExactLocationsAndDeniesUnknownMethods guards native framing and fallback ownership.
func TestNginxGRPCPolicyUsesExactLocationsAndDeniesUnknownMethods(t *testing.T) {
	e := model.Endpoint{Exposure: "public", Route: "https://grpc.example.com", Protocol: "grpc", GRPC: &model.GRPCPolicy{Services: map[string]model.GRPCServicePolicy{"engine.v1.EngineService": {Methods: map[string]model.GRPCMethodPolicy{"Connect": {IdleTimeoutSeconds: 42}}}}}}
	text, err := renderRoute(Route{Project: model.Project{Name: "engine", Route: e.Route, Endpoint: &e}, Upstreams: []string{"127.0.0.1:50051"}})
	// Exact method permissions must preserve the full RPC path and expose no catch-all proxy.
	if err != nil || !strings.Contains(text, "location = /engine.v1.EngineService/Connect {") || strings.Count(text, "grpc_pass") != 1 {
		t.Fatalf("%s %v", text, err)
	}
	// Method-specific budgets and terminal native permission errors are mandatory.
	if !strings.Contains(text, "grpc_read_timeout 42s") || !strings.Contains(text, "add_header grpc-status 7 always") {
		t.Fatal(text)
	}
}

// TestNginxHTTPPolicyRejectsQUICAndProtectsWebSocketHandshake tests the adapter's explicit capability boundary.
func TestNginxHTTPPolicyRejectsQUICAndProtectsWebSocketHandshake(t *testing.T) {
	e := model.Endpoint{Exposure: "public", Route: "https://app.example.com", Protocol: "http", HTTP: &model.HTTPPolicy{Routes: map[string]model.HTTPRoutePolicy{"socket": {Path: "/ws", Methods: []string{"GET"}, Protocol: "websocket"}}}}
	p := model.Project{Name: "app", Route: e.Route, Endpoint: &e}
	e.HTTP.Routes["health"] = model.HTTPRoutePolicy{Path: "/health", Protocol: "http", Methods: []string{"GET"}}
	e.HTTP.Routes["events"] = model.HTTPRoutePolicy{Path: "/events", Protocol: "sse", Methods: []string{"GET"}}
	e.HTTP.Routes["mcp"] = model.HTTPRoutePolicy{Path: "/mcp", Protocol: "streamable-http", Methods: []string{"GET", "POST", "DELETE"}}
	text, err := renderRoute(Route{Project: p, Upstreams: []string{"127.0.0.1:8081"}})
	// Method and Upgrade checks must be return-only conditions with an exact resource location.
	if err != nil || !strings.Contains(text, "location = /ws {") || !strings.Contains(text, "$http_upgrade !~* ^websocket$") || !strings.Contains(text, "$request_method !~ ^(GET)$") {
		t.Fatalf("%s %v", text, err)
	}
	// Lowering one route's disabled upgrade flag must not invalidate sibling WebSocket permissions.
	for _, path := range []string{"/health", "/events", "/mcp"} {
		// A missing exact location would silently deny a reviewed HTTP permission.
		if !strings.Contains(text, "location = "+path+" {") {
			t.Fatalf("missing mixed-transport permission %s: %s", path, text)
		}
	}
	e.HTTP.Routes["quic"] = model.HTTPRoutePolicy{Path: "/quic", Methods: []string{"GET"}, Protocol: "http", HTTPVersions: []string{"h3"}}
	// An unsupported transport cannot be accepted then silently served over TCP.
	if _, err := renderRoute(Route{Project: p, Upstreams: []string{"127.0.0.1:8081"}}); err == nil {
		t.Fatal("Nginx silently accepted HTTP/3 policy")
	}
}

// TestNginxRejectsWebSocketExtendedCONNECT keeps the adapter from silently accepting unsupported handshakes.
func TestNginxRejectsWebSocketExtendedCONNECT(t *testing.T) {
	// Both extended transports require a proxy that bridges their CONNECT streams.
	for _, version := range []string{"h2", "h3"} {
		e := model.Endpoint{Exposure: "public", Route: "https://app.example.com", Protocol: "http", HTTP: &model.HTTPPolicy{Routes: map[string]model.HTTPRoutePolicy{"socket": {Path: "/ws", Protocol: "websocket", Methods: []string{"CONNECT"}, HTTPVersions: []string{version}}}}}
		_, err := renderRoute(Route{Project: model.Project{Name: "app", Route: e.Route, Endpoint: &e}, Upstreams: []string{"127.0.0.1:8081"}})
		// A rejected requirement cannot be misreported as HTTP/1-only support.
		if err == nil || !strings.Contains(err.Error(), "Caddy") {
			t.Fatalf("%s: expected explicit Caddy requirement; got %v", version, err)
		}
	}
}
