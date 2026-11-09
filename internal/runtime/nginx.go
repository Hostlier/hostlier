package runtime

import (
	"context"

	"hostlier/internal/model"
	edge "hostlier/internal/nginx"
)

// usesNginx selects both standalone and shared-master Nginx routing without a Caddy fallback.
func usesNginx(proxy string) bool {
	return proxy == "nginx" || proxy == "nginx-existing" || proxy == "nginx-gateway"
}

// nginx renders the same desired applications through the explicitly selected alternative edge.
func (r *Runtime) nginx(ctx context.Context, s state) error {
	// The explicit gateway mode keeps Nginx private behind managed public protocol termination.
	if r.options.Proxy == "nginx-gateway" {
		return r.nginxGateway(ctx, s)
	}
	routes := []edge.Route{}
	// Runtime service endpoints retain balancing responsibility on Kubernetes.
	for _, app := range expandedApplications(s) {
		routes = append(routes, edge.Route{Project: app.Spec, Upstreams: []string{app.Upstream}, Unavailable: model.DesiredReplicas(app.Spec) == 0})
	}
	// Existing masters receive only our isolated include; their other sites and certificates stay external.
	if r.options.Proxy == "nginx-existing" {
		return edge.ApplyShared(ctx, edge.SharedOptions{}, r.options.Hostname, r.options.ControllerUpstream, routes)
	}
	manager := edge.Manager{Options: edge.Options{Directory: r.options.NginxDirectory}}
	return manager.Apply(ctx, r.options.Hostname, r.options.ControllerUpstream, routes)
}

// validateProxySelection rejects accidental fallback from an unsupported proxy name.
func validateProxySelection(proxy string) error {
	// Empty selection retains the original Caddy default.
	switch proxy {
	// Both explicitly supported edge implementations have independent ownership paths.
	case "", "caddy", "nginx", "nginx-existing", "nginx-gateway":
		return nil
	// No other proxy can silently fall back to Caddy.
	default:
		return model.Err("PROXY", "Supported proxies are caddy, nginx, nginx-existing and nginx-gateway")
	}
}

// defaultProxyName supplies the visible edge identity for diagnostics.
func defaultProxyName(proxy string) string {
	// Older installation records did not carry a proxy field.
	if proxy == "" {
		return "caddy"
	}
	return proxy
}

// edgeHealth checks the selected edge without mutating routes or issuing certificates.
func (r *Runtime) edgeHealth(ctx context.Context) error {
	// A gateway is healthy only when both its private Nginx config and public frontend are healthy.
	if r.options.Proxy == "nginx-gateway" {
		return r.nginxGatewayHealth(ctx)
	}
	// Coexistence inspection is read-only and checks the external include and route boundary.
	if r.options.Proxy == "nginx-existing" {
		_, err := edge.InspectShared(ctx, edge.SharedOptions{}, []string{r.options.Hostname})
		return err
	}
	// Nginx verifies its standalone configuration rather than contacting Caddy administration.
	if r.options.Proxy == "nginx" {
		return edge.Check(ctx, edge.Options{Directory: r.options.NginxDirectory})
	}
	_, err := request(ctx, r.web, "GET", r.options.CaddyAdmin+"/config/", nil, nil)
	return err
}

// validateRuntimeOptions centralizes recovery fencing and selected proxy validation.
func validateRuntimeOptions(o Options) error {
	// Restored metadata must use its original operational ownership path.
	if err := model.ValidateRecoveryLocation(o.StateDir); err != nil {
		return err
	}
	return validateProxySelection(o.Proxy)
}

// inspectExternalRoute refuses occupied external hostnames before creating any Docker workload.
func (r *Runtime) inspectExternalRoute(ctx context.Context, route model.Route) error {
	// Private workloads and standalone edges do not reserve names in an external Nginx configuration.
	if r.options.Proxy != "nginx-existing" || route.Hostname == "" {
		return nil
	}
	_, err := edge.InspectShared(ctx, edge.SharedOptions{}, []string{route.Hostname})
	return err
}

// validateOwnedRoute checks both Hostlier applications and external hostname ownership before mutation.
func (r *Runtime) validateOwnedRoute(ctx context.Context, s state, name string, route model.Route) error {
	// Internal overlap remains an error independently of whether an external edge is selected.
	if err := validateRouteOwnership(s, name, route); err != nil {
		return err
	}
	return r.inspectExternalRoute(ctx, route)
}
