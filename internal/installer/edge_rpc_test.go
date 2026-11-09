package installer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"hostlier/internal/controledge"
)

// Upgrade just the named Caddy route, with a revision guard. Running init
// twice must not resend a patch or replace existing HTTP application routes.
func TestCaddyGRPCUpgrade(t *testing.T) {
	for _, mode := range []string{"caddy", "nginx-gateway"} {
		t.Run(mode, func(t *testing.T) {
			p := Plan{Options: Options{ControllerHostname: "control.example", Proxy: mode}}
			current := map[string]any{"@id": "hostlier-controller", "match": []any{map[string]any{"host": []any{"control.example"}}}, "handle": []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": "127.0.0.1:7463"}}}}, "terminal": true}
			patches := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/id/hostlier-controller" {
					t.Errorf("unexpected admin path %s", r.URL.Path)
				}
				if r.Method == "GET" {
					w.Header().Set("Trailer", "ETag")
					_ = json.NewEncoder(w).Encode(current)
					w.Header().Set("ETag", `"revision"`)
					return
				}
				if r.Method != "PATCH" || r.Header.Get("If-Match") != `"revision"` {
					t.Errorf("unguarded update")
				}
				if err := json.NewDecoder(r.Body).Decode(&current); err != nil {
					t.Error(err)
				}
				patches++
			}))
			defer s.Close()
			for i := 0; i < 2; i++ {
				if err := upgradeCaddyRPC(context.Background(), s.Client(), s.URL+"/id/hostlier-controller", p); err != nil {
					t.Fatal(err)
				}
			}
			routes := current["handle"].([]any)[0].(map[string]any)["routes"].([]any)
			first := routes[0].(map[string]any)
			handler := first["handle"].([]any)[0].(map[string]any)
			ordinary := routes[1].(map[string]any)["handle"].([]any)[0].(map[string]any)
			wantRPC, wantHTTP := "127.0.0.1:7463", "127.0.0.1:7463"
			if mode == "nginx-gateway" {
				wantRPC, wantHTTP = "127.0.0.1:14444", "127.0.0.1:14443"
			}
			upstream := func(h map[string]any) any { return h["upstreams"].([]any)[0].(map[string]any)["dial"] }
			if patches != 1 || upstream(handler) != wantRPC || upstream(ordinary) != wantHTTP || first["match"].([]any)[0].(map[string]any)["path"].([]any)[0] != controledge.RPCPrefix+"*" || handler["transport"].(map[string]any)["versions"].([]any)[0] != "h2c" || ordinary["transport"] != nil {
				t.Fatal("route lost its protocol/upstream", current)
			}
			handler["upstreams"] = []any{map[string]any{"dial": "unexpected:443"}}
			if _, _, err := controllerRPCUpgrade(p, current); err == nil {
				t.Fatal("adopted a foreign RPC upstream")
			}
		})
	}
}
