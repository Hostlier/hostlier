package runtime

import (
	"hostlier/internal/model"
	"regexp"
	"slices"
)

// buildTrafficPolicyRoute attaches a deny-by-default subroute to the existing endpoint reservation.
func buildTrafficPolicyRoute(p model.Project, upstream string, route model.Route) (map[string]any, error) {
	// Policy paths are public protocol identities and cannot be rewritten after admission.
	if p.Proxy != nil && p.Proxy.StripPrefix {
		return nil, model.Err("INVALID_CONFIG", "Traffic policies cannot strip path prefixes")
	}
	match := map[string]any{"host": []string{route.Hostname}}
	// Scope matching is case-sensitive and segment-aware, including the exact enclosing path.
	if route.Path != "/" {
		match["path_regexp"] = map[string]any{"pattern": "^" + regexp.QuoteMeta(route.Path) + "(?:/.*)?$"}
	}
	routes := httpPolicyRoutesForEndpoint(p, upstream)
	return map[string]any{"@id": "hostlier-app-" + p.Name, "match": []any{match}, "handle": []any{map[string]any{"handler": "subroute", "routes": routes}}, "terminal": true}, nil
}

// httpPolicyRoutesForEndpoint selects the already validated application-protocol family.
func httpPolicyRoutesForEndpoint(p model.Project, upstream string) []any {
	// Native gRPC has a trailers-only permission error rather than an HTTP error page.
	if p.Endpoint.GRPC != nil {
		return grpcPolicyRoutes(p, upstream)
	}
	return httpPolicyRoutes(p, upstream)
}

// httpPolicyRequiresH3 enables QUIC when any reviewed route admits HTTP/3 clients.
func httpPolicyRequiresH3(p *model.HTTPPolicy) bool {
	// Legacy endpoints have no per-route version requirements.
	if p == nil {
		return false
	}
	// A route-level requirement must activate the shared UDP listener as well.
	for _, rule := range p.Routes {
		// No route may claim HTTP/3 support on an HTTP/1-and-2-only listener.
		if slices.Contains(rule.HTTPVersions, "h3") {
			return true
		}
	}
	return false
}
