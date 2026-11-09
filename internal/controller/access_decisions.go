package controller

import (
	"hostlier/internal/model"
	"net/http"
)

// beginPasskeyDecision binds a browser assertion to the exact retained deployment or administrative request.
func (c *Controller) beginPasskeyDecision(r *http.Request) (any, error) {
	// Require the configured controller origin before consuming any challenge.
	if err := c.browserOrigin(r); err != nil {
		return nil, err
	}
	var b struct {
		ID          string `json:"id"`
		Fingerprint string `json:"fingerprint"`
		Decision    string `json:"decision"`
	}
	// Reject malformed or unknown request fields before changing state.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	a := c.findHumanRequest(b.ID)
	// Stop when validation or proof verification fails.
	if err := c.checkHumanDecision(a, b.Fingerprint, b.Decision); err != nil {
		return nil, err
	}
	return c.beginPasskeyProof(browserCeremony{Kind: "decision", ApprovalID: a.ID, Fingerprint: a.Fingerprint, Decision: b.Decision, Generation: a.Generation})
}

// finishPasskeyDecision consumes the assertion before marking only its original reviewed request approved.
func (c *Controller) finishPasskeyDecision(r *http.Request) (any, error) {
	v, err := c.takeBrowserCeremony(r, "decision")
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	a := c.state.HumanApprovals[v.ApprovalID]
	// Stop when validation or proof verification fails.
	if err = c.checkHumanDecision(a, v.Fingerprint, v.Decision); err != nil {
		return nil, err
	}
	id, err := c.finishPasskeyProof(r, v)
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	a.Status = "rejected"
	// Proceed only for this explicitly validated protocol state.
	if v.Decision == "approve" {
		a.Status = "approved"
	}
	a.Approver = c.state.Human.ID
	a.CredentialID = id
	a.ApprovedAt = c.opts.Now().UnixMilli()
	c.log("Browser passkey decision "+a.Status+": "+a.ID, "", "info")
	return c.humanApprovalView(a), nil
}

// beginPasskeyPairing binds exact device identity and requested capability limits before the browser prompt.
func (c *Controller) beginPasskeyPairing(r *http.Request) (any, error) {
	// Require the configured controller origin before consuming any challenge.
	if err := c.browserOrigin(r); err != nil {
		return nil, err
	}
	var b scopedPairingRequest
	// Reject malformed or unknown request fields before changing state.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	// Stop when validation or proof verification fails.
	if _, err := c.livePairing(b.ID); err != nil {
		return nil, err
	}
	// Stop when validation or proof verification fails.
	if err := c.validateDeploymentScope(b.Scope); err != nil {
		return nil, err
	}
	return c.beginPasskeyProof(browserCeremony{Kind: "pair", PairID: b.ID, Scope: b.Scope, Generation: c.state.Generation})
}

// finishPasskeyPairing shows the code only after verified owner consent to the captured scope.
func (c *Controller) finishPasskeyPairing(r *http.Request) (any, error) {
	v, err := c.takeBrowserCeremony(r, "pair")
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	// Reject the request: Controller changed; review the device scope again.
	if v.Generation != c.state.Generation {
		return nil, model.Err("STALE_PLAN", "Controller changed; review the device scope again")
	}
	id, err := c.finishPasskeyProof(r, v)
	// Stop when validation or proof verification fails.
	if err != nil {
		return nil, err
	}
	return c.issuePairCode(v.PairID, v.Scope, id)
}
