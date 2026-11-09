// Package nginx implements Hostlier's explicitly owned alternative HTTPS edge.
package nginx

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// Route binds a validated project route to one or more private runtime endpoints.
type Route struct {
	Gateway     bool
	Project     model.Project
	Upstreams   []string
	Unavailable bool
}

// safePath prevents configuration or command injection through administrator paths.
func safePath(path string) bool { return regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`).MatchString(path) }

// Render builds a standalone configuration with HTTP challenges and certificate-valid HTTPS.
func Render(o Options, controller, upstream string, routes []Route) ([]byte, error) {
	o = defaults(o)
	// Listener values cannot inject directives into the generated configuration.
	if !validListener(o.HTTPAddress) || !validListener(o.HTTPSAddress) {
		return nil, fmt.Errorf("invalid nginx listener")
	}
	// Trusting gateway metadata is safe only on explicitly private, separate backend listeners.
	if err := validateGatewayOptions(o); err != nil {
		return nil, err
	}
	// All interpolated paths belong to the explicitly selected standalone edge.
	for _, path := range []string{o.Directory, o.CertificateDirectory, o.ChallengeDirectory} {
		// Validate the selected edge ownership and configuration before proceeding.
		if !safePath(path) {
			return nil, fmt.Errorf("unsafe nginx path")
		}
	}
	groups, err := groupRoutes(controller, routes)
	// Canonical grouping must succeed before rendering any configuration.
	if err != nil {
		return nil, err
	}

	hosts := make([]string, 0, len(groups))
	// Stable host order makes reviewed configuration reproducible.
	for host := range groups {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	var body strings.Builder
	body.WriteString(httpPreamble(o))
	// Each host receives its own complete TLS server and owned locations.
	for _, host := range hosts {
		text, err := hostServer(o, host, controller, upstream, groups[host])
		// Stop activation when the required local operation fails.
		if err != nil {
			return nil, err
		}
		body.WriteString(text)
	}
	body.WriteString("}\n")
	return []byte(body.String()), nil
}

// httpPreamble renders process settings and a shared ACME challenge/HTTPS redirect listener.
func httpPreamble(o Options) string {
	// A gateway backend has no public ACME, redirect or TLS listener.
	if o.Gateway {
		return gatewayPreamble(o)
	}
	return fmt.Sprintf("worker_processes auto;\npid %s/nginx.pid;\nerror_log stderr warn;\nevents { worker_connections 1024; }\nhttp {\n access_log off;\n map $http_upgrade $connection_upgrade { default upgrade; '' close; }\n server { listen %s; server_name _; location ^~ /.well-known/acme-challenge/ { root %s; } location / { return 308 https://$host$request_uri; } }\n", o.Directory, o.HTTPAddress, o.ChallengeDirectory)
}

// Bootstrap creates the HTTP-only challenge server before the first certificate is available.
func Bootstrap(o Options) ([]byte, error) {
	o = defaults(o)
	// The gateway backend starts fail-closed before application routes are published.
	if o.Gateway {
		return gatewayBootstrap(o)
	}
	// Bootstrap paths follow the same restricted configuration grammar.
	if !safePath(o.Directory) || !safePath(o.ChallengeDirectory) || !validListener(o.HTTPAddress) {
		return nil, fmt.Errorf("unsafe nginx path")
	}
	return []byte(httpPreamble(o) + "}\n"), nil
}

// hostServer renders one host and rejects unsafe upstreams before nginx parses configuration.
func hostServer(o Options, host, controller, upstream string, routes []Route) (string, error) {
	cert := filepath.Join(o.CertificateDirectory, "live", host)
	text := fmt.Sprintf(" server { listen %s ssl http2; server_name %s; ssl_certificate %s/fullchain.pem; ssl_certificate_key %s/privkey.pem; ssl_protocols TLSv1.2 TLSv1.3;\n", o.HTTPSAddress, host, cert, cert)
	// Public transport terminates at the gateway; backend metadata is accepted only on loopback.
	if o.Gateway {
		text = gatewayHost(o, host)
	}
	// The reserved controller route also supports streamed events and upgraded requests.
	if host == controller {
		// Give only digest chunk URLs room for a base64-encoded 1 MiB chunk.
		// Set an explicit cap for other routes even if the host has a larger default.
		text += " client_max_body_size 1m;\n"
		location, err := proxyLocation("/", false, []string{upstream})
		if err != nil {
			return "", err
		}
		artifactLocation := strings.Replace(location, "location ^~ / {", `location ~ "^/v1/artifacts/[a-f0-9]{64}/chunks$" { client_max_body_size 2m;`, 1)
		// A normal prefix lets Nginx choose the exact chunk regex first.
		location = strings.Replace(location, "location ^~ / {", "location / {", 1)
		return text + gatewayForwardedScheme(artifactLocation+location, o.Gateway) + " }\n", nil
	}
	// Order specific routes before their parent locations.
	sort.Slice(routes, func(i, j int) bool { return len(routes[i].Project.Route) > len(routes[j].Project.Route) })
	// Route declarations preserve segment boundaries and explicit scale-to-zero behavior.
	for _, route := range routes {
		route.Gateway = o.Gateway
		location, err := renderRoute(route)
		// Stop activation when the required local operation fails.
		if err != nil {
			return "", err
		}
		text += location
	}
	// A root application already owns the fallback location.
	for _, route := range routes {
		parsed, _ := config.ParseRoute(route.Project.Route, nil, "")
		// Validate the selected edge ownership and configuration before proceeding.
		if parsed.Path == "/" {
			return text + " }\n", nil
		}
	}
	return text + " location / { return 404; }\n }\n", nil
}

// renderRoute preserves exact typed protocol paths and independently matched descendants.
func renderRoute(route Route) (string, error) {
	parsed, err := config.ParseRoute(route.Project.Route, nil, "")
	// Canonical parsing supplies the validated host and path grammar.
	if err != nil {
		return "", err
	}
	// Gateway policies validate original client methods and protocol metadata before HTTP/1 bridging.
	if route.Gateway {
		return renderGatewayRoute(route, parsed.Path)
	}
	// Policy endpoints generate exact allow locations and reserve the rest of their path scope.
	if model.HasTrafficPolicy(route.Project.Endpoint) {
		return trafficPolicyLocations(route, parsed.Path)
	}
	strip := route.Project.Proxy != nil && route.Project.Proxy.StripPrefix
	prefix, err := bareEndpointLocation(route, parsed.Path, strip)
	// Exact protocol paths and descendant prefixes must both render successfully.
	if err != nil {
		return "", err
	}
	// Zero-capacity applications reserve their route without waking workloads.
	if route.Unavailable {
		return prefix + fmt.Sprintf(" location ^~ %s { return 503; }\n", descendantPath(parsed.Path)), nil
	}
	location, err := endpointLocation(route.Project, parsed.Path, strip, route.Upstreams)
	return prefix + location, err
}

// descendantPath appends a slash only for a non-root prefix.
func descendantPath(path string) string {
	// Validate the selected edge ownership and configuration before proceeding.
	if path == "/" {
		return path
	}
	return path + "/"
}

// proxyLocation renders buffered-off HTTP/1.1 proxying and an optional prefix rewrite.
func proxyLocation(path string, strip bool, upstreams []string) (string, error) {
	// An active route always requires an address validated as a private endpoint.
	if len(upstreams) != 1 {
		return "", fmt.Errorf("nginx route requires exactly one runtime service endpoint")
	}
	// Stop activation when the required local operation fails.
	if err := validUpstream(upstreams[0]); err != nil {
		return "", err
	}
	target := "http://" + upstreams[0]
	// A trailing URI slash asks nginx to strip the matched location prefix.
	if strip && path != "/" {
		target += "/"
	}
	return fmt.Sprintf(" location ^~ %s { proxy_pass %s; proxy_http_version 1.1; proxy_set_header Host $host; proxy_set_header X-Forwarded-Proto $scheme; proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade; proxy_buffering off; proxy_request_buffering off; proxy_read_timeout 3600s; }\n", descendantPath(path), target), nil
}

// validUpstream confines proxy destinations to numeric local runtime endpoints.
func validUpstream(address string) error {
	host, port, err := net.SplitHostPort(address)
	// Reject directive metacharacters, unresolved names and missing service ports.
	if err != nil || net.ParseIP(host) == nil || !regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(port) {
		return fmt.Errorf("invalid nginx upstream")
	}
	return nil
}

// validListener accepts only explicit numeric interface addresses and ports.
func validListener(value string) bool {
	return regexp.MustCompile(`^[0-9a-fA-F.:\[\]]+$`).MatchString(value)
}

// groupRoutes validates hostname ownership before creating per-host TLS servers.
func groupRoutes(controller string, routes []Route) (map[string][]Route, error) {
	groups := map[string][]Route{}
	// Group routes by host so path applications share the same certificate and server.
	for _, route := range routes {
		parsed, err := config.ParseRoute(route.Project.Route, nil, "")
		// Stop activation when the required local operation fails.
		if err != nil {
			return nil, err
		}
		groups[parsed.Hostname] = append(groups[parsed.Hostname], route)
	}
	// Keep the controller hostname distinct from application ownership.
	if controller != "" {
		// Validate the selected edge ownership and configuration before proceeding.
		if _, err := config.NormalizeDomain(controller); err != nil {
			return nil, err
		}
		// Validate the selected edge ownership and configuration before proceeding.
		if len(groups[controller]) > 0 {
			return nil, fmt.Errorf("application route conflicts with controller")
		}
		groups[controller] = nil
	}
	return groups, nil
}
