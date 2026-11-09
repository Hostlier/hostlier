package nginx

import (
	"hostlier/internal/model"
	"strings"
	"testing"
)

// TestRendererRejectsUnsupportedTransports ensures direct rendering cannot silently downgrade requirements.
func TestRendererRejectsUnsupportedTransports(t *testing.T) {
	// Runtime preflight and standalone rendering share the same explicit capability boundary.
	for _, endpoint := range []model.Endpoint{{Protocol: "h2c"}, {Protocol: "http", HTTPVersions: []string{"h3"}}} {
		_, err := endpointLocation(model.Project{Endpoint: &endpoint}, "/", false, []string{"127.0.0.1:50051"})
		// Unsupported configurations must fail before producing a usable Nginx fragment.
		if err == nil {
			t.Fatal("unsupported transport was silently downgraded")
		}
	}
}

// TestNginxExactProtocolPathsNeverRedirect protects literal RPC and MCP addresses from trailing-slash mutation.
func TestNginxExactProtocolPathsNeverRedirect(t *testing.T) {
	p := model.Project{Name: "engine", Route: "https://engine.example.com/mcp", Endpoint: &model.Endpoint{Protocol: "http"}}
	text, err := renderRoute(Route{Project: p, Upstreams: []string{"127.0.0.1:8081"}})
	// Exact and descendant locations both proxy without changing the incoming request method or path.
	if err != nil || strings.Contains(text, "308") || !strings.Contains(text, "location = /mcp {") || !strings.Contains(text, "location ^~ /mcp/ {") {
		t.Fatalf("%s %v", text, err)
	}
}
