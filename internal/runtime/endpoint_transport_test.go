package runtime

import (
	"encoding/json"
	"hostlier/internal/model"
	"strings"
	"testing"
)

// TestCaddyNamedTransportRendering preserves HTTP streaming and native gRPC without conflating QUIC upstreams.
func TestCaddyNamedTransportRendering(t *testing.T) {
	p := model.Project{Name: "engine", Route: "https://grpc.example.com", Endpoint: &model.Endpoint{Protocol: "grpc", HTTPVersions: []string{"h1", "h2", "h3"}, IdleTimeoutSeconds: 120}}
	route, err := BuildCaddyRoute(p, "127.0.0.1:50051")
	data, _ := json.Marshal(route)
	// Native HTTP/2 streaming owns its flush semantics, including trailers-only error responses.
	if err != nil || !strings.Contains(string(data), `"h2c"`) || strings.Contains(string(data), `"flush_interval"`) {
		t.Fatalf("%s %v", data, err)
	}
	record := appRecord{Deployment: model.Deployment{Name: "engine", Status: "running"}, Spec: model.Project{Version: 2, Name: "engine", Endpoints: map[string]model.Endpoint{"grpc": {Port: 50051, Exposure: "public", Route: p.Route, Protocol: "grpc"}, "http": {Port: 8081, Exposure: "public", Route: "https://engine.example.com", HTTPVersions: []string{"h3"}}}}, EndpointUpstreams: map[string]string{"http": "127.0.0.1:18081", "grpc": "127.0.0.1:15051"}}
	s := state{Apps: map[string]appRecord{"engine": record}}
	// One workload produces independent routes, while the public listener advertises HTTP/3.
	if len(expandedApplications(s)) != 2 || strings.Join(edgeHTTPVersions(s), ",") != "h1,h2,h3" {
		t.Fatal("named route expansion lost transport information")
	}
}

// TestCaddyExactProtocolPathsNeverRedirect protects RPC methods and Streamable HTTP POST resource identity.
func TestCaddyExactProtocolPathsNeverRedirect(t *testing.T) {
	p := model.Project{Name: "engine", Route: "https://engine.example.com/mcp", Endpoint: &model.Endpoint{Protocol: "http"}}
	route, err := BuildCaddyRoute(p, "127.0.0.1:8081")
	data, _ := json.Marshal(route)
	// Both the exact resource and its descendants proxy directly without a 308 redirect.
	if err != nil || strings.Contains(string(data), "static_response") || !strings.Contains(string(data), `"/mcp"`) || !strings.Contains(string(data), `"/mcp/*"`) {
		t.Fatalf("%s %v", data, err)
	}
}
