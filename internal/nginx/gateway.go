package nginx

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// gatewayDefaults assigns dedicated private HTTP/1 and h2c backend sockets.
func gatewayDefaults(o Options) Options {
	// Keep gateway assets separate from both standalone and operator-owned Nginx.
	if o.Directory == "" {
		o.Directory = "/etc/hostlier/nginx-gateway"
	}
	// Ordinary HTTP and WebSocket Upgrade use the HTTP/1 backend listener.
	if o.HTTPSAddress == "" {
		o.HTTPSAddress = "127.0.0.1:14443"
	}
	// Native gRPC retains HTTP/2 through its separate cleartext loopback socket.
	if o.H2Address == "" {
		o.H2Address = "127.0.0.1:14444"
	}
	return o
}

// validateGatewayOptions prevents transport metadata from being trusted on a publicly reachable listener.
func validateGatewayOptions(o Options) error {
	// Ordinary Nginx configurations do not trust gateway headers.
	if !o.Gateway {
		return nil
	}
	// Every backend socket must be a concrete loopback address with a valid port.
	for _, address := range []string{o.HTTPSAddress, o.H2Address} {
		host, port, err := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		number, portError := strconv.Atoi(port)
		// Wildcards, DNS names and malformed ports cannot create a trusted ingress.
		if err != nil || ip == nil || !ip.IsLoopback() || !gatewayPort(number, portError) || !validListener(address) {
			return fmt.Errorf("Nginx gateway backends require numeric loopback listeners")
		}
	}
	// Different wire protocols cannot compete for the same backend socket.
	if o.HTTPSAddress == o.H2Address {
		return fmt.Errorf("Nginx gateway HTTP/1 and HTTP/2 sockets must differ")
	}
	return nil
}

// gatewayPort excludes ephemeral and out-of-range ports from the fixed private trust boundary.
func gatewayPort(number int, err error) bool {
	return err == nil && number > 0 && number <= 65535
}

// gatewayPreamble does not expose port 80 or configure a second certificate authority.
func gatewayPreamble(o Options) string {
	return fmt.Sprintf("worker_processes auto;\npid %s/nginx.pid;\nerror_log stderr warn;\nevents { worker_connections 1024; }\nhttp {\n access_log off;\n map $http_upgrade $connection_upgrade { default upgrade; '' close; }\n", o.Directory)
}

// gatewayHost routes both private backend protocols under the original hostname.
func gatewayHost(o Options, host string) string {
	return fmt.Sprintf(" server { listen %s; listen %s http2; server_name %s;\n", o.HTTPSAddress, o.H2Address, host)
}

// gatewayBootstrap refuses traffic until the controller publishes its reviewed routes.
func gatewayBootstrap(o Options) ([]byte, error) {
	// Bootstrap and steady state use the same private listener boundary.
	if err := validateGatewayOptions(o); err != nil {
		return nil, err
	}
	// Filesystem paths are interpolated only after the renderer's restricted path check.
	if !safePath(o.Directory) {
		return nil, fmt.Errorf("unsafe Nginx gateway directory")
	}
	return []byte(gatewayPreamble(o) + gatewayHost(o, "_") + " return 503; }\n}\n"), nil
}

// gatewayForwardedScheme preserves the client's HTTPS origin after private plaintext bridging.
func gatewayForwardedScheme(location string, gateway bool) string {
	// Standalone Nginx uses its own public TLS scheme as before.
	if !gateway {
		return location
	}
	return strings.NewReplacer("X-Forwarded-Proto $scheme;", "X-Forwarded-Proto $http_x_hostlier_gateway_scheme;", "_set_header Host $host;", "_set_header Host $http_host;").Replace(location)
}

// ensureEdgeCertificates leaves TLS ownership entirely with Caddy for a managed gateway.
func ensureEdgeCertificates(ctx context.Context, o Options, controller string, routes []Route) error {
	// The private Nginx backend cannot independently issue or renew public certificates.
	if o.Gateway {
		return nil
	}
	return ensureCertificates(ctx, o, controller, routes)
}

// GatewayServiceAssets supplies a dedicated backend service without changing an external Nginx master.
func GatewayServiceAssets(directory string) (string, []byte, error) {
	o := defaults(Options{Gateway: true, Directory: directory})
	bootstrap, err := gatewayBootstrap(o)
	// Invalid listener or path selection cannot become a system service.
	if err != nil {
		return "", nil, err
	}
	unit := fmt.Sprintf("[Unit]\nDescription=Hostlier private Nginx gateway backend\nAfter=network-online.target\nWants=network-online.target\n[Service]\nType=forking\nPIDFile=%s/nginx.pid\nExecStartPre=/usr/sbin/nginx -t -c %s/nginx.conf\nExecStart=/usr/sbin/nginx -c %s/nginx.conf\nExecReload=/usr/sbin/nginx -s reload -c %s/nginx.conf\nExecStop=/usr/sbin/nginx -s quit -c %s/nginx.conf\nRestart=on-failure\n[Install]\nWantedBy=multi-user.target\n", o.Directory, o.Directory, o.Directory, o.Directory, o.Directory)
	return unit, bootstrap, nil
}
