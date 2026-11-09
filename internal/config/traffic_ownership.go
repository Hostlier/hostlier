package config

import (
	"fmt"
	"hostlier/internal/model"
	"strings"
)

// validatePolicyOwnership rejects a named endpoint that would shadow another endpoint's explicit permission.
func validatePolicyOwnership(p model.Project) error {
	names := model.EndpointNames(p)
	// A permission's protocol identity must reach the listener on which it was declared.
	for _, name := range names {
		e := p.Endpoints[name]
		paths := trafficPolicyPaths(e)
		// Compare each concrete policy path against all sibling route reservations.
		for _, other := range names {
			// Self-ownership never constitutes a collision.
			if name == other {
				continue
			}
			// A more specific sibling must not intercept an explicitly allowed request.
			if err := checkPolicyShadow(name, e, paths, other, p.Endpoints[other]); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkPolicyShadow reports both endpoint owners instead of silently depending on route order.
func checkPolicyShadow(name string, e model.Endpoint, paths []string, other string, sibling model.Endpoint) error {
	// Private listeners cannot shadow a public edge route.
	if e.Exposure != "public" || sibling.Exposure != "public" || len(paths) == 0 {
		return nil
	}
	route, _ := ParseRoute(e.Route, nil, "")
	next, _ := ParseRoute(sibling.Route, nil, "")
	// Parent fallbacks are harmless; equal or more specific siblings can capture a permission.
	if route.Hostname != next.Hostname || len(next.Path) < len(route.Path) {
		return nil
	}
	// Case-sensitive, segment-aware scope comparison mirrors explicit policy admission.
	for _, path := range paths {
		// Disjoint methods remain independent even when their listener port is shared.
		if withinTrafficScope(path, next.Path) {
			return invalid(fmt.Sprintf("Endpoint %q permission %s%s is shadowed by endpoint %q at %s; choose distinct paths or hosts", name, route.Hostname, path, other, sibling.Route))
		}
	}
	return nil
}

// trafficPolicyPaths enumerates only explicit permissions, leaving legacy catch-all routing compatible.
func trafficPolicyPaths(e model.Endpoint) []string {
	paths := []string{}
	// RPC identities are deterministic and include their package, service and method.
	for _, rule := range model.GRPCRules(e) {
		paths = append(paths, rule.Path)
	}
	// HTTP permissions reserve their exact application resource paths.
	if e.HTTP != nil {
		// Route name ordering keeps conflict diagnostics stable across runs.
		for _, name := range model.HTTPRouteNames(*e.HTTP) {
			paths = append(paths, e.HTTP.Routes[name].Path)
		}
	}
	return paths
}

// RouteConflictMessage names the existing owner and the conflicting public routing boundary.
func RouteConflictMessage(owner, endpoint string, existing, proposed model.Route) string {
	// Legacy declarations still receive a useful endpoint identity in diagnostics.
	if endpoint == "" {
		endpoint = "primary"
	}
	return fmt.Sprintf("Route %s conflicts with application %q endpoint %q at %s; choose another hostname or path", proposed.URL, owner, endpoint, existing.URL)
}

// RouteScopesOverlap protects the complete enclosing path, regardless of HTTP method or transport.
func RouteScopesOverlap(a, b model.Route) bool {
	return a.Hostname == b.Hostname && (a.Path == "/" || b.Path == "/" || a.Path == b.Path || strings.HasPrefix(a.Path, b.Path+"/") || strings.HasPrefix(b.Path, a.Path+"/"))
}
