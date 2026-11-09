package runtime

import (
	"context"
	"fmt"
	"hostlier/internal/controledge"
	"hostlier/internal/model"
	edge "hostlier/internal/nginx"
	"net"
	"os"
	"path/filepath"
	"time"
)

// nginxGatewayOptions keeps backend assets and sockets separate from public or operator-owned Nginx.
func (r *Runtime) nginxGatewayOptions() edge.Options {
	return edge.Options{Gateway: true, Directory: r.options.NginxDirectory}
}

// nginxGateway publishes the private Nginx policy layer before switching the public gateway atomically.
func (r *Runtime) nginxGateway(ctx context.Context, s state) error {
	o := r.nginxGatewayOptions()
	// The runtime's default directory is explicit so recovery checks cannot choose another owner.
	if o.Directory == "" {
		o.Directory = "/etc/hostlier/nginx-gateway"
	}
	previous, err := os.ReadFile(filepath.Join(o.Directory, "nginx.conf"))
	// Missing backend assets require the managed gateway setup, never an automatic public listener takeover.
	if err != nil {
		return model.Err("EDGE_GATEWAY_UNAVAILABLE", "Initialize the private Nginx gateway backend before selecting nginx-gateway")
	}
	routes := []edge.Route{}
	// Each actual application upstream is reachable only through Nginx, including native gRPC.
	for _, app := range expandedApplications(s) {
		routes = append(routes, edge.Route{Project: app.Spec, Upstreams: []string{app.Upstream}, Unavailable: model.DesiredReplicas(app.Spec) == 0})
	}
	manager := edge.Manager{Options: o}
	// Nginx syntax checks and rollback precede publication at the public edge.
	if err := manager.Apply(ctx, r.options.Hostname, r.options.ControllerUpstream, routes); err != nil {
		return err
	}
	// Failed frontend publication restores the previous private routing layer as well.
	if err := r.publishCaddy(ctx, s, r.nginxGatewayRoute); err != nil {
		rollback := edge.Activate(ctx, o, previous)
		return fmt.Errorf("gateway publication failed: %w; backend rollback: %v", err, rollback)
	}
	return nil
}

// nginxGatewayRoute lowers only the gateway-to-Nginx transport, retaining the signed public admission policy.
func (r *Runtime) nginxGatewayRoute(app appRecord) (map[string]any, error) {
	copy := app
	upstream := "127.0.0.1:14443"
	// Every copied endpoint preserves client policies while Nginx owns application transport selection.
	if app.Spec.Endpoint != nil {
		e := *app.Spec.Endpoint
		e.UpstreamServerName = ""
		// Native gRPC uses the distinct private h2c listener, preserving frames and trailers.
		if e.Protocol == "grpc" || e.Protocol == "grpcs" {
			e.Protocol = "grpc"
			upstream = "127.0.0.1:14444"
		} else { // HTTP and WebSocket clients reach Nginx through a private HTTP/1 connection.
			e.Protocol = "http"
		}
		copy.Spec.Endpoint = &e
	}
	copy.Upstream = upstream
	route, err := applicationEdgeRoute(copy)
	// Rendering must succeed before attaching trusted, overwritten transport metadata.
	if err == nil {
		gatewayProxyHeaders(route)
	}
	return route, err
}

// controllerEdgeRoute keeps local controller administration behind the same gateway backend when selected.
func (r *Runtime) controllerEdgeRoute() map[string]any {
	upstream := r.options.ControllerUpstream
	rpc := upstream
	// In gateway mode Nginx also owns the controller's reverse proxy route.
	if r.options.Proxy == "nginx-gateway" {
		upstream = "127.0.0.1:14443"
		rpc = "127.0.0.1:14444"
	}
	route := controledge.CaddyRoute(r.options.Hostname, upstream, rpc)
	// Ordinary Caddy deployments do not introduce gateway metadata.
	if r.options.Proxy == "nginx-gateway" {
		gatewayProxyHeaders(route)
	}
	return route
}

// gatewayProxyHeaders traverses generated handlers, overwriting all client-supplied transport claims.
func gatewayProxyHeaders(value map[string]any) {
	// Metadata comes from the original request, before Caddy converts CONNECT into HTTP/1 Upgrade.
	if value["handler"] == "reverse_proxy" {
		headers := object(object(value, "headers"), "request")
		headers["set"] = controledge.GatewayHeaders()
	}
	// Generated routes have a bounded nesting depth and no request-defined configuration nodes.
	for _, key := range []string{"handle", "routes"} {
		children, _ := value[key].([]any)
		// Matchers are never rewritten; only actual reverse proxy handlers receive metadata.
		for _, child := range children {
			// The renderer constructs object children rather than arbitrary external values.
			if node, ok := child.(map[string]any); ok {
				gatewayProxyHeaders(node)
			}
		}
	}
}

// nginxGatewayHealth checks both managed routing layers without altering either configuration.
func (r *Runtime) nginxGatewayHealth(ctx context.Context) error {
	// A valid configuration cannot conceal a stopped private backend.
	if err := nginxGatewayListeners(ctx); err != nil {
		return err
	}
	// Private backend syntax errors remain visible even if the frontend is responding.
	if err := edge.Check(ctx, r.nginxGatewayOptions()); err != nil {
		return err
	}
	_, err := request(ctx, r.web, "GET", r.options.CaddyAdmin+"/config/", nil, nil)
	return err
}

// validateNginxGatewayFeatures preserves early capability checks without claiming native Nginx CONNECT.
func (r *Runtime) validateNginxGatewayFeatures(ctx context.Context, p model.Project) error {
	// Nginx still owns application transport, including its existing generic h2c limitation.
	for _, endpoint := range model.EndpointProjects(p) {
		// The public gateway does not bypass Nginx to reach an unsupported application upstream.
		if endpoint.Endpoint != nil && endpoint.Endpoint.Protocol == "h2c" {
			return model.Err("EDGE_PROTOCOL_UNSUPPORTED", "The Nginx gateway backend cannot proxy generic h2c application upstreams")
		}
	}
	// Real public protocol negotiation is checked by the same Caddy capability gate as direct mode.
	if err := r.validateExtendedWebSocket(ctx, p); err != nil {
		return err
	}
	// Both backend sockets must be live before changing an application revision.
	if err := nginxGatewayListeners(ctx); err != nil {
		return err
	}
	return edge.Check(ctx, r.nginxGatewayOptions())
}

// nginxGatewayListeners bounds private backend checks for health reporting and deployment preflight.
func nginxGatewayListeners(ctx context.Context) error {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	// Both fixed loopback sockets must be live before changing an application revision.
	for _, address := range []string{"127.0.0.1:14443", "127.0.0.1:14444"} {
		connection, err := dialer.DialContext(ctx, "tcp", address)
		// Missing managed backend never falls back to direct application routing.
		if err != nil {
			return model.Err("EDGE_GATEWAY_UNAVAILABLE", "Start hostlier-nginx-gateway.service before selecting nginx-gateway")
		}
		connection.Close()
	}
	return nil
}
