package nginx

import (
	"fmt"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"net"
	"strings"
)

// endpointLocation renders native gRPC separately from HTTP upgrade and streaming behavior.
func endpointLocation(p model.Project, path string, strip bool, upstreams []string) (string, error) {
	// Legacy declarations retain their established HTTP proxy behavior.
	if p.Endpoint == nil {
		return proxyLocation(path, strip, upstreams)
	}
	// Renderer callers share the same validated transport boundary as controller planning.
	if err := config.ValidateEndpointTransport(*p.Endpoint); err != nil {
		return "", err
	}
	// The standalone renderer cannot silently downgrade a requested client transport either.
	if err := validateNginxEndpoint(p.Endpoint); err != nil {
		return "", err
	}
	// All routes retain private numeric upstream validation.
	if len(upstreams) != 1 {
		return "", fmt.Errorf("endpoint requires one private upstream")
	}
	// Configuration text cannot accept arbitrary socket or directive fragments.
	if err := validUpstream(upstreams[0]); err != nil {
		return "", err
	}
	e := p.Endpoint
	// Native gRPC preserves method paths and trailers over HTTP/2.
	if e.Protocol == "grpc" || e.Protocol == "grpcs" {
		return grpcLocation(path, upstreams[0], e), nil
	}
	location, err := proxyLocation(path, strip, upstreams)
	// Invalid generated HTTP locations cannot be modified into valid ones.
	if err != nil {
		return "", err
	}
	// TLS upstreams always verify their certificate and selected identity.
	if e.Protocol == "https" {
		location = strings.Replace(location, "proxy_pass http://", "proxy_pass https://", 1)
		location = strings.Replace(location, " }\n", upstreamTLS("proxy", upstreams[0], e)+" }\n", 1)
	}
	return applyHTTPPolicy(location, e), nil
}

// grpcLocation preserves gRPC framing, authority, trailers and long-lived stream I/O.
func grpcLocation(path, upstream string, e *model.Endpoint) string {
	protocol := "grpc"
	tls := ""
	// Encrypted upstreams use the same verified trust roots as ordinary HTTPS.
	if e.Protocol == "grpcs" {
		protocol = "grpcs"
		tls = upstreamTLS("grpc", upstream, e)
	}
	timeout := e.IdleTimeoutSeconds
	// An omitted timeout preserves the established one-hour idle budget.
	if timeout == 0 {
		timeout = 3600
	}
	return fmt.Sprintf(" location ^~ %s { grpc_pass %s://%s; grpc_set_header Host $host; grpc_set_header X-Forwarded-Proto $scheme; grpc_set_header X-Forwarded-For $proxy_add_x_forwarded_for; grpc_read_timeout %ds; grpc_send_timeout %ds; grpc_socket_keepalive on;%s }\n", descendantPath(path), protocol, upstream, timeout, timeout, tls)
}

// upstreamTLS renders only a validated server name or numeric upstream identity.
func upstreamTLS(prefix, upstream string, e *model.Endpoint) string {
	host, _, _ := net.SplitHostPort(upstream)
	// Explicit hostnames let certificates remain valid behind numeric private routing.
	if e.UpstreamServerName != "" {
		host = e.UpstreamServerName
	}
	return fmt.Sprintf(" %s_ssl_verify on; %s_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt; %s_ssl_server_name on; %s_ssl_name %s;", prefix, prefix, prefix, prefix, host)
}

// applyHTTPPolicy changes only typed upgrade, buffering and idle timeout directives.
func applyHTTPPolicy(location string, e *model.Endpoint) string {
	// WebSockets are an HTTP upgrade feature and can be disabled per endpoint.
	if e.WebSocket != nil && !*e.WebSocket {
		location = strings.Replace(location, "proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade;", "proxy_set_header Upgrade \"\"; proxy_set_header Connection \"\";", 1)
	}
	// Buffered endpoints explicitly opt out of the default streaming-friendly proxy policy.
	if e.Streaming != nil && !*e.Streaming {
		location = strings.ReplaceAll(location, "buffering off", "buffering on")
	}
	// Stream budgets are idle I/O deadlines rather than whole-call deadlines.
	if e.IdleTimeoutSeconds > 0 {
		location = strings.Replace(location, "proxy_read_timeout 3600s;", fmt.Sprintf("proxy_read_timeout %ds; proxy_send_timeout %ds;", e.IdleTimeoutSeconds, e.IdleTimeoutSeconds), 1)
	}
	return location
}

// validateNginxEndpoint keeps direct renderers consistent with runtime preflight capability checks.
func validateNginxEndpoint(e *model.Endpoint) error {
	// Generic HTTP/2 upstreams cannot be proxied as HTTP/1 or as native gRPC.
	if e.Protocol == "h2c" {
		return fmt.Errorf("generic h2c upstream requires Caddy")
	}
	// This Nginx adapter does not generate QUIC listeners.
	for _, version := range e.HTTPVersions {
		// A requested capability must be implemented rather than merely omitted.
		if version == "h3" {
			return fmt.Errorf("HTTP/3 ingress requires the Caddy edge")
		}
	}
	return validateHTTPPolicyEdge(e.HTTP)
}

// bareEndpointLocation preserves exact protocol resources while retaining legacy web-prefix redirects.
func bareEndpointLocation(route Route, path string, strip bool) (string, error) {
	// Root locations already match both bare and descendant requests.
	if path == "/" {
		return "", nil
	}
	// Existing untyped web applications retain their canonical trailing slash behavior.
	if !model.EndpointPreservesPath(route.Project) {
		return fmt.Sprintf(" location = %s { return 308 %s/$is_args$args; }\n", path, path), nil
	}
	// Zero-capacity reservations cannot accidentally proxy their exact resource path.
	if route.Unavailable {
		return fmt.Sprintf(" location = %s { return 503; }\n", path), nil
	}
	location, err := endpointLocation(route.Project, path, strip, route.Upstreams)
	return strings.Replace(location, "location ^~ "+descendantPath(path), "location = "+path, 1), err
}
