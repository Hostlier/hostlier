package controller

import (
	"crypto/ed25519"
	"encoding/base64"
	"hostlier/internal/emailauth"
	"hostlier/internal/model"
	"net/http"
	"strings"
	"time"
)

// deploymentScope names every capability conferred by a paired CLI credential.
type deploymentScope struct {
	Apps            []string `json:"apps"`
	Secrets         []string `json:"secrets"`
	CreateSecrets   []string `json:"createSecrets,omitempty"`
	DurationSeconds int64    `json:"durationSeconds"`
	MaxUses         int      `json:"maxUses"`
}

// cliPairing retains a short-lived device-bound request and only a verifier of its one-time code.
type cliPairing struct {
	ID, Name, PublicKey, SecretHash, Challenge, CodeHash, Issuer string
	Scope                                                        deploymentScope
	Attempts                                                     int
	ExpiresAt                                                    time.Time
}

// scopedPairingRequest captures the scope an owner reviews before generating a code.
type scopedPairingRequest struct {
	ID    string          `json:"id"`
	Scope deploymentScope `json:"scope"`
}

// startCLIPairing issues a browser review URL and a private terminal-only challenge.
func (c *Controller) startCLIPairing(r *http.Request) (any, error) {
	var b struct {
		Name      string `json:"name"`
		PublicKey string `json:"publicKey"`
	}
	// Reject malformed or unknown request fields before changing state.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	// Reject the request: A bounded device name is required.
	if required(b.Name) != nil || len(b.Name) > 100 {
		return nil, model.Err("INVALID_REQUEST", "A bounded device name is required")
	}
	// Stop when validation or proof verification fails.
	if _, err := emailauth.PublicKey(b.PublicKey); err != nil {
		return nil, model.Err("INVALID_REQUEST", "A device signing public key is required")
	}
	c.pruneAccess()
	// Reject the request: Too many pending CLI connections.
	if len(c.access.Pairings) >= 100 {
		return nil, model.Err("RATE_LIMITED", "Too many pending CLI connections")
	}
	id, secret := random(), random()
	p := &cliPairing{ID: id, Name: b.Name, PublicKey: b.PublicKey, SecretHash: verifier(secret), Challenge: random(), ExpiresAt: c.opts.Now().Add(10 * time.Minute)}
	c.access.Pairings[id] = p
	return map[string]any{"id": id, "requestSecret": secret, "pollSupported": true, "ownerSetupRequired": !c.humanEnrolled(), "message": "hostlier-cli-pair-v1|" + c.state.HumanRealm + "|" + id + "|" + p.Challenge, "url": "https://" + c.state.Hostname + "/connect?request=" + id, "expiresAt": p.ExpiresAt.UnixMilli()}, nil
}

// pairReview reveals device identity and fingerprint without exposing a redemption code or secret.
func (c *Controller) pairReview(id string) (any, error) {
	p, err := c.livePairing(id)
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": p.ID, "name": p.Name, "deviceFingerprint": verifier(p.PublicKey), "expiresAt": p.ExpiresAt.UnixMilli(), "approved": p.CodeHash != ""}, nil
}

// livePairing rejects missing and expired device requests uniformly.
func (c *Controller) livePairing(id string) (*cliPairing, error) {
	p := c.access.Pairings[id]
	// Reject the request: CLI connection request is unavailable.
	if p == nil || !c.opts.Now().Before(p.ExpiresAt) {
		return nil, model.Err("INVALID_ENROLLMENT", "CLI connection request is unavailable")
	}
	return p, nil
}

// validateDeploymentScope accepts empty all-access selection or explicitly bounded deployment authority.
func (c *Controller) validateDeploymentScope(s deploymentScope) error {
	// Empty selections grant all access only after the ordinary owner approval.
	if s.allAccess() {
		return nil
	}
	// Reject the request: Specify 1–20 apps, at most 20 secrets, 1–100 deployments and 1 minute–8 hours.
	if !validAuthorizationLimits(authorizationRequest{Scope: "deploy", Apps: s.Apps, Secrets: s.Secrets, DurationSeconds: s.DurationSeconds, MaxUses: s.MaxUses}) {
		return model.Err("INVALID_AUTHORIZATION", "Specify 1–20 apps, at most 20 secrets, 1–100 deployments and 1 minute–8 hours")
	}
	// Check every retained entry before accepting the complete set.
	for _, name := range append(append([]string{}, s.Apps...), s.Secrets...) {
		// Reject the request: Scope requires exact application and secret names.
		if !scopeName.MatchString(name) {
			return model.Err("INVALID_AUTHORIZATION", "Scope requires exact application and secret names")
		}
	}
	// Creation is a separate, explicit capability limited to referenced secret names.
	if err := validateSecretCreationScope(s); err != nil {
		return err
	}
	// Existing secrets need no creation permission.
	for _, name := range s.Secrets {
		// Reject the request: Upload each named secret before authorizing its use.
		if c.state.Secrets[name] == nil && !containsName(s.CreateSecrets, name) {
			return model.Err("SECRET_MISSING", "Secret "+name+" does not exist; explicitly allow this CLI to create it in the approval form")
		}
	}
	return nil
}

