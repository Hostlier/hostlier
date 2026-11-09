package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// FleetFingerprint returns the public transport identity for out-of-band host enrollment.
func (c *Controller) FleetFingerprint() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fleet.Fingerprint(c.state.FleetIdentity.Certificate)
}

// prepareAgentListener creates the mutually authenticated TLS configuration while holding the state lock.
func (c *Controller) prepareAgentListener(o *ListenOptions) error {
	// Ordinary controllers keep their configured listener; host agents require a peer pin.
	if o.ControllerFingerprint == "" {
		return nil
	}
	identity, err := c.fleetIdentity()
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	o.TLSConfig, err = fleet.TLS(identity, o.ControllerFingerprint, true)
	return err
}

// remoteListenerHandler makes privileged delegation reachable only on a pinned host-agent listener.
func (c *Controller) remoteListenerHandler(o ListenOptions) http.Handler {
	// Apply this check before granting authority or advancing protocol state.
	if o.ControllerFingerprint == "" {
		return c.Handler()
	}
	// Keep this callback bounded to the current request and its captured state.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.serveHostAgent(w, r, o.ControllerFingerprint) })
}

// serveHostAgent rejects bearer-only access and removes trust-administration routes from delegation.
func (c *Controller) serveHostAgent(w http.ResponseWriter, r *http.Request, pin string) {
	// Apply this check before granting authority or advancing protocol state.
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || !model.FleetRoute(r.URL.Path) {
		http.Error(w, "Host delegation denied", http.StatusForbidden)
		return
	}
	digest := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	actor := r.Header.Get("X-Hostlier-Actor")
	// Apply this check before granting authority or advancing protocol state.
	if hex.EncodeToString(digest[:]) != pin || !fleet.ValidPin(actor) {
		http.Error(w, "Host delegation denied", http.StatusForbidden)
		return
	}
	authority := hostAuthority{Actor: "controller:" + actor}
	authority.Peer, _ = r.Context().Value(masterPeerKey{}).(*masterPeer)
	// Apply this check before granting authority or advancing protocol state.
	if model.RequiresHumanApproval(r.Method, r.URL.Path) {
		generation, err := strconv.ParseInt(r.Header.Get("X-Hostlier-Generation"), 10, 64)
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err != nil || generation < 0 {
			http.Error(w, "Host generation required", http.StatusForbidden)
			return
		}
		authority.Generation = &generation
	}
	ctx := context.WithValue(r.Context(), fleetContext{}, authority)
	c.handle(w, r.WithContext(ctx), true)
}

// checkHostAuthority invalidates remote approval when a host changes after review.
func (c *Controller) checkHostAuthority(r *http.Request) error {
	authority, ok := r.Context().Value(fleetContext{}).(hostAuthority)
	// Reject the request before side effects: Host state changed after review; request a new approval.
	if ok && authority.Peer != nil && authority.Peer != c.master.peer {
		return model.Err("HOST_REVOKED", "Master connection has changed")
	}
	if ok && authority.Generation != nil && *authority.Generation != c.state.Generation {
		return model.Err("STALE_PLAN", "Host state changed after review; request a new approval")
	}
	return nil
}
