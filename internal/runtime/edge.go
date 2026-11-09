package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// BuildCaddyRoute renders segment-aware routing and optional prefix stripping for one application.
func BuildCaddyRoute(spec model.Project, upstream string) (map[string]any, error) {
	// Transport policy is validated even for callers rendering a route directly.
	if spec.Endpoint != nil {
		// Reject invalid upstream transport before generating a route.
		if err := config.ValidateEndpointTransport(*spec.Endpoint); err != nil {
			return nil, err
		}
	}
	route, e := config.ParseRoute(spec.Route, nil, "")
	// Require a valid canonical route before generating proxy matchers.
	if e != nil {
		return nil, e
	}
	// Policy endpoints own their full route scope, including rejected requests.
	if model.HasTrafficPolicy(spec.Endpoint) {
		return buildTrafficPolicyRoute(spec, upstream, route)
	}
	proxy := endpointProxy(spec, upstream)
	handles := []any{}
	// Strip only a non-root prefix explicitly requested by the project.
	if spec.Proxy != nil && spec.Proxy.StripPrefix && route.Path != "/" {
		handles = append(handles, map[string]any{"handler": "rewrite", "strip_path_prefix": route.Path})
	}
	handles = append(handles, proxy)
	matches := map[string]any{"host": []string{route.Hostname}}
	outer := map[string]any{"host": []string{route.Hostname}}
	routes := []any{}
	// Redirect a bare non-root prefix to its slash form before proxying descendants.
	if route.Path != "/" {
		matches["path"] = []string{route.Path + "/*"}
		outer["path"] = []string{route.Path, route.Path + "/*"}
		// Typed endpoints preserve bare RPC/MCP paths; legacy web prefixes retain slash redirects.
		if model.EndpointPreservesPath(spec) {
			matches["path"] = outer["path"]
		} else {
			routes = append(routes, map[string]any{"match": []any{map[string]any{"host": []string{route.Hostname}, "path": []string{route.Path}}}, "handle": []any{map[string]any{"handler": "static_response", "status_code": 308, "headers": map[string]any{"Location": []string{route.Path + "/"}}}}, "terminal": true})
		}
	}
	routes = append(routes, map[string]any{"match": []any{matches}, "handle": handles, "terminal": true})
	return map[string]any{"@id": "hostlier-app-" + spec.Name, "match": []any{outer}, "handle": []any{map[string]any{"handler": "subroute", "routes": routes}}, "terminal": true}, nil
}

// object obtains or creates a JSON object without discarding sibling configuration.
func object(parent map[string]any, key string) map[string]any {
	result, ok := parent[key].(map[string]any)
	// Replace an absent or differently typed child with a fresh object.
	if !ok {
		result = map[string]any{}
		parent[key] = result
	}
	return result
}

// caddy updates owned routes while preserving unrelated Caddy configuration.
func (r *Runtime) caddy(ctx context.Context, s state) error {
	if err := r.syncIdleRoutes(s); err != nil {
		return err
	}
	// Explicit Nginx selection uses its independently owned standalone edge.
	if usesNginx(r.options.Proxy) {
		err := r.nginx(ctx, s)
		return r.checkIdlePublication(err)
	}
	return r.checkIdlePublication(r.publishCaddy(ctx, s, applicationEdgeRoute))
}

// publishCaddy atomically updates owned routes using the selected direct or gateway application renderer.
func (r *Runtime) publishCaddy(ctx context.Context, s state, build func(appRecord) (map[string]any, error)) error {
	r.edgeMu.Lock()
	defer r.edgeMu.Unlock()
	data, e := request(ctx, r.web, "GET", r.options.CaddyAdmin+"/config/", nil, nil)
	// Do not rewrite an edge whose current configuration cannot be read.
	if e != nil {
		return e
	}
	configuration := map[string]any{}
	// Require valid JSON unless Caddy explicitly reports an empty configuration.
	if string(data) != "null" && json.Unmarshal(data, &configuration) != nil {
		return model.Err("CADDY_CONFIG", "Cannot read Caddy configuration")
	}
	servers := object(object(object(configuration, "apps"), "http"), "servers")
	server, ok := servers["hostlier"].(map[string]any)
	// Create the owned HTTPS server only when its block does not already exist.
	if !ok {
		// Refuse adoption if another server already owns public HTTPS.
		if err := rejectForeignHTTPS(servers); err != nil {
			return err
		}
		server = map[string]any{"listen": []string{":443"}}
		servers["hostlier"] = server
	}
	routes := externalRoutes(server)
	// Keep the reserved controller route active when a controller hostname is configured.
	if r.options.Hostname != "" {
		routes = append(routes, r.controllerEdgeRoute())
	}
	apps := expandedApplications(s)
	// Render active application routes in specificity order.
	for _, a := range apps {
		route, e := build(a)
		// Stop before loading any configuration if one owned route is invalid.
		if e != nil {
			return e
		}
		routes = append(routes, route)
	}
	server["routes"] = routes
	server["protocols"] = edgeHTTPVersions(s)
	_, e = request(ctx, r.web, "POST", r.options.CaddyAdmin+"/load", configuration, nil)
	return e
}

