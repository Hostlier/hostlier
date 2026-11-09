package runtime

import (
	"hostlier/internal/model"
	"regexp"
)

// grpcPolicyRoutes creates exact, case-sensitive RPC permissions with a terminal denial fallback.
func grpcPolicyRoutes(p model.Project, upstream string) []any {
	routes := []any{}
	// One independently configured proxy preserves each method's inherited I/O budget.
	for _, rule := range model.GRPCRules(*p.Endpoint) {
		copy := p
		e := *p.Endpoint
		e.IdleTimeoutSeconds = rule.IdleTimeoutSeconds
		copy.Endpoint = &e
		match := exactPolicyMatch(rule.Path, []string{"POST"})
		match["protocol"] = "grpc"
		routes = append(routes, terminalPolicyRule([]any{match}, endpointProxy(copy, upstream)))
	}
	deny := map[string]any{"handler": "static_response", "status_code": 200, "headers": map[string]any{"Content-Type": []string{"application/grpc"}, "Grpc-Status": []string{"7"}, "Grpc-Message": []string{"Blocked by Hostlier gRPC policy"}}}
	return append(routes, terminalPolicyRule(nil, deny))
}

// httpPolicyRoutes preserves application streaming semantics while enforcing exact paths and methods.
func httpPolicyRoutes(p model.Project, upstream string) []any {
	routes := []any{}
	// Stable rule ordering makes repeated edge reloads byte-for-byte reproducible.
	for _, name := range model.HTTPRouteNames(*p.Endpoint.HTTP) {
		rule := p.Endpoint.HTTP.Routes[name]
		copy := p
		e := *p.Endpoint
		e.IdleTimeoutSeconds = model.InheritedIdleTimeout(rule.IdleTimeoutSeconds, e.IdleTimeoutSeconds)
		upgrade := rule.Protocol == "websocket"
		e.WebSocket = &upgrade
		copy.Endpoint = &e
		routes = append(routes, terminalPolicyRule(httpPolicyMatches(rule), endpointProxy(copy, upstream)))
	}
	deny := map[string]any{"handler": "static_response", "status_code": 403, "body": "Blocked by Hostlier HTTP policy"}
	return append(routes, terminalPolicyRule(nil, deny))
}

// exactPolicyMatch avoids Caddy's case-insensitive path matcher for protobuf and application identities.
func exactPolicyMatch(path string, methods []string) map[string]any {
	return map[string]any{"path_regexp": map[string]any{"pattern": "^" + regexp.QuoteMeta(path) + "$"}, "method": methods}
}

// httpPolicyMatches combines method permissions with supported client protocols and WebSocket handshakes.
func httpPolicyMatches(rule model.HTTPRoutePolicy) []any {
	versions := rule.HTTPVersions
	// An omitted version restriction preserves the existing HTTP/1 WebSocket contract.
	if len(versions) == 0 && rule.Protocol == "websocket" {
		versions = []string{"h1"}
	}
	// An omitted version restriction accepts any protocol enabled on the owned edge.
	if len(versions) == 0 {
		versions = []string{""}
	}
	matches := []any{}
	// Caddy ORs matcher sets, while fields within each matcher set are ANDed.
	for _, version := range versions {
		// WebSocket methods and handshake fields differ between Upgrade and extended CONNECT.
		if rule.Protocol == "websocket" {
			matches = append(matches, websocketPolicyMatch(rule.Path, version))
			continue
		}
		match := exactPolicyMatch(rule.Path, rule.Methods)
		// An explicit version is admission control, not merely an advertised capability.
		if version != "" {
			match["protocol"] = map[string]string{"h1": "http/1.1", "h2": "http/2", "h3": "http/3"}[version]
		}
		matches = append(matches, match)
	}
	return matches
}

// websocketPolicyMatch admits only the real protocol-specific WebSocket handshake, never generic CONNECT.
func websocketPolicyMatch(path, version string) map[string]any {
	match := exactPolicyMatch(path, []string{"CONNECT"})
	match["protocol"] = map[string]string{"h1": "http/1.1", "h2": "http/2", "h3": "http/3"}[version]
	// HTTP/1 keeps its traditional GET Upgrade handshake.
	if version == "h1" {
		match["method"] = []string{"GET"}
		match["header_regexp"] = map[string]any{"Upgrade": map[string]any{"pattern": `(?i)^websocket$`}, "Connection": map[string]any{"pattern": `(?i)(^|,)\s*upgrade\s*(,|$)`}}
		return match
	}
	// Caddy receives the HTTP/2 pseudo-header from its HTTP/2 server implementation.
	if version == "h2" {
		match["header"] = map[string]any{":protocol": []string{"websocket"}}
		return match
	}
	// quic-go exposes the HTTP/3 :protocol pseudo-header as Request.Proto, not as a normal header.
	match["expression"] = "{http.request.proto} == 'websocket'"
	return match
}

// terminalPolicyRule prevents a failed or denied policy from falling through into another endpoint.
func terminalPolicyRule(matches []any, handler map[string]any) map[string]any {
	route := map[string]any{"handle": []any{handler}, "terminal": true}
	// A missing matcher is the explicit final denial within an already scoped endpoint.
	if matches != nil {
		route["match"] = matches
	}
	return route
}
