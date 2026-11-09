package controller

import (
	"crypto/subtle"
	"net/http"
	"time"

	"hostlier/internal/engineoauth"
	"hostlier/internal/model"
)

// serveFusedCallback redeems only an owner-initiated state and never exposes tokens to the browser.
func (c *Controller) serveFusedCallback(w http.ResponseWriter, r *http.Request) bool {
	// Reject invalid protocol state before advancing the connection.
	if r.URL.Path != "/fused/oauth/callback" {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Reject methods or paths outside this exact protocol route.
	if r.Method != "GET" || r.Host != c.state.Hostname || len(r.URL.RawQuery) > 8192 {
		http.Error(w, "Invalid OAuth callback", 400)
		return true
	}
	name, private, err := c.consumeFusedAttempt(r.URL.Query().Get("state"))
	// Propagate failure before publishing any connection state.
	if err != nil {
		http.Error(w, "Invalid or expired authorization state", 400)
		return true
	}
	// Denial consumes the attempt so a later callback cannot revive it.
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		http.Error(w, "Authorization declined. Reconnect from Hostlier.", 400)
		return true
	}
	form := fusedTokenForm(private)
	form.Set("grant_type", "authorization_code")
	form.Set("code", r.URL.Query().Get("code"))
	form.Set("redirect_uri", private.RedirectURI)
	form.Set("code_verifier", private.Verifier)
	// Token exchange runs outside the controller lock; the consumed state prevents concurrent replay.
	tokens, err := engineoauth.Exchange(r.Context(), private.Metadata.TokenEndpoint, form)
	// Propagate failure before publishing any connection state.
	if err != nil {
		http.Error(w, "Engine authorization failed. Reconnect from Hostlier.", 400)
		return true
	}
	private.Tokens = tokens
	// Propagate failure before publishing any connection state.
	if err = c.completeFusedAttempt(name, private); err != nil {
		http.Error(w, "Could not save connection. Reconnect from Hostlier.", 500)
		return true
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("Connected to Fused. Return to Hostlier to deploy your Minion app.\n"))
	return true
}

// consumeFusedAttempt durably invalidates state before sending the code to Engine.
func (c *Controller) consumeFusedAttempt(state string) (string, fusedOAuthPrivate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.consumeFusedAttemptLocked(state, false)
}

// consumeFusedAttemptLocked invalidates one pending ceremony under the caller-owned state lock.
func (c *Controller) consumeFusedAttemptLocked(state string, relay bool) (string, fusedOAuthPrivate, error) {
	// An empty value can never match an initialized attempt.
	if state != "" {
		hash := verifier(state)
		// Search only retained owner-initiated attempts.
		for name, v := range c.state.FusedConnections {
			// Only the expected live connection state may advance.
			if v.Status != "pending" || !v.ExpiresAt.After(c.opts.Now()) {
				continue
			}
			_, private, err := c.loadFusedPrivate(name)
			// Propagate failure before publishing any connection state.
			if err != nil {
				return "", private, err
			}
			// Dynamic clients must finish through their authenticated initiating CLI.
			if private.Actor != "" && !relay {
				continue
			}
			// Only the original unpredictable state may consume this ceremony.
			if subtle.ConstantTimeCompare([]byte(private.StateHash), []byte(hash)) != 1 {
				continue
			}
			v.Status = "exchanging"
			// Propagate failure before publishing any connection state.
			if err = c.store.Save(c.state); err != nil {
				return "", private, err
			}
			return name, private, nil
		}
	}
	return "", fusedOAuthPrivate{}, model.Err("FUSED_OAUTH", "Invalid authorization state")
}

// completeFusedAttempt cannot resurrect a connection cancelled while exchange was in flight.
func (c *Controller) completeFusedAttempt(name string, private fusedOAuthPrivate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.completeFusedAttemptLocked(name, private)
}

// completeFusedAttemptLocked commits credentials under the caller-owned state lock.
func (c *Controller) completeFusedAttemptLocked(name string, private fusedOAuthPrivate) error {
	v, prior, err := c.loadFusedPrivate(name)
	// Propagate failure before publishing any connection state.
	if err != nil {
		return err
	}
	// Only the expected live connection state may advance.
	if v.Status != "exchanging" || prior.Verifier == "" || subtle.ConstantTimeCompare([]byte(prior.Verifier), []byte(private.Verifier)) != 1 {
		return model.Err("FUSED_OAUTH", "Connection was cancelled")
	}
	private.Verifier, private.StateHash = "", ""
	// Propagate failure before publishing any connection state.
	if err = c.saveFusedPrivate(v, private); err != nil {
		return err
	}
	v.Status = "connected"
	v.ExpiresAt = c.opts.Now().Add(time.Duration(private.Tokens.ExpiresIn) * time.Second)
	return c.store.Save(c.state)
}
