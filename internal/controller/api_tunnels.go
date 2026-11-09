package controller

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/internal/store"
	"hostlier/internal/tunnel"
)

var tunnelName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// apiTunnels uses ordinary owner approval for every credential or publication change.
func (c *Controller) apiTunnels(r *http.Request, p []string, actor string) (any, error) {
	// An unconfigured gateway cannot publish or allocate tunnel hostnames.
	if c.opts.TunnelDomain == "" {
		return nil, model.Err("INVALID_CONFIG", "Tunnel gateway is not configured")
	}
	// Creation is a protected management mutation.
	if matches(r, p, "POST", 2, "") {
		return c.createTunnel(r, actor)
	}
	// Inventory remains scoped to the authenticated owner.
	if matches(r, p, "GET", 2, "") {
		return c.listTunnels(r, actor)
	}
	// Only exact collection and item management routes are supported.
	if len(p) != 3 {
		return endpointNotFound()
	}
	lease, ok := c.state.Tunnels[p[2]]
	// Hide reservations belonging to another management identity.
	if !ok || (lease.Owner != actor && actor != "local") {
		return endpointNotFound()
	}
	// Expose only credential-free status on item reads.
	if r.Method == "GET" {
		return c.viewTunnel(lease), nil
	}
	// Revocation uses the existing owner approval boundary.
	if r.Method == "DELETE" {
		return c.revokeTunnel(lease)
	}
	return endpointNotFound()
}

// validateTunnelRequest limits publication to a bounded hostname, connector credential and lifetime.
func validateTunnelRequest(b tunnelRequest) error {
	// Bind publication to bounded DNS and Bridge identifiers.
	if !tunnelName.MatchString(b.Name) || !scopeName.MatchString(b.BridgeID) {
		return model.Err("INVALID_REQUEST", "Valid tunnel name and bridgeId required")
	}
	// Bound concurrency and credential lifetime before reserving a name.
	if b.Connections < 1 || b.Connections > 16 || b.DurationSeconds < 60 || b.DurationSeconds > 30*24*60*60 {
		return model.Err("INVALID_REQUEST", "Connections must be 1–16 and lifetime 1 minute–30 days")
	}
	hash, err := hex.DecodeString(b.TokenVerifier)
	// Require a full SHA-256 verifier rather than a plaintext token.
	if err != nil || len(hash) != 32 {
		return model.Err("INVALID_REQUEST", "A SHA-256 tunnel credential verifier is required")
	}
	return nil
}

// createTunnel persists reservations before admitting connector traffic; identical retries are safe.
func (c *Controller) createTunnel(r *http.Request, actor string) (any, error) {
	var b tunnelRequest
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err := validateTunnelRequest(b); err != nil {
		return nil, err
	}
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if _, err := config.NormalizeDomain(b.Name + "." + c.opts.TunnelDomain); err != nil {
		return nil, err
	}
	// Existing names retain owner identity across expiry and revocation.
	if old, ok := c.state.Tunnels[b.Name]; ok {
		return c.replaceTunnel(r.Context(), b, old, actor)
	}
	// Bound retained reservations independently of live connection count.
	if len(c.state.Tunnels) >= 1000 {
		return nil, model.Err("RATE_LIMITED", "Tunnel reservation capacity reached")
	}

	return c.installTunnel(r.Context(), b, actor)
}

// installTunnel atomically commits a new lease before publishing its broker.
func (c *Controller) installTunnel(ctx context.Context, b tunnelRequest, actor string) (any, error) {
	broker, err := tunnel.NewBroker(b.Connections)
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err != nil {
		return nil, err
	}
	lease := TunnelLease{Name: b.Name, Owner: actor, BridgeID: b.BridgeID, URL: "https://" + b.Name + "." + c.opts.TunnelDomain, ConnectURL: c.connectTunnelURL(b.Name), Connections: b.Connections, ExpiresAt: c.opts.Now().Add(time.Duration(b.DurationSeconds) * time.Second).UnixMilli(), TokenVerifier: b.TokenVerifier}
	// Do not issue a public URL until the owned edge serves it with a trusted certificate.
	if err := c.ensureTunnelIngress(ctx, lease.URL); err != nil {
		broker.Close()
		return nil, err
	}
	old, existed := c.state.Tunnels[b.Name]
	c.state.Tunnels[b.Name] = lease
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err := c.store.Save(c.state); err != nil {
		delete(c.state.Tunnels, b.Name)
		// Restore the prior record if durable issuance fails.
		if existed {
			c.state.Tunnels[b.Name] = old
		}
		broker.Close()
		return nil, err
	}
	c.tunnelBrokers[b.Name] = broker
	c.state.Generation++
	return c.viewTunnel(lease), nil
}

