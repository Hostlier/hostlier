package controller

import (
	"encoding/base64"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"hostlier/internal/model"
	"net/http"
	"net/url"
	"time"
)

// passkeyVerifier fixes RP ID and origin from trusted controller configuration, never request headers.
func (c *Controller) passkeyVerifier() (*webauthn.WebAuthn, error) {
	origin := "https://" + c.state.Hostname
	u, err := url.Parse(origin)
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	return webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "Hostlier", RPOrigins: []string{origin}, AuthenticatorSelection: protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired}, AttestationPreference: protocol.PreferNoAttestation})
}

// passkeyBootstrap creates a short-lived enrollment link only through protected server administration.
func (c *Controller) passkeyBootstrap(r *http.Request, actor string) (any, error) {
	// Reject the request: Passkey bootstrap requires protected local administration.
	if actor != "local" {
		return nil, model.Err("UNAUTHORIZED", "Passkey bootstrap requires protected local administration")
	}
	var b struct {
		Name string `json:"name"`
	}
	// Reject malformed or unknown request fields before changing state.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	link, expires, err := c.ownerEnrollmentLink(b.Name)
	// Preserve the existing protected administrator enrollment response.
	if err != nil {
		return nil, err
	}
	return map[string]any{"url": link, "expiresAt": expires}, nil
}

// ownerEnrollmentLink issues a bounded single-use link without requiring a second server binary.
func (c *Controller) ownerEnrollmentLink(name string) (string, int64, error) {
	// Reject the request: A bounded owner name is required.
	if required(name) != nil || len(name) > 100 {
		return "", 0, model.Err("INVALID_REQUEST", "A bounded owner name is required")
	}
	c.pruneAccess()
	// Reject the request: Too many active enrollment links.
	if len(c.access.Bootstraps) >= 10 {
		return "", 0, model.Err("RATE_LIMITED", "Too many active enrollment links")
	}
	// Proceed only for this explicitly validated protocol state.
	if c.state.Human == nil {
		c.state.Human = &HumanIdentity{ID: random(), Name: name}
	}
	token := random()
	expires := c.opts.Now().Add(10 * time.Minute)
	c.access.Bootstraps[verifier(token)] = expires
	c.log("Passkey enrollment link created by server administrator", "", "info")
	return "https://" + c.state.Hostname + "/security#enroll=" + token, expires.UnixMilli(), nil
}

// beginPasskeyRegistration requires the administrator bootstrap secret and a same-origin browser.
func (c *Controller) beginPasskeyRegistration(r *http.Request) (any, error) {
	// Require the configured controller origin before consuming any challenge.
	if err := c.browserOrigin(r); err != nil {
		return nil, err
	}
	var b struct {
		Token string `json:"token"`
	}
	// Reject malformed or unknown request fields before changing state.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	hash := verifier(b.Token)
	expires, ok := c.access.Bootstraps[hash]
	// Reject the request: Enrollment link expired or invalid.
	if !ok || !c.opts.Now().Before(expires) || c.state.Human == nil {
		return nil, model.Err("UNAUTHORIZED", "Enrollment link expired or invalid")
	}
	// Reject the request: At most ten passkeys may be retained.
	if len(c.state.Human.Passkeys) >= 10 {
		return nil, model.Err("KEY_LIMIT", "At most ten passkeys may be retained")
	}
	w, err := c.passkeyVerifier()
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	excluded := []protocol.CredentialDescriptor{}
	// Check every retained entry before accepting the complete set.
	for _, key := range c.state.Human.WebAuthnCredentials() {
		excluded = append(excluded, key.Descriptor())
	}
	options, session, err := w.BeginRegistration(c.state.Human, webauthn.WithExclusions(excluded))
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, model.Err("PASSKEY_FAILED", "Could not begin passkey enrollment")
	}
	return c.keepBrowserCeremony(browserCeremony{Session: *session, Kind: "register", Bootstrap: hash}, options)
}

// finishPasskeyRegistration verifies origin, RP ID, challenge, user presence and user verification before enrollment.
func (c *Controller) finishPasskeyRegistration(r *http.Request) (any, error) {
	v, err := c.takeBrowserCeremony(r, "register")
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	expires, ok := c.access.Bootstraps[v.Bootstrap]
	// Reject the request: Enrollment link expired or consumed.
	if !ok || !c.opts.Now().Before(expires) {
		return nil, model.Err("UNAUTHORIZED", "Enrollment link expired or consumed")
	}
	// Reject the request: Owner unavailable or passkey limit reached.
	if c.state.Human == nil || len(c.state.Human.Passkeys) >= 10 {
		return nil, model.Err("KEY_LIMIT", "Owner unavailable or passkey limit reached")
	}
	w, err := c.passkeyVerifier()
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	key, err := w.FinishRegistration(c.state.Human, v.Session, r)
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, model.Err("PASSKEY_FAILED", "Passkey registration could not be verified")
	}
	id := "passkey:" + base64.RawURLEncoding.EncodeToString(key.ID)
	// Proceed only for this explicitly validated protocol state.
	if c.state.Human.Passkeys == nil {
		c.state.Human.Passkeys = map[string]Passkey{}
	}
	// Reject the request: Passkey already enrolled.
	if _, ok := c.state.Human.Passkeys[id]; ok {
		return nil, model.Err("KEY_EXISTS", "Passkey already enrolled")
	}
	c.state.Human.Passkeys[id] = Passkey{Credential: *key}
	delete(c.access.Bootstraps, v.Bootstrap)
	c.state.Generation++
	c.log("Browser passkey enrolled: "+id, "", "info")
	return map[string]any{"enrolled": true, "fingerprint": id}, nil
}

// beginPasskeyProof offers only live enrolled credentials and requires verification for every approval.
func (c *Controller) beginPasskeyProof(v browserCeremony) (any, error) {
	// Reject the request: Enroll a browser passkey first.
	if c.state.Human == nil || len(c.state.Human.WebAuthnCredentials()) == 0 {
		return nil, model.Err("HUMAN_NOT_ENROLLED", "Enroll a browser passkey first")
	}
	w, err := c.passkeyVerifier()
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	options, session, err := w.BeginLogin(c.state.Human, webauthn.WithUserVerification(protocol.VerificationRequired))
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, model.Err("PASSKEY_FAILED", "Could not begin passkey approval")
	}
	v.Session = *session
	return c.keepBrowserCeremony(v, options)
}

// finishPasskeyProof writes back counter and backup flags only after successful verification.
func (c *Controller) finishPasskeyProof(r *http.Request, v browserCeremony) (string, error) {
	// Reject the request: Owner unavailable.
	if c.state.Human == nil {
		return "", model.Err("HUMAN_NOT_ENROLLED", "Owner unavailable")
	}
	w, err := c.passkeyVerifier()
	// Stop when validation or proof verification fails.
	if err != nil {
		return "", err
	}
	key, err := w.FinishLogin(c.state.Human, v.Session, r)
	// Stop when validation or proof verification fails.
	if err != nil || key.Authenticator.CloneWarning {
		return "", model.Err("PASSKEY_FAILED", "Passkey assertion could not be verified")
	}
	id := "passkey:" + base64.RawURLEncoding.EncodeToString(key.ID)
	// Reject the request: Passkey revoked or unavailable.
	if !c.liveApprovalKey(id) {
		return "", model.Err("PASSKEY_FAILED", "Passkey revoked or unavailable")
	}
	c.state.Human.Passkeys[id] = Passkey{Credential: *key}
	return id, nil
}
