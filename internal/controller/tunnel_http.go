package controller

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"hostlier/internal/tunnel"
)

// serveTunnel prevents public app hostnames from ever reaching management surfaces.
func (c *Controller) serveTunnel(w http.ResponseWriter, r *http.Request, local bool) bool {
	// Public tunnel hosts are isolated from every management and enrollment route.
	if !local && c.tunnelHost(r.Host) {
		c.serveTunnelApp(w, r)
		return true
	}
	p := strings.Split(r.URL.Path, "/")
	// Only the exact connector endpoint bypasses ordinary management dispatch.
	if len(p) != 5 || p[1] != "v1" || p[2] != "tunnels" || p[4] != "connect" {
		return false
	}
	// The dedicated transport accepts only WebSocket GET handshakes.
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return true
	}
	broker, status := c.authenticateTunnel(r, p[3])
	// Return only a generic status when connector authentication fails.
	if status != 0 {
		http.Error(w, "Tunnel unavailable", status)
		return true
	}
	broker.AcceptHTTP(w, r)
	return true
}

// authenticateTunnel grants only a single lease's reverse stream, never management authority.
func (c *Controller) authenticateTunnel(r *http.Request, name string) (*tunnel.Broker, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok := c.state.Tunnels[name]
	// Require the explicit Bearer scheme for the scoped credential.
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return nil, 401
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	hash := verifier(token)
	// Compare only the verifier belonging to this named lease.
	if !ok || subtle.ConstantTimeCompare([]byte(hash), []byte(lease.TokenVerifier)) != 1 {
		return nil, 401
	}
	// Only unexpired, unrevoked leases may regain reverse connections.
	if c.closed || !c.tunnelLive(lease) {
		return nil, 410
	}
	broker := c.tunnelBrokers[name]
	// A disabled or unavailable gateway must fail closed.
	if broker == nil {
		return nil, 410
	}
	return broker, 0
}

// serveTunnelApp selects an exact live hostname and releases management locks before streaming.
func (c *Controller) serveTunnelApp(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSuffix(strings.ToLower(r.Host), ".")
	// Normalize an explicit authority port before hostname comparison.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	name := strings.TrimSuffix(host, "."+c.opts.TunnelDomain)
	c.mu.Lock()
	lease, exists := c.state.Tunnels[name]
	broker := c.tunnelBrokers[name]
	live := !c.closed && exists && c.tunnelLive(lease) && lease.URL == "https://"+host
	c.mu.Unlock()
	// Unknown, revoked and expired names never reach application or management code.
	if !live {
		http.NotFound(w, r)
		return
	}
	// A disabled or unavailable gateway must fail closed.
	if broker == nil {
		http.Error(w, "Tunnel unavailable", 503)
		return
	}
	// Hostlier tunnel URLs are always HTTPS; the trusted edge may terminate TLS before this handler.
	// Broker rewrites forwarding headers and never trusts visitor-supplied proxy identity.
	broker.ServeHTTP(w, r)
}
