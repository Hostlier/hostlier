package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTunnelIngressPublishesExactHostAndPreservesRoutes(t *testing.T) {
	var config map[string]any
	if err := json.Unmarshal([]byte(`{"apps":{"http":{"servers":{"hostlier":{"listen":[":443"],"routes":[{"@id":"external","match":[{"host":["other.example.com"]}]}]}}}}}`), &config); err != nil {
		t.Fatal(err)
	}
	loads := 0
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config/":
			json.NewEncoder(w).Encode(config)
		case "/load":
			loads++
			if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer admin.Close()
	r, err := New(Options{CaddyAdmin: admin.URL, SkipPublicHTTPSProbe: true})
	if err != nil {
		t.Fatal(err)
	}
	adapter := r.(*Runtime)
	url := "https://new.tunnels.example.com"
	if err := adapter.EnsureTunnelIngress(context.Background(), url); err != nil {
		t.Fatal(err)
	}
	if err := adapter.EnsureTunnelIngress(context.Background(), url); err != nil {
		t.Fatal(err)
	}
	routes := config["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["hostlier"].(map[string]any)["routes"].([]any)
	if loads != 1 || len(routes) != 2 || !tunnelRouteMatchesHost(routes[1].(map[string]any), "new.tunnels.example.com") || !tunnelRouteForController(routes[1].(map[string]any), "127.0.0.1:7463") {
		t.Fatalf("unexpected tunnel publication: loads=%d routes=%v", loads, routes)
	}
	if routes[0].(map[string]any)["@id"] != "external" {
		t.Fatal("unrelated Caddy route changed")
	}
}

func TestTunnelIngressRejectsOccupiedHost(t *testing.T) {
	route := map[string]any{"match": []any{map[string]any{"host": []any{"taken.tunnels.example.com"}}}, "handle": []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": "127.0.0.1:9999"}}}}}
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/load" {
			t.Fatal("occupied route was overwritten")
		}
		json.NewEncoder(w).Encode(map[string]any{"apps": map[string]any{"http": map[string]any{"servers": map[string]any{"hostlier": map[string]any{"routes": []any{route}}}}}})
	}))
	defer admin.Close()
	r, err := New(Options{CaddyAdmin: admin.URL, SkipPublicHTTPSProbe: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.(*Runtime).EnsureTunnelIngress(context.Background(), "https://taken.tunnels.example.com"); err == nil {
		t.Fatal("occupied hostname accepted")
	}
}