// revokeTunnel commits revocation before tearing down existing upgraded requests.
func (c *Controller) revokeTunnel(lease TunnelLease) (any, error) {
	old := lease
	lease.Revoked = true
	c.state.Tunnels[lease.Name] = lease
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err := c.store.Save(c.state); err != nil {
		c.state.Tunnels[lease.Name] = old
		return nil, err
	}
	// Close all active pipes before acknowledging durable revocation.
	if broker := c.tunnelBrokers[lease.Name]; broker != nil {
		broker.Close()
		delete(c.tunnelBrokers, lease.Name)
	}
	c.state.Generation++
	return c.viewTunnel(lease), nil
}

// listTunnels exposes only reservations owned by the caller, or all to local administration.
func (c *Controller) listTunnels(r *http.Request, actor string) ([]tunnelView, error) {
	out := []tunnelView{}
	query := store.CollectionQuery{}
	if actor != "local" {
		query.Owner = actor
	}
	leases, err := collectionValues[TunnelLease](r.Context(), c.store, "tunnels", query)
	if err != nil {
		return nil, err
	}
	for _, lease := range leases {
		out = append(out, c.viewTunnel(lease))
	}
	return out, nil
}

// viewTunnel reports live transport state without exposing the credential verifier.
func (c *Controller) viewTunnel(lease TunnelLease) tunnelView {
	status := "offline"
	// Report transport readiness only when at least one reverse pipe is live.
	if broker := c.tunnelBrokers[lease.Name]; broker != nil && broker.Connected() > 0 {
		status = "connected"
	}
	// Expiry takes precedence over a recently connected transport.
	if !c.tunnelLive(lease) {
		status = "expired"
	}
	// Revoked reservations retain ownership without retaining transport.
	if lease.Revoked {
		status = "revoked"
	}
	return tunnelView{lease.Name, lease.BridgeID, lease.URL, lease.ConnectURL, lease.Connections, lease.ExpiresAt, status}
}

// replaceTunnel preserves name ownership and makes exact issuance retries idempotent.
func (c *Controller) replaceTunnel(ctx context.Context, b tunnelRequest, old TunnelLease, actor string) (any, error) {
	// Only the owner or protected local administration can replace a reservation.
	if old.Owner != actor && actor != "local" {
		return endpointNotFound()
	}
	// A live reservation cannot be replaced by a second credential.
	if !old.Revoked {
		// Recover an identical issuance retry without extending the lease.
		if sameTunnelRequest(b, old, actor) {
			if err := c.ensureTunnelIngress(ctx, old.URL); err != nil {
				return nil, err
			}
			return c.viewTunnel(old), nil
		}
		return nil, model.Err("ROUTE_CONFLICT", "Tunnel name is reserved; revoke it before replacing")
	}
	return c.installTunnel(ctx, b, actor)
}

// sameTunnelRequest recognizes a retry without extending expiry or changing the connector identity.
func sameTunnelRequest(b tunnelRequest, old TunnelLease, actor string) bool {
	return old.Owner == actor && old.TokenVerifier == b.TokenVerifier && old.BridgeID == b.BridgeID && old.Connections == b.Connections
}

// addTunnelReview shows the exact public origin for the approved connector identity.
func (c *Controller) addTunnelReview(request humanRequest, review map[string]any) {
	// Only publication proposals need the derived public URL in their review.
	if request.Method != "POST" || request.Path != "/v1/tunnels" {
		return
	}
	var input tunnelRequest
	// Bind publication to bounded DNS and Bridge identifiers.
	if json.Unmarshal(request.Body, &input) != nil || !tunnelName.MatchString(input.Name) {
		return
	}
	review["publicUrl"] = "https://" + input.Name + "." + c.opts.TunnelDomain
	review["gatewayUrl"] = c.opts.TunnelURL
	review["warning"] = "Publishes traffic forwarded by this connector to the internet until lease expiry or revocation. The connector owns its local forwarding configuration. The Bridge label identifies this credential; it does not verify Engine pairing."
}
