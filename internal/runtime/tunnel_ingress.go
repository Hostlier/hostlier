package runtime

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/url"
	"time"

	"hostlier/internal/model"
)

// EnsureTunnelIngress publishes one exact hostname through the owned Caddy edge.
// An externally managed Nginx edge uses the operator's wildcard ingress instead.
func (r *Runtime) EnsureTunnelIngress(ctx context.Context, publicURL string) error {
	if usesNginx(r.options.Proxy) {
		return nil
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return model.Err("INVALID_TUNNEL_URL", "Tunnel ingress requires an HTTPS origin")
	}
	r.edgeMu.Lock()
	defer r.edgeMu.Unlock()
	data, err := request(ctx, r.web, "GET", r.options.CaddyAdmin+"/config/", nil, nil)
	if err != nil {
		return err
	}
	configuration := map[string]any{}
	if string(data) != "null" && json.Unmarshal(data, &configuration) != nil {
		return model.Err("CADDY_CONFIG", "Cannot read Caddy configuration")
	}
	servers := object(object(object(configuration, "apps"), "http"), "servers")
	server, ok := servers["hostlier"].(map[string]any)
	if !ok {
		if err := rejectForeignHTTPS(servers); err != nil {
			return err
		}
		server = map[string]any{"listen": []string{":443"}}
		servers["hostlier"] = server
	}
	host := u.Hostname()
	routes, _ := server["routes"].([]any)
	for _, raw := range routes {
		route, _ := raw.(map[string]any)
		if !tunnelRouteMatchesHost(route, host) {
			continue
		}
		if !tunnelRouteForController(route, r.options.ControllerUpstream) {
			return model.Err("ROUTE_OWNED", "Tunnel hostname is already routed to another upstream")
		}
		return r.tunnelTLSReady(ctx, publicURL)
	}
	routes = append(routes, map[string]any{
		"@id":      "hostlier-tunnel-" + host,
		"match":    []any{map[string]any{"host": []string{host}}},
		"handle":   []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": r.options.ControllerUpstream}}, "flush_interval": -1}},
		"terminal": true,
	})
	server["routes"] = routes
	if _, err = request(ctx, r.web, "POST", r.options.CaddyAdmin+"/load", configuration, nil); err != nil {
		return err
	}
	return r.tunnelTLSReady(ctx, publicURL)
}

func tunnelRouteMatchesHost(route map[string]any, host string) bool {
	matches, _ := route["match"].([]any)
	for _, raw := range matches {
		match, _ := raw.(map[string]any)
		hosts, _ := match["host"].([]any)
		for _, value := range hosts {
			if value == host {
				return true
			}
		}
	}
	return false
}

func tunnelRouteForController(route map[string]any, upstream string) bool {
	matches, _ := route["match"].([]any)
	if len(matches) != 1 || route["terminal"] != true {
		return false
	}
	match, _ := matches[0].(map[string]any)
	if len(match) != 1 {
		return false
	}
	handles, _ := route["handle"].([]any)
	if len(handles) != 1 {
		return false
	}
	proxy, _ := handles[0].(map[string]any)
	if proxy["handler"] != "reverse_proxy" {
		return false
	}
	upstreams, _ := proxy["upstreams"].([]any)
	if len(upstreams) != 1 {
		return false
	}
	selected, _ := upstreams[0].(map[string]any)
	return selected["dial"] == upstream
}

// tunnelTLSReady verifies the public certificate without waiting for a controller response.
// Tunnel creation holds the controller state lock, so an HTTP probe would deadlock.
func (r *Runtime) tunnelTLSReady(ctx context.Context, publicURL string) error {
	if r.options.SkipPublicHTTPSProbe {
		return nil
	}
	deadline := time.Now().Add(r.options.HealthTimeout)
	u, _ := url.Parse(publicURL)
	address := u.Host
	if u.Port() == "" {
		address = net.JoinHostPort(u.Hostname(), "443")
	}
	for time.Now().Before(deadline) {
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		connection, err := (&tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}}).DialContext(probe, "tcp", address)
		if err == nil {
			connection.Close()
			cancel()
			return nil
		}
		cancel()
		if err = pause(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return model.Err("HTTPS_NOT_READY", "Tunnel HTTPS not ready; verify DNS, certificate issuance and public TCP 80/443")
}

// EnsureTunnelIngress keeps Kubernetes workloads behind the same owned edge.
func (k *K3s) EnsureTunnelIngress(ctx context.Context, publicURL string) error {
	return k.base.EnsureTunnelIngress(ctx, publicURL)
}

var _ model.TunnelIngressRuntime = (*Runtime)(nil)
var _ model.TunnelIngressRuntime = (*K3s)(nil)
