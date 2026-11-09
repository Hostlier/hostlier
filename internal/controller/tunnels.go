package controller

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/internal/tunnel"
)

// initializeTunnels validates public authority and restores only live lease-local brokers.
func (c *Controller) initializeTunnels() error {
	// Initialize the additive collection for older controller databases.
	if c.state.Tunnels == nil {
		c.state.Tunnels = map[string]TunnelLease{}
	}
	// Disabling publication closes access while retaining durable reservations.
	if c.opts.TunnelDomain == "" && c.opts.TunnelURL == "" {
		c.rememberTunnelGateway()
		return nil
	}
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err := c.configureTunnelAuthority(); err != nil {
		return err
	}
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err := c.restoreTunnels(); err != nil {
		return err
	}
	c.rememberTunnelGateway()
	return nil
}

// tunnelOrigin rejects ambiguous authorities and credential-bearing configuration.
func tunnelOrigin(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

// validateTunnelNamespace prevents wildcard ingress from stealing existing application routes.
func (c *Controller) validateTunnelNamespace() error {
	// Inspect every active or accepted deployment before reserving a wildcard.
	for _, spec := range c.reservedProjects() {
		// Named endpoints reserve hostnames independently of the primary endpoint.
		for _, endpoint := range model.EndpointProjects(spec) {
			route, err := config.ParseRoute(endpoint.Route, c.state.Domains, c.state.Hostname)
			// Propagate the failed prerequisite before reporting success or serving traffic.
			if err != nil {
				return err
			}
			// Reject any overlap with ordinary application ingress.
			if c.tunnelHost(route.Hostname) {
				return model.Err("ROUTE_CONFLICT", "Tunnel domain overlaps a reserved application")
			}
		}
	}
	return nil
}

// restoreTunnels forbids silently moving durable URLs when gateway configuration changes.
func (c *Controller) restoreTunnels() error {
	// Restore each durable reservation independently.
	for name, lease := range c.state.Tunnels {
		// Revoked reservations retain ownership without retaining transport.
		if lease.Revoked {
			continue
		}
		// Do not silently change URLs already issued to connector clients.
		if lease.URL != "https://"+name+"."+c.opts.TunnelDomain || lease.ConnectURL != c.connectTunnelURL(name) {
			c.closeTunnels()
			return model.Err("INVALID_CONFIG", "Revoke existing tunnels before changing the tunnel domain or URL")
		}
		// Only unexpired, unrevoked leases may regain reverse connections.
		if c.tunnelLive(lease) {
			if err := c.ensureTunnelIngress(context.Background(), lease.URL); err != nil {
				c.closeTunnels()
				return err
			}
			broker, err := tunnel.NewBroker(lease.Connections)
			// Propagate the failed prerequisite before reporting success or serving traffic.
			if err != nil {
				c.closeTunnels()
				return err
			}
			c.tunnelBrokers[name] = broker
		}
	}
	return nil
}

// ensureTunnelIngress makes the approved public URL reachable before it is reported as ready.
func (c *Controller) ensureTunnelIngress(ctx context.Context, publicURL string) error {
	if publisher, ok := c.opts.Runtime.(model.TunnelIngressRuntime); ok {
		return publisher.EnsureTunnelIngress(ctx, publicURL)
	}
	return nil
}

// tunnelHost reserves the entire suffix, including unknown names and its apex.
func (c *Controller) tunnelHost(host string) bool {
	// An unconfigured gateway cannot publish or allocate tunnel hostnames.
	if c.opts.TunnelDomain == "" {
		return false
	}
	// Normalize an explicit authority port before hostname comparison.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == c.opts.TunnelDomain || strings.HasSuffix(host, "."+c.opts.TunnelDomain)
}

// connectTunnelURL uses configured authority rather than untrusted forwarded headers.
func (c *Controller) connectTunnelURL(name string) string {
	return c.opts.TunnelURL + "/v1/tunnels/" + name + "/connect"
}

// tunnelLive checks the durable expiry even before the background timer runs.
func (c *Controller) tunnelLive(lease TunnelLease) bool {
	return !lease.Revoked && lease.ExpiresAt > c.opts.Now().UnixMilli()
}

// closeTunnels revokes active and upgraded connections under the controller state lock.
func (c *Controller) closeTunnels() {
	// Visit every broker so idle and upgraded pipes receive the lifecycle change.
	for name, broker := range c.tunnelBrokers {
		broker.Close()
		delete(c.tunnelBrokers, name)
	}
}

// startTunnelExpiry closes streams at expiry instead of only refusing subsequent requests.
func (c *Controller) startTunnelExpiry() {
	c.wg.Add(1)
	// Track the expiry worker in the controller shutdown lifecycle.
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		// Continue monitoring until controller cancellation.
		for {
			// Wait for shutdown or the next expiry check without busy polling.
			select {
			// Stop the monitor when the controller closes.
			case <-c.ctx.Done():
				return
			// Recheck durable expirations at the bounded polling interval.
			case <-ticker.C:
				c.expireTunnels()
			}
		}
	}()
}

// expireTunnels keeps idle and active reverse connections within their approved lifetime.
func (c *Controller) expireTunnels() {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Visit every broker so idle and upgraded pipes receive the lifecycle change.
	for name, broker := range c.tunnelBrokers {
		// Expired credentials cannot keep existing reverse streams alive.
		if !c.tunnelLive(c.state.Tunnels[name]) {
			broker.Close()
			delete(c.tunnelBrokers, name)
		}
	}
}

// rememberTunnelGateway invalidates pending approvals when the public authority changes.
func (c *Controller) rememberTunnelGateway() {
	// Changing public authority invalidates outstanding owner approvals.
	if c.state.TunnelGatewayDomain != c.opts.TunnelDomain || c.state.TunnelGatewayURL != c.opts.TunnelURL {
		c.state.TunnelGatewayDomain = c.opts.TunnelDomain
		c.state.TunnelGatewayURL = c.opts.TunnelURL
		c.state.Generation++
	}
}

// configureTunnelAuthority validates the edge origin and excludes existing application namespaces.
func (c *Controller) configureTunnelAuthority() error {
	domain, err := config.NormalizeDomain(c.opts.TunnelDomain)
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err != nil {
		return err
	}
	u, err := url.Parse(c.opts.TunnelURL)
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err != nil || !tunnelOrigin(u) {
		return model.Err("INVALID_CONFIG", "Tunnel URL must be an HTTPS controller origin")
	}
	c.opts.TunnelDomain = domain
	c.opts.TunnelURL = strings.TrimSuffix(c.opts.TunnelURL, "/")
	// Keep management authority outside the wildcard app namespace.
	if c.tunnelHost(u.Hostname()) || c.tunnelHost(c.state.Hostname) {
		return model.Err("INVALID_CONFIG", "Controller hostname must be outside the tunnel domain")
	}
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err := c.validateTunnelNamespace(); err != nil {
		return err
	}

	return nil
}
