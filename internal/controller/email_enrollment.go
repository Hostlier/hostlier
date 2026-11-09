package controller

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hostlier/internal/deviceauth"
	"hostlier/internal/emailauth"
	"hostlier/internal/model"
)

// publicEmailEndpoint exposes only bounded enrollment and public server-session discovery.
func publicEmailEndpoint(r *http.Request) bool {
	// Apply this check before granting authority or advancing protocol state.
	if r.Method == "GET" && r.URL.Path == "/v1/email/session" {
		return true
	}
	return r.Method == "POST" && (r.URL.Path == "/v1/email/begin" || r.URL.Path == "/v1/email/verify")
}

// apiEmail grants a revocable device identity after independent mailbox and device-key verification.
func (c *Controller) apiEmail(r *http.Request, p []string, actor string) (any, error) {
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "GET", 3, "session") {
		return map[string]any{"epoch": c.deviceEpoch, "emailEnrollment": c.opts.EmailEnrollment != nil}, nil
	}
	// Reject the request before side effects: Email enrollment is not configured.
	if c.opts.EmailEnrollment == nil {
		return nil, model.Err("EMAIL_UNAVAILABLE", "Email enrollment is not configured")
	}
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "POST", 3, "begin") {
		var request emailauth.Request
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err := decode(r, &request); err != nil {
			return nil, err
		}
		c.wg.Add(1)
		c.mu.Unlock()
		result, err := c.opts.EmailEnrollment.Begin(r.Context(), request)
		c.mu.Lock()
		c.wg.Done()
		// Reject the request before side effects: Enrollment unavailable; check inputs or try again later.
		if err != nil {
			return nil, model.Err("ENROLLMENT_UNAVAILABLE", "Enrollment unavailable; check inputs or try again later")
		}
		return result, nil
	}
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "POST", 3, "verify") {
		var proof emailauth.Proof
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err := decode(r, &proof); err != nil {
			return nil, err
		}
		request, err := c.opts.EmailEnrollment.Verify(proof)
		// Reject the request before side effects: Invalid or expired enrollment code.
		if err != nil {
			return nil, model.Err("INVALID_ENROLLMENT", "Invalid or expired enrollment code")
		}
		credential := c.credentials(request.Name)
		client := c.state.Clients[credential["clientId"].(string)]
		client.PublicKey = request.PublicKey
		client.EnrollmentEmailHash = c.store.Authenticate("email-owner|" + request.Email)
		credential["deviceBound"] = true
		c.log("Email verified device enrolled: "+client.ID, "", "info")
		return credential, nil
	}
	return endpointNotFound()
}

// deviceProofKey carries only the fields needed by this protocol step.
type deviceProofKey struct{}

