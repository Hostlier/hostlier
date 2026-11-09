package nginx

import (
	"fmt"
	"hostlier/internal/model"
	"regexp"
	"slices"
	"strings"
)

// trafficPolicyLocations enforces reviewed exact permissions with a complete enclosing denial location.
func trafficPolicyLocations(route Route, scope string) (string, error) {
	p := route.Project
	// Reuse all transport and upstream checks before constructing policy-specific locations.
	if _, err := endpointLocation(p, scope, false, route.Upstreams); err != nil {
		return "", err
	}
	// A policy never rewrites the public path after an admission decision.
	if p.Proxy != nil && p.Proxy.StripPrefix {
		return "", fmt.Errorf("traffic policies cannot strip path prefixes")
	}
	locations, allowed := grpcPolicyLocations(p, route.Upstreams[0], route.Unavailable)
	denial := "default_type application/grpc; add_header grpc-status 7 always; add_header grpc-message 'Blocked by Hostlier gRPC policy' always; return 200;"
	// Ordinary HTTP permissions use HTTP 403 while native gRPC returns a trailers-only status.
	if p.Endpoint.HTTP != nil {
		var err error
		locations, allowed, err = httpPolicyLocations(p, route.Upstreams[0], route.Unavailable)
		// A failed per-route render cannot silently omit an explicitly permitted resource.
		if err != nil {
			return "", err
		}
		denial = "return 403;"
	}
	// Exact approved resources must not collide with an exact denial at the enclosing path.
	if scope != "/" && !slices.Contains(allowed, scope) {
		locations += fmt.Sprintf(" location = %s { %s }\n", scope, denial)
	}
	return locations + fmt.Sprintf(" location ^~ %s { %s }\n", descendantPath(scope), denial), nil
}

// grpcPolicyLocations preserves protobuf paths, trailers and per-method timeout inheritance.
func grpcPolicyLocations(p model.Project, upstream string, unavailable bool) (string, []string) {
	text := ""
	paths := []string{}
	// A nil policy belongs to the other protocol family and contributes no RPC permissions.
	if p.Endpoint.GRPC == nil {
		return text, paths
	}
	// Exact locations beat the endpoint's default-deny prefix without regex precedence surprises.
	for _, rule := range model.GRPCRules(*p.Endpoint) {
		e := *p.Endpoint
		e.IdleTimeoutSeconds = rule.IdleTimeoutSeconds
		location := grpcLocation(rule.Path, upstream, &e)
		location = strings.Replace(location, "location ^~ "+descendantPath(rule.Path), "location = "+rule.Path, 1)
		location = strings.Replace(location, "{ grpc_pass", "{ if ($request_method != POST) { return 403; } grpc_pass", 1)
		// Zero-capacity reservations cannot turn an allowed method into a live upstream call.
		if unavailable {
			location = fmt.Sprintf(" location = %s { return 503; }\n", rule.Path)
		}
		text += location
		paths = append(paths, rule.Path)
	}
	return text, paths
}

// httpPolicyLocations forwards only approved HTTP methods and compatible upgrade handshakes.
func httpPolicyLocations(p model.Project, upstream string, unavailable bool) (string, []string, error) {
	text := ""
	paths := []string{}
	// Route validation has already excluded unsafe characters from every literal path.
	for _, name := range model.HTTPRouteNames(*p.Endpoint.HTTP) {
		rule := p.Endpoint.HTTP.Routes[name]
		e := *p.Endpoint
		// The enclosing policy was validated before lowering; this copy represents one transport only.
		e.HTTP = nil
		e.IdleTimeoutSeconds = model.InheritedIdleTimeout(rule.IdleTimeoutSeconds, e.IdleTimeoutSeconds)
		upgrade := rule.Protocol == "websocket"
		e.WebSocket = &upgrade
		copy := p
		copy.Endpoint = &e
		location, err := endpointLocation(copy, rule.Path, false, []string{upstream})
		// Preserve renderer failures instead of turning an allowed route into a default denial.
		if err != nil {
			return "", nil, err
		}
		location = strings.Replace(location, "location ^~ "+descendantPath(rule.Path), "location = "+rule.Path, 1)
		location = strings.Replace(location, "{ proxy_pass", "{ "+httpPolicyGuards(rule)+" proxy_pass", 1)
		// Empty deployments preserve all exact route reservations without waking a backend.
		if unavailable {
			location = fmt.Sprintf(" location = %s { return 503; }\n", rule.Path)
		}
		text += location
		paths = append(paths, rule.Path)
	}
	return text, paths, nil
}

// httpPolicyGuards uses return-only conditions so Nginx rewrite control cannot change proxy behavior.
func httpPolicyGuards(rule model.HTTPRoutePolicy) string {
	guard := fmt.Sprintf("if ($request_method !~ ^(%s)$) { return 403; }", strings.Join(rule.Methods, "|"))
	versions := rule.HTTPVersions
	// HTTP/1 Upgrade is deliberately distinct from unsupported HTTP/2 or HTTP/3 extended CONNECT.
	if rule.Protocol == "websocket" {
		versions = []string{"h1"}
		guard += ` if ($http_upgrade !~* ^websocket$) { return 403; } if ($http_connection !~* "(^|,)\s*upgrade\s*(,|$)") { return 403; }`
	}
	// Explicit per-route version restrictions are enforced rather than only advertised.
	if len(versions) > 0 {
		patterns := []string{}
		// Nginx HTTP/3 requirements are rejected before this renderer is reached.
		for _, version := range versions {
			patterns = append(patterns, regexp.QuoteMeta(map[string]string{"h1": "HTTP/1.1", "h2": "HTTP/2.0"}[version]))
		}
		guard += fmt.Sprintf(` if ($server_protocol !~ "^(%s)$") { return 403; }`, strings.Join(patterns, "|"))
	}
	return guard
}

// validateHTTPPolicyEdge rejects route-level HTTP/3 requirements on an edge without QUIC support.
func validateHTTPPolicyEdge(p *model.HTTPPolicy) error {
	// Existing endpoint configurations have no per-route transport requirements.
	if p == nil {
		return nil
	}
	// Every admission rule must be implementable by the selected proxy adapter.
	for _, rule := range p.Routes {
		// The current Nginx adapter only bridges ordinary HTTP/1 Upgrade handshakes.
		if rule.Protocol == "websocket" && (slices.Contains(rule.HTTPVersions, "h2") || slices.Contains(rule.HTTPVersions, "h3")) {
			return fmt.Errorf("WebSocket extended CONNECT requires the Caddy edge")
		}
		// QUIC cannot be approximated by HTTP/2 or an Alt-Svc header.
		if slices.Contains(rule.HTTPVersions, "h3") {
			return fmt.Errorf("HTTP/3 policy requires the Caddy edge")
		}
	}
	return nil
}
