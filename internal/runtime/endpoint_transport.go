package runtime

import (
	"context"
	"hostlier/internal/model"
	"slices"
	"time"
)

// endpointProxy configures upstream transport independently of client-facing HTTP versions.
func endpointProxy(p model.Project, upstream string) map[string]any {
	proxy := map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": upstream}}}
	e := p.Endpoint
	// Legacy applications keep the proxy's ordinary HTTP transport defaults.
	if e == nil {
		return proxy
	}
	transport := map[string]any{"protocol": "http"}
	// Cleartext HTTP/2 is required for native gRPC and generic h2c backends.
	if model.EndpointScheme(e) == "h2c" {
		transport["versions"] = []string{"h2c"}
	}
	// Encrypted backends retain normal certificate and hostname verification.
	if model.EndpointScheme(e) == "https" {
		tls := map[string]any{}
		// Only explicit TLS names replace the numeric upstream identity.
		if e.UpstreamServerName != "" {
			tls["server_name"] = e.UpstreamServerName
		}
		transport["tls"] = tls
	}
	// Native encrypted gRPC requires HTTP/2 upstream negotiation.
	if e.Protocol == "grpcs" {
		transport["versions"] = []string{"2"}
	}
	applyEndpointBudget(transport, e)
	proxy["transport"] = transport
	return streamingProxyPolicy(proxy, e)
}

// applyEndpointBudget keeps long-lived calls independent from a whole-request timeout.
func applyEndpointBudget(transport map[string]any, e *model.Endpoint) {
	// Idle stream budgets apply to transport I/O, not a maximum application lifetime.
	if e.IdleTimeoutSeconds > 0 {
		transport["read_timeout"] = int64(time.Duration(e.IdleTimeoutSeconds) * time.Second)
		transport["write_timeout"] = int64(time.Duration(e.IdleTimeoutSeconds) * time.Second)
	}

}

// streamingProxyPolicy preserves streaming responses and explicit WebSocket upgrade policy.
func streamingProxyPolicy(proxy map[string]any, e *model.Endpoint) map[string]any {
	// Forced header flushing turns trailers-only gRPC errors into a header block plus empty DATA.
	// Let the native HTTP/2 transport flush streams without breaking END_STREAM status semantics.
	if e.Protocol == "grpc" || e.Protocol == "grpcs" {
		return proxy
	}
	// Immediate flushing preserves SSE, streaming request bodies and Streamable HTTP responses.
	if e.Streaming == nil || *e.Streaming {
		proxy["flush_interval"] = -1
	}
	// A disabled upgrade policy is forwarded explicitly rather than ignored.
	if e.WebSocket != nil && !*e.WebSocket {
		proxy["headers"] = map[string]any{"request": map[string]any{"delete": []string{"Upgrade", "Connection"}}}
	}
	return proxy
}

// edgeHTTPVersions advertises required client-facing protocols on the owned TLS listener.
func edgeHTTPVersions(s state) []string {
	versions := []string{"h1", "h2"}
	// QUIC is opt-in; unrequested transports never silently claim a validated capability.
	for _, app := range runningApplications(s) {
		// Inspect each public listener without changing its workload identity.
		for _, p := range model.EndpointProjects(app.Spec) {
			// A QUIC requirement enables HTTP/3 on the shared owned listener.
			if endpointRequiresH3(p.Endpoint) {
				return append(versions, "h3")
			}
		}
	}
	return versions
}

// endpointRequiresH3 detects an explicit client-to-edge QUIC requirement.
func endpointRequiresH3(e *model.Endpoint) bool {
	// Legacy endpoint defaults do not require a UDP listener.
	if e == nil {
		return false
	}
	return slices.Contains(e.HTTPVersions, "h3") || httpPolicyRequiresH3(e.HTTP)
}

// validateEdgeFeatures rejects unsupported Nginx transports before a candidate can disturb live routes.
func (r *Runtime) validateEdgeFeatures(ctx context.Context, p model.Project) error {
	// Managed gateway mode validates both protocols before any existing workload is stopped.
	if r.options.Proxy == "nginx-gateway" {
		return r.validateNginxGatewayFeatures(ctx, p)
	}
	// Caddy's HTTP transport supports HTTP/1, HTTP/2, gRPC and HTTP/3 ingress.
	if !usesNginx(r.options.Proxy) {
		return r.validateExtendedWebSocket(ctx, p)
	}
	h3 := false
	// Native gRPC uses its dedicated Nginx module; generic h2c needs a compatible edge.
	for _, project := range model.EndpointProjects(p) {
		h2ws, h3ws := model.ExtendedWebSocketVersions(project.Endpoint)
		// Unsupported extended CONNECT must fail before stopping the previous workload.
		if h2ws || h3ws {
			return model.Err("EDGE_WEBSOCKET_UNSUPPORTED", "WebSocket over HTTP/2 or HTTP/3 requires the Caddy edge")
		}
		// Generic HTTP/2 upstreams cannot fall back to Nginx HTTP/1 proxying.
		if project.Endpoint != nil && project.Endpoint.Protocol == "h2c" {
			return model.Err("EDGE_PROTOCOL_UNSUPPORTED", "Generic h2c upstreams require the Caddy edge")
		}
		h3 = h3 || endpointRequiresH3(project.Endpoint)
	}
	// Older Nginx builds cannot be reported as providing HTTP/3.
	if !h3 {
		return nil
	}
	return model.Err("EDGE_HTTP3_UNSUPPORTED", "HTTP/3 ingress requires the Caddy edge; the Nginx adapter does not yet support QUIC")
}
