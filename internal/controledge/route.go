// Package controledge renders the controller's native gRPC route alongside
// its HTTP SDK, browser and WebSocket routes.
package controledge

const RPCPrefix = "/hostlier.control.v1.Controller/"

// CaddyRoute uses h2c only for RPCs; ordinary requests retain their original
// upstream protocol so WebSocket upgrades and HTTP SDKs continue to work.
func CaddyRoute(host, ordinary, rpc string) map[string]any {
	proxy := func(address string) map[string]any {
		return map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": address}}}
	}
	native := proxy(rpc)
	native["transport"] = map[string]any{"protocol": "http", "versions": []string{"h2c"}}
	native["flush_interval"] = -1
	routes := []any{
		map[string]any{"match": []any{map[string]any{"path": []string{RPCPrefix + "*"}}}, "handle": []any{native}, "terminal": true},
		map[string]any{"handle": []any{proxy(ordinary)}},
	}
	return map[string]any{"@id": "hostlier-controller", "match": []any{map[string]any{"host": []string{host}}}, "handle": []any{map[string]any{"handler": "subroute", "routes": routes}}, "terminal": true}
}

// GatewayHeaders preserves the original connection metadata for the private Nginx listener.
func GatewayHeaders() map[string]any {
	return map[string]any{"X-Hostlier-Gateway-Method": []string{"{http.request.method}"}, "X-Hostlier-Gateway-Protocol": []string{"{http.request.proto_name}"}, "X-Hostlier-Gateway-Scheme": []string{"{http.request.scheme}"}}
}
