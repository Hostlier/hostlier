package controller

import (
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"hostlier/internal/model"
	"hostlier/internal/securitykey"
)

// initializeHuman starts fresh terminal challenges without granting existing API credentials approval authority.
func (c *Controller) initializeHuman() error {
	// Apply this check before granting authority or advancing protocol state.
	if c.state.Authorizations == nil {
		c.state.Authorizations = map[string]*Authorization{}
	}
	c.access = accessState{Ceremonies: map[string]browserCeremony{}, Bootstraps: map[string]time.Time{}, Pairings: map[string]*cliPairing{}}
	c.human = humanSecurity{Ceremonies: map[string]humanCeremony{}}
	// Apply this check before granting authority or advancing protocol state.
	if c.state.HumanApprovals == nil {
		c.state.HumanApprovals = map[string]*HumanApproval{}
	}
	// Initialize a persistent controller identity for domain-separated signing challenges.
	if c.state.HumanRealm == "" {
		c.state.HumanRealm = random()
	}
	return nil
}

// publicHumanEndpoint permits only bounded review and hardware-signature protocols without API credentials.
func publicHumanEndpoint(r *http.Request) bool {
	// Apply this check before granting authority or advancing protocol state.
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/human/review/") {
		return true
	}
	return r.Method == "POST" && (r.URL.Path == "/v1/human/decision/begin" || r.URL.Path == "/v1/human/decision/finish")
}

// apiHuman keeps key enrollment separate from remote API authority and exposes terminal signing ceremonies.
func (c *Controller) apiHuman(r *http.Request, p []string, actor string) (any, error) {
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "POST", 3, "enroll") {
		return c.enrollHumanKey(r, actor)
	}
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "POST", 3, "revoke") {
		return c.revokeHumanKey(r, actor)
	}
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "GET", 3, "status") {
		return map[string]any{"humanApprovalRequired": true, "enrolled": c.humanEnrolled(), "methods": []string{"browser-passkey", "openssh-fido"}, "method": "browser-passkey-or-openssh-fido", "hardwareKeyRequired": false, "scopedCredentialsSupported": true}, nil
	}
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "GET", 4, "") && p[2] == "review" {
		return c.reviewHumanRequest(p[3])
	}
	// Apply this check before granting authority or advancing protocol state.
	if r.Method != "POST" {
		return endpointNotFound()
	}
	handlers := map[string]func(*http.Request) (any, error){"/v1/human/decision/begin": c.beginHumanDecision, "/v1/human/decision/finish": c.finishHumanDecision}
	// Apply this check before granting authority or advancing protocol state.
	if handler := handlers[r.URL.Path]; handler != nil {
		return handler(r)
	}
	return endpointNotFound()
}

// humanEnrolled requires at least one live independently enrolled passkey or hardware key.
func (c *Controller) humanEnrolled() bool {
	// Fail closed when the required identity, scope or format does not match.
	if c.state.Human == nil {
		return false
	}
	// Browser credentials satisfy owner enrollment without a physical hardware key.
	if len(c.state.Human.WebAuthnCredentials()) > 0 {
		return true
	}
	// Only enrolled, nonrevoked keys can confer human authority.
	for _, key := range c.state.Human.Keys {
		// Apply this check before granting authority or advancing protocol state.
		if !key.Revoked {
			return true
		}
	}
	return false
}

// enrollHumanKey trusts public keys only from the explicit root-owned administrative channel.
func (c *Controller) enrollHumanKey(r *http.Request, actor string) (any, error) {
	// Reject the request before side effects: Hardware-key enrollment requires protected local administration.
	if actor != "local" {
		return nil, model.Err("HUMAN_VERIFICATION", "Hardware-key enrollment requires protected local administration")
	}
	var body struct {
		Name      string `json:"name"`
		PublicKey string `json:"publicKey"`
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	// Reject the request before side effects: A bounded approver name is required.
	if required(body.Name) != nil || len(body.Name) > 100 {
		return nil, model.Err("INVALID_REQUEST", "A bounded approver name is required")
	}
	key, err := securitykey.Parse(body.PublicKey)
	// Reject the request before side effects: Provide one bare FIDO security-key public key.
	if err != nil {
		return nil, model.Err("INVALID_REQUEST", "Provide one bare FIDO security-key public key")
	}
	// Apply this check before granting authority or advancing protocol state.
	if c.state.Human == nil {
		c.state.Human = &HumanIdentity{ID: random(), Name: body.Name}
	}
	// Apply this check before granting authority or advancing protocol state.
	if c.state.Human.Keys == nil {
		c.state.Human.Keys = map[string]securitykey.Credential{}
	}
	// Reject the request before side effects: At most ten approver keys may be retained.
	if len(c.state.Human.Keys) >= 10 {
		return nil, model.Err("KEY_LIMIT", "At most ten approver keys may be retained")
	}
	pin := ssh.FingerprintSHA256(key)
	// Reject the request before side effects: Key already enrolled; revoked keys cannot be reactivated.
	if _, exists := c.state.Human.Keys[pin]; exists {
		return nil, model.Err("KEY_EXISTS", "Key already enrolled; revoked keys cannot be reactivated")
	}
	c.state.Human.Keys[pin] = securitykey.Credential{PublicKey: string(ssh.MarshalAuthorizedKey(key)), Fingerprint: pin}
	c.state.Generation++
	c.log("Human hardware key enrolled: "+pin, "", "info")
	return map[string]any{"enrolled": true, "fingerprint": pin}, nil
}

// revokeHumanKey invalidates pending approvals and active authorizations from a lost hardware key.
func (c *Controller) revokeHumanKey(r *http.Request, actor string) (any, error) {
	// Reject the request before side effects: Key recovery requires protected local administration.
	if actor != "local" {
		return nil, model.Err("HUMAN_VERIFICATION", "Key recovery requires protected local administration")
	}
	var body struct {
		Fingerprint string `json:"fingerprint"`
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	// Apply this check before granting authority or advancing protocol state.
	if c.state.Human == nil {
		return endpointNotFound()
	}
	// Passkey revocation invalidates approvals and grants just like hardware-key revocation.
	if key, ok := c.state.Human.Passkeys[body.Fingerprint]; ok {
		key.Revoked = true
		c.state.Human.Passkeys[body.Fingerprint] = key
		c.state.Generation++
		c.log("Browser passkey revoked: "+body.Fingerprint, "", "warn")
		return map[string]bool{"revoked": true}, nil
	}
	key, ok := c.state.Human.Keys[body.Fingerprint]
	// Apply this check before granting authority or advancing protocol state.
	if !ok {
		return endpointNotFound()
	}
	key.Revoked = true
	c.state.Human.Keys[body.Fingerprint] = key
	c.state.Generation++
	c.log("Human hardware key revoked: "+body.Fingerprint, "", "warn")
	return map[string]bool{"revoked": true}, nil
}
