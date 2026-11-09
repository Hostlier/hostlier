package runtime

import (
	"encoding/json"
	"hostlier/internal/model"
	"strings"
	"testing"
)

// TestGatewayRoutesAlwaysUseNginxAndOverwriteTransportClaims rejects direct application bypass.
func TestGatewayRoutesAlwaysUseNginxAndOverwriteTransportClaims(t *testing.T) {
	r := &Runtime{options: Options{Proxy: "nginx-gateway"}}
	e := model.Endpoint{Exposure: "public", Route: "https://app.example.com", Protocol: "http", HTTP: &model.HTTPPolicy{Routes: map[string]model.HTTPRoutePolicy{"socket": {Path: "/ws", Protocol: "websocket", Methods: []string{"CONNECT"}, HTTPVersions: []string{"h3"}}}}}
	app := appRecord{Spec: model.Project{Name: "app", Route: e.Route, Endpoint: &e}, Upstream: "127.0.0.1:18081"}
	app.Status = "running"
	route, err := r.nginxGatewayRoute(app)
	data, _ := json.Marshal(route)
	// The frontend can reach only the managed private backend, not the workload's private socket.
	if err != nil || !strings.Contains(string(data), "127.0.0.1:14443") || strings.Contains(string(data), "127.0.0.1:18081") {
		t.Fatalf("%s %v", data, err)
	}
	// Every trusted header is derived from the real frontend request and replaces client input.
	for _, field := range []string{"X-Hostlier-Gateway-Method", "X-Hostlier-Gateway-Protocol", "X-Hostlier-Gateway-Scheme", "http.request.proto_name", "http.request.method"} {
		// Missing metadata would make backend policy evaluation depend on client-supplied claims.
		if !strings.Contains(string(data), field) {
			t.Fatalf("missing %s: %s", field, data)
		}
	}
}