// adminPairCode is the explicit server-operator bootstrap alternative to browser approval.
func (c *Controller) adminPairCode(r *http.Request) (any, error) {
	var b scopedPairingRequest
	// Reject malformed or unknown request fields before changing state.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	return c.issuePairCode(b.ID, b.Scope, "administrator:"+c.state.HumanRealm)
}

// issuePairCode shows a high-entropy one-time code only after authority and exact scope are established.
func (c *Controller) issuePairCode(id string, s deploymentScope, issuer string) (any, error) {
	p, err := c.livePairing(id)
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	// Reject the request: A code was already issued; start a new connection if it was lost.
	if p.CodeHash != "" {
		return nil, model.Err("INVALID_ENROLLMENT", "A code was already issued; start a new connection if it was lost")
	}
	// Stop when validation or proof verification fails.
	if err = c.validateDeploymentScope(s); err != nil {
		return nil, err
	}
	// Reject the request: Approver authority is no longer available.
	if !c.liveApprovalKey(issuer) {
		return nil, model.Err("UNAUTHORIZED", "Approver authority is no longer available")
	}
	code := strings.ToUpper(random()[:16])
	p.CodeHash = verifier(code)
	p.Scope = s
	p.Issuer = issuer
	c.log("CLI connection approved ("+s.accessMode()+"): "+p.ID, "", "info")
	return map[string]any{"code": code[:4] + "-" + code[4:8] + "-" + code[8:12] + "-" + code[12:], "expiresAt": p.ExpiresAt.UnixMilli(), "scope": s, "access": s.accessMode(), "name": p.Name}, nil
}

// redeemCLIPairing requires the terminal secret, owner code and proof of the original device private key.
func (c *Controller) redeemCLIPairing(r *http.Request) (any, error) {
	var b struct {
		ID        string `json:"id"`
		Secret    string `json:"requestSecret"`
		Code      string `json:"code"`
		Signature string `json:"signature"`
	}
	// Reject malformed or unknown request fields before changing state.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	p, err := c.livePairing(b.ID)
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	// Reject the request: CLI connection proof is invalid.
	if verifier(b.Secret) != p.SecretHash {
		return nil, model.Err("INVALID_ENROLLMENT", "CLI connection proof is invalid")
	}
	p.Attempts++
	// Discard expired or consumed ephemeral authority.
	if p.Attempts > 5 {
		delete(c.access.Pairings, p.ID)
		return nil, model.Err("INVALID_ENROLLMENT", "Too many invalid code attempts")
	}
	code := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(b.Code), "-", ""))
	key, _ := emailauth.PublicKey(p.PublicKey)
	signature, _ := base64.RawStdEncoding.DecodeString(b.Signature)
	message := "hostlier-cli-pair-v1|" + c.state.HumanRealm + "|" + p.ID + "|" + p.Challenge
	// Reject the request: CLI connection proof is invalid.
	if p.CodeHash == "" || verifier(code) != p.CodeHash || !ed25519.Verify(key, []byte(message), signature) {
		return nil, model.Err("INVALID_ENROLLMENT", "CLI connection proof is invalid")
	}
	return c.completeCLIPairing(p)
}

// completeCLIPairing atomically creates the approved device credential and consumes its pairing request.
func (c *Controller) completeCLIPairing(p *cliPairing) (any, error) {
	// Reject the request: Pairing approver was revoked.
	if !c.liveApprovalKey(p.Issuer) {
		return nil, model.Err("UNAUTHORIZED", "Pairing approver was revoked")
	}
	// Stop when validation or proof verification fails.
	if err := c.validateDeploymentScope(p.Scope); err != nil {
		return nil, err
	}
	// Reject the request: Too many retained grants.
	if len(c.state.Authorizations) >= 1000 {
		return nil, model.Err("AUTHORIZATION_LIMIT", "Too many retained grants")
	}
	id, token, grantID := random(), random(), random()
	c.state.Clients[id] = &Client{ID: id, Name: p.Name, Verifier: verifier(token), PublicKey: p.PublicKey, DeploymentGrant: grantID}
	g := &Authorization{ID: grantID, Device: id, Host: "local", Apps: p.Scope.Apps, Secrets: p.Scope.Secrets, CreateSecrets: p.Scope.CreateSecrets, Scope: p.Scope.accessMode(), ExpiresAt: c.authorizationExpiry(p.Scope.accessMode(), p.Scope.DurationSeconds), MaxUses: p.Scope.MaxUses, KeyFingerprint: p.Issuer}
	c.state.Authorizations[grantID] = g
	delete(c.access.Pairings, p.ID)
	c.state.Generation++
	c.log("CLI credential issued ("+g.Scope+"): "+id, "", "info")
	return map[string]any{"clientId": id, "token": token, "authorizationId": grantID, "expiresAt": g.ExpiresAt, "scope": p.Scope, "access": g.Scope}, nil
}