// publicReady verifies certificate-valid HTTPS before committing application readiness.
func (r *Runtime) publicReady(ctx context.Context, target string) error {
	// Explicit local test environments may bypass the external HTTPS gate.
	if target == "" || r.options.SkipPublicHTTPSProbe {
		return nil
	}
	deadline := time.Now().Add(r.options.HealthTimeout)
	// Retry certificate issuance and public routing within the configured readiness window.
	for time.Now().Before(deadline) {
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		req, _ := http.NewRequestWithContext(probe, "GET", target, nil)
		res, e := r.web.Do(req)
		ok := e == nil && res.StatusCode < 500
		// Inspect HTTPS status only when TLS and the HTTP request completed.
		if e == nil {
			res.Body.Close()
		}
		cancel()
		// Accept a reachable application response below the server-error range.
		if ok {
			return nil
		}
		// Honor cancellation while waiting for certificate or public-route readiness.
		if e = pause(ctx, 500*time.Millisecond); e != nil {
			return e
		}
	}
	return model.Err("HTTPS_NOT_READY", "HTTPS not ready; verify DNS, certificate validation and external TCP 80/443")
}

// rejectForeignHTTPS refuses automatic adoption of an existing HTTPS listener.
func rejectForeignHTTPS(servers map[string]any) error {
	// Inspect all foreign servers before creating a public HTTPS listener.
	for _, other := range servers {
		// Ignore non-object entries that cannot declare a listener.
		if candidate, yes := other.(map[string]any); yes {
			// Inspect the existing server's listener collection when present.
			if listen, yes := candidate["listen"].([]any); yes {
				// Check every address bound by the foreign server.
				for _, p := range listen {
					// A foreign wildcard HTTPS listener conflicts with automatic adoption.
					if p == ":443" || p == "0.0.0.0:443" {
						return model.Err("CADDY_CONFLICT", "Existing HTTPS listener cannot be safely adopted")
					}
				}
			}
		}
	}
	return nil
}

// externalRoutes preserves routes outside the controller-owned application namespace.
func externalRoutes(server map[string]any) []any {
	routes := []any{}
	// Carry forward existing routes when the server already has a route list.
	if existing, ok := server["routes"].([]any); ok {
		// Filter only Hostlier-owned route identifiers.
		for _, route := range existing {
			m, _ := route.(map[string]any)
			id, _ := m["@id"].(string)
			// Preserve unrelated route definitions in their original order.
			if !strings.HasPrefix(id, "hostlier-app-") && id != "hostlier-controller" {
				routes = append(routes, route)
			}
		}
	}
	return routes
}

// validateRouteOwnership rejects overlapping paths already assigned to other applications.
func validateRouteOwnership(s state, name string, route model.Route) error {
	// Private workloads do not compete for an empty public route.
	if route.Hostname == "" {
		return nil
	}
	// Compare every named public route while preserving same-app replacement.
	for _, app := range s.Apps {
		// The replacing application already owns its previous declarations.
		if app.Name == name {
			continue
		}
		// A secondary listener cannot be claimed through a legacy route.
		for endpoint, project := range model.EndpointProjects(app.Spec) {
			other, err := config.ParseRoute(project.Route, nil, "")
			// Invalid state or overlapping host/path ownership must be inspected before deployment.
			if err != nil || routesOverlap(other, route) {
				return model.Err("ROUTE_OWNED", config.RouteConflictMessage(app.Name, endpoint, other, route))
			}
		}
	}
	return nil
}

// routesOverlap treats a root route and either path ancestry as exclusive ownership.
func routesOverlap(a, b model.Route) bool {
	return config.RouteScopesOverlap(a, b)
}

// runningApplications orders live routes from most to least specific.
func runningApplications(s state) []appRecord {
	apps := []appRecord{}
	// Include only applications that should currently receive traffic.
	for _, a := range s.Apps {
		// Stopped and retained applications must not publish live edge routes.
		if model.Public(a.Spec) && (a.Status == "running" || model.DesiredReplicas(a.Spec) == 0) {
			apps = append(apps, a)
		}
	}
	// Sort longer route prefixes first so a parent route cannot shadow a child.
	sort.Slice(apps, func(i, j int) bool { return len(apps[i].Spec.Route) > len(apps[j].Spec.Route) })
	return apps
}

// applicationEdgeRoute preserves zero-capacity route reservations with an explicit unavailable response.
func applicationEdgeRoute(a appRecord) (map[string]any, error) {
	route, err := BuildCaddyRoute(a.Spec, a.Upstream)
	// Preserve route parsing failures before replacing its response handler.
	if err != nil {
		return nil, err
	}
	// Desired zero has no backend and never wakes on request.
	if model.DesiredReplicas(a.Spec) == 0 {
		route["handle"] = []any{map[string]any{"handler": "static_response", "status_code": 503, "body": "Application scaled to zero"}}
	}
	return route, nil
}
