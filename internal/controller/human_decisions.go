package controller

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"hostlier/internal/model"
	"hostlier/internal/securitykey"
)

// findHumanRequest accepts the full opaque ID or the independent human-friendly locator.
func (c *Controller) findHumanRequest(id string) *HumanApproval {
	// Apply this check before granting authority or advancing protocol state.
	if a := c.state.HumanApprovals[id]; a != nil {
		return a
	}
	// Validate each entry before accepting the complete collection.
	for _, a := range c.state.HumanApprovals {
		// Apply this check before granting authority or advancing protocol state.
		if a.Code != "" && strings.EqualFold(a.Code, id) {
			return a
		}
	}
	return nil
}

// reviewHumanRequest reveals sanitized intent while requiring a separate signature to grant authority.
func (c *Controller) reviewHumanRequest(id string) (any, error) {
	a := c.findHumanRequest(id)
	// Apply this check before granting authority or advancing protocol state.
	if a == nil || a.ExpiresAt <= c.opts.Now().UnixMilli() {
		return endpointNotFound()
	}
	return c.humanApprovalView(a), nil
}

// beginHumanDecision binds an exact decision to the enrolled public key and a one-time nonce.
func (c *Controller) beginHumanDecision(r *http.Request) (any, error) {
	var body struct {
		ID             string `json:"id"`
		Fingerprint    string `json:"fingerprint"`
		Decision       string `json:"decision"`
		KeyFingerprint string `json:"keyFingerprint"`
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	a := c.findHumanRequest(body.ID)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := c.checkHumanDecision(a, body.Fingerprint, body.Decision); err != nil {
		return nil, err
	}
	key, ok := c.state.Human.Keys[body.KeyFingerprint]
	// Reject the request before side effects: Approver key is not enrolled.
	if !ok || key.Revoked {
		return nil, model.Err("HUMAN_VERIFICATION", "Approver key is not enrolled")
	}
	id := random()
	expires := c.opts.Now().Add(2 * time.Minute)
	message, _ := json.Marshal(map[string]any{"protocol": securitykey.Namespace, "controller": c.state.HumanRealm, "controllerHost": c.state.Hostname, "requestId": a.ID, "fingerprint": a.Fingerprint, "decision": body.Decision, "keyFingerprint": body.KeyFingerprint, "generation": a.Generation, "nonce": id, "expiresAt": expires.UnixMilli()})
	session := humanCeremony{ApprovalID: a.ID, Fingerprint: a.Fingerprint, Decision: body.Decision, KeyFingerprint: body.KeyFingerprint, Message: string(message), ExpiresAt: expires}
	return c.retainHumanCeremony(id, session)
}

// checkHumanDecision rechecks exact intent and state before and after signature verification.
func (c *Controller) checkHumanDecision(a *HumanApproval, fingerprint, decision string) error {
	// Reject the request before side effects: Approval request unavailable or changed.
	if a == nil || !c.humanEnrolled() || a.Status != "pending" || a.Fingerprint != fingerprint {
		return model.Err("APPROVAL_INVALID", "Approval request unavailable or changed")
	}
	// Reject the request before side effects: Request expired or controller changed.
	if a.ExpiresAt <= c.opts.Now().UnixMilli() || a.Generation != c.state.Generation {
		return model.Err("STALE_PLAN", "Request expired or controller changed")
	}
	// Reject the request before side effects: Choose approve or reject.
	if decision != "approve" && decision != "reject" {
		return model.Err("INVALID_REQUEST", "Choose approve or reject")
	}
	return nil
}

// retainHumanCeremony bounds unauthenticated challenge allocation and invalidates expired challenges.
func (c *Controller) retainHumanCeremony(id string, session humanCeremony) (any, error) {
	// Validate each entry before accepting the complete collection.
	for key, existing := range c.human.Ceremonies {
		// Apply this check before granting authority or advancing protocol state.
		if !c.opts.Now().Before(existing.ExpiresAt) {
			delete(c.human.Ceremonies, key)
		}
	}
	// Reject the request before side effects: Too many active signing challenges.
	if len(c.human.Ceremonies) >= 100 {
		return nil, model.Err("RATE_LIMITED", "Too many active signing challenges")
	}
	c.human.Ceremonies[id] = session
	return map[string]any{"ceremony": id, "message": session.Message, "expiresAt": session.ExpiresAt.UnixMilli()}, nil
}

// finishHumanDecision consumes every attempted challenge and requires signed user-verification and presence flags.
func (c *Controller) finishHumanDecision(r *http.Request) (any, error) {
	var body struct {
		Ceremony  string `json:"ceremony"`
		Signature string `json:"signature"`
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	session, ok := c.human.Ceremonies[body.Ceremony]
	delete(c.human.Ceremonies, body.Ceremony)
	// Reject the request before side effects: Signing challenge expired or already consumed.
	if !ok || !c.opts.Now().Before(session.ExpiresAt) {
		return nil, model.Err("HUMAN_CEREMONY", "Signing challenge expired or already consumed")
	}
	a := c.state.HumanApprovals[session.ApprovalID]
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := c.checkHumanDecision(a, session.Fingerprint, session.Decision); err != nil {
		return nil, err
	}
	key := c.state.Human.Keys[session.KeyFingerprint]
	counter, err := securitykey.Verify(key, []byte(session.Message), body.Signature)
	// Reject the request before side effects: Hardware signature, user verification or touch check failed.
	if err != nil {
		return nil, model.Err("HUMAN_VERIFICATION", "Hardware signature, user verification or touch check failed")
	}
	key.Counter = counter
	c.state.Human.Keys[session.KeyFingerprint] = key
	a.Status = "rejected"
	// Apply this check before granting authority or advancing protocol state.
	if session.Decision == "approve" {
		a.Status = "approved"
	}
	a.Approver = c.state.Human.ID
	a.CredentialID = session.KeyFingerprint
	a.ApprovedAt = c.opts.Now().UnixMilli()
	c.log("Hardware-key decision "+a.Status+": "+a.ID, "", "info")
	return c.humanApprovalView(a), nil
}