// verifyDeviceRequest rejects stolen bearer tokens, modified requests and repeated signed messages.
func (c *Controller) verifyDeviceRequest(r *http.Request, client *Client) error {
	// Legacy profiles retain bearer authentication; only email-enrolled devices require proofs.
	if client.PublicKey == "" {
		return nil
	}
	// Reject the request before side effects: Device owner is no longer authorized.
	if !c.deviceEmailAllowed(client) {
		return model.Err("UNAUTHORIZED", "Device owner is no longer authorized")
	}
	// A live stream rechecks revocation without consuming its opening nonce twice.
	if r.Context().Value(deviceProofKey{}) == client.ID {
		return nil
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := c.validateDeviceEpoch(r); err != nil {
		return err
	}
	nonce := r.Header.Get("X-Hostlier-Nonce")
	// Reject the request before side effects: Invalid device signature.
	if len(nonce) != 64 {
		return model.Err("UNAUTHORIZED", "Invalid device signature")
	}
	body, err := deviceRequestBody(r)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	public, err := emailauth.PublicKey(client.PublicKey)
	// Reject the request before side effects: Device key unavailable.
	if err != nil {
		return model.Err("UNAUTHORIZED", "Device key unavailable")
	}
	signature, _ := base64.RawStdEncoding.DecodeString(r.Header.Get("X-Hostlier-Signature"))
	message := deviceauth.Message(c.deviceEpoch, r.Method, r.URL.RequestURI(), r.Header.Get("X-Hostlier-Time"), nonce, body)
	// Reject the request before side effects: Invalid device signature.
	if !ed25519.Verify(public, message, signature) {
		return model.Err("UNAUTHORIZED", "Invalid device signature")
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := c.consumeDeviceNonce(client.ID + "|" + nonce); err != nil {
		return err
	}
	*r = *r.WithContext(context.WithValue(r.Context(), deviceProofKey{}, client.ID))
	return nil
}

// deviceEmailAllowed applies operator allowlist changes to existing device access without modifying remote policy.
func (c *Controller) deviceEmailAllowed(client *Client) bool {
	// Apply this check before granting authority or advancing protocol state.
	if client.EnrollmentEmailHash == "" {
		return true
	}
	// Fail closed when the required identity, scope or format does not match.
	if c.opts.EmailEnrollment == nil {
		return false
	}
	// Compare only administrator-allowlisted mailbox identities.
	for email := range c.opts.EmailEnrollment.Allowed {
		// Apply this check before granting authority or advancing protocol state.
		if c.store.Authenticate("email-owner|"+email) == client.EnrollmentEmailHash {
			return true
		}
	}
	return false
}

// consumeDeviceNonce bounds replay state for the current process session; restart changes the signed epoch.
func (c *Controller) consumeDeviceNonce(nonce string) error {
	now := c.opts.Now().Unix()
	// Expire retained entries to keep protocol memory bounded.
	for key, expires := range c.deviceNonces {
		// Apply this check before granting authority or advancing protocol state.
		if expires < now {
			delete(c.deviceNonces, key)
		}
	}
	// Reject the request before side effects: Device request already consumed.
	if _, seen := c.deviceNonces[nonce]; seen {
		return model.Err("UNAUTHORIZED", "Device request already consumed")
	}
	// Reject the request before side effects: Device request budget exhausted.
	if len(c.deviceNonces) >= 10000 {
		return model.Err("RATE_LIMITED", "Device request budget exhausted")
	}
	c.deviceNonces[nonce] = now + int64(2*time.Minute/time.Second)
	return nil
}

// deviceRequestBody preserves exactly the bytes verified and later decoded by the handler.
func deviceRequestBody(r *http.Request) ([]byte, error) {
	var err error
	var body []byte
	limit := deviceRequestLimit(r)
	// Apply this check before granting authority or advancing protocol state.
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, limit+1))
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	// Reject the request before side effects: Device request too large.
	if err != nil || int64(len(body)) > limit {
		return nil, model.Err("INVALID_REQUEST", "Device request too large")
	}
	return body, nil
}

// deviceRequestLimit permits negotiated artifact chunks without enlarging other signed requests.
func deviceRequestLimit(r *http.Request) int64 {
	// Only bounded artifact chunk writes need room for a MiB encoded as JSON/base64.
	if artifactChunkRoute(r) {
		return maxRequestBytes
	}
	return 131072
}

// artifactChunkRoute selects the one remote endpoint that needs a larger body.
func artifactChunkRoute(r *http.Request) bool {
	p := strings.Split(r.URL.Path, "/")
	if r.Method != http.MethodPost || len(p) != 5 || p[0] != "" || p[1] != "v1" || p[2] != "artifacts" || p[4] != "chunks" || len(p[3]) != 64 {
		return false
	}
	for _, ch := range p[3] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// validateDeviceEpoch binds each signed request to this process and a narrow clock window.
func (c *Controller) validateDeviceEpoch(r *http.Request) error {
	timestamp, err := strconv.ParseInt(r.Header.Get("X-Hostlier-Time"), 10, 64)
	delta := c.opts.Now().Unix() - timestamp
	// Reject the request before side effects: Refresh the authenticated device session.
	if err != nil || delta < -60 || delta > 60 || r.Header.Get("X-Hostlier-Epoch") != c.deviceEpoch {
		return model.Err("DEVICE_SESSION", "Refresh the authenticated device session")
	}
	return nil
}
