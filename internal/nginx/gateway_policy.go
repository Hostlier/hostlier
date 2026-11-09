package nginx

import (
	"fmt"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"strings"
)

// renderGatewayRoute enforces the original wire contract after the trusted gateway's protocol bridge.
func renderGatewayRoute(route Route, scope string) (string, error) {
	p := route.Project
	// Policy-free legacy routes retain their existing path/upgrade behavior on the private backend.
	if !model.HasTrafficPolicy(p.Endpoint) {
		route.Gateway = false
		text, err := renderRoute(route)
		return gatewayForwardedScheme(text, true), err
	}
	// Every policy is still validated against the original signed deployment specification.
	if err := config.ValidateEndpointTransport(*p.Endpoint); err != nil {
		return "", err
	}
	// The backend cannot discover or dial arbitrary addresses supplied by a request.
	if len(route.Upstreams) != 1 {
		return "", fmt.Errorf("gateway endpoint requires one private upstream")
	}
	// All application upstreams use the same private endpoint validation as direct Nginx.
	if err := validUpstream(route.Upstreams[0]); err != nil {
		return "", err
	}
	// Native gRPC keeps full method paths and trailers over the separate h2c backend listener.
	if p.Endpoint.GRPC != nil {
		text, paths := grpcPolicyLocations(p, route.Upstreams[0], route.Unavailable)
		return gatewayForwardedScheme(text+gatewayDenyLocations(scope, paths, true), true), nil
	}
	text, paths, err := gatewayHTTPLocations(route)
	return gatewayForwardedScheme(text+gatewayDenyLocations(scope, paths, false), true), err
}

// gatewayHTTPLocations renders method and version checks from trusted transport metadata, never client claims.
func gatewayHTTPLocations(route Route) (string, []string, error) {
	p := route.Project
	text, paths := "", []string{}
	// Exact paths remain independent of route map iteration order.
	for _, name := range model.HTTPRouteNames(*p.Endpoint.HTTP) {
		rule := p.Endpoint.HTTP.Routes[name]
		copy := p
		e := *p.Endpoint
		e.HTTP, e.HTTPVersions = nil, nil
		e.IdleTimeoutSeconds = model.InheritedIdleTimeout(rule.IdleTimeoutSeconds, e.IdleTimeoutSeconds)
		upgrade := rule.Protocol == "websocket"
		e.WebSocket = &upgrade
		copy.Endpoint = &e
		location, err := endpointLocation(copy, rule.Path, false, route.Upstreams)
		// A render failure cannot silently omit an allowed resource.
		if err != nil {
			return "", nil, err
		}
		location = strings.Replace(location, "location ^~ "+descendantPath(rule.Path), "location = "+rule.Path, 1)
		location = strings.Replace(location, "{ proxy_pass", "{ "+gatewayHTTPGuards(rule)+" proxy_pass", 1)
		// A scaled-to-zero application never becomes reachable through the gateway path.
		if route.Unavailable {
			location = fmt.Sprintf(" location = %s { return 503; }\n", rule.Path)
		}
		text += location
		paths = append(paths, rule.Path)
	}
	return text, paths, nil
}

// gatewayHTTPGuards requires the exact public method and protocol even though Nginx sees HTTP/1 Upgrade.
func gatewayHTTPGuards(rule model.HTTPRoutePolicy) string {
	guard := fmt.Sprintf(`if ($http_x_hostlier_gateway_method !~ ^(%s)$) { return 403; }`, strings.Join(rule.Methods, "|"))
	versions := rule.HTTPVersions
	// Backward-compatible WebSocket rules remain HTTP/1-only unless explicitly extended.
	if len(versions) == 0 && rule.Protocol == "websocket" {
		versions = []string{"h1"}
	}
	// Original client protocol is overwritten by the gateway and trusted only on private listeners.
	if len(versions) > 0 {
		patterns := []string{}
		// Caddy's proto_name placeholder identifies H3 without confusing it with its :protocol selector.
		for _, version := range versions {
			patterns = append(patterns, map[string]string{"h1": `HTTP/1\.1`, "h2": "HTTP/2", "h3": "HTTP/3"}[version])
		}
		guard += fmt.Sprintf(` if ($http_x_hostlier_gateway_protocol !~ "^(%s)$") { return 403; }`, strings.Join(patterns, "|"))
	}
	// A genuine upstream Upgrade handshake is still required after extended CONNECT conversion.
	if rule.Protocol == "websocket" {
		guard += ` if ($request_method != GET) { return 403; } if ($http_upgrade !~* ^websocket$) { return 403; } if ($http_connection !~* "(^|,)\s*upgrade\s*(,|$)") { return 403; }`
	}
	return guard
}

// gatewayDenyLocations reserves both exact scope and descendants without duplicate allowed locations.
func gatewayDenyLocations(scope string, allowed []string, grpc bool) string {
	denial := "return 403;"
	// Native clients receive a proper gRPC permission error rather than a proxy HTML page.
	if grpc {
		denial = "default_type application/grpc; add_header grpc-status 7 always; add_header grpc-message 'Blocked by Hostlier gRPC policy' always; return 200;"
	}
	text := ""
	// Exact approved resources must not be replaced by a conflicting exact denial.
	if scope != "/" && !containsPath(allowed, scope) {
		text = fmt.Sprintf(" location = %s { %s }\n", scope, denial)
	}
	return text + fmt.Sprintf(" location ^~ %s { %s }\n", descendantPath(scope), denial)
}

// containsPath avoids introducing wildcard semantics into exact resource reservations.
func containsPath(paths []string, value string) bool {
	// The list is bounded by the same reviewed policy limits as the generated edge locations.
	for _, path := range paths {
		// Only a literal match suppresses an enclosing exact denial.
		if path == value {
			return true
		}
	}
	return false
}
