package controller

import "time"

// allAccess treats only a completely empty permission selection as unrestricted.
func (s deploymentScope) allAccess() bool {
	return len(s.Apps) == 0 && len(s.Secrets) == 0 && len(s.CreateSecrets) == 0 && s.DurationSeconds == 0 && s.MaxUses == 0
}

// accessMode persists unrestricted authority explicitly rather than inferring it from old grants.
func (s deploymentScope) accessMode() string {
	// Any supplied restriction keeps the existing deployment validation rules.
	if s.allAccess() {
		return "all"
	}
	return "deploy"
}

// authorizationExpiry reserves zero expiry exclusively for explicitly unrestricted grants.
func (c *Controller) authorizationExpiry(mode string, seconds int64) int64 {
	// Unrestricted grants remain valid until revoked.
	if mode == "all" {
		return 0
	}
	return c.opts.Now().Add(time.Duration(seconds) * time.Second).UnixMilli()
}

// authorizationUnexpired never upgrades a legacy zero-expiry deployment grant.
func (c *Controller) authorizationUnexpired(g *Authorization) bool {
	return g.Scope == "all" && g.ExpiresAt == 0 || g.ExpiresAt > c.opts.Now().UnixMilli()
}

// applyAllAccessAuthorization retains the normal encrypted, device-bound execution receipt.
func (c *Controller) applyAllAccessAuthorization(a *HumanApproval) bool {
	// A single live grant must confer the entire administrative authority.
	for _, g := range c.state.Authorizations {
		// Never borrow another device's authority or bypass revocation.
		if g.Scope != "all" || g.Device != a.Actor || !c.authorizationLive(g) {
			continue
		}
		a.Status, a.Approver, a.CredentialID = "approved", g.KeyFingerprint, g.KeyFingerprint
		a.AuthorizationID, a.ApprovedAt = g.ID, c.opts.Now().UnixMilli()
		c.log("Request authorized by all-access grant: "+g.ID, "", "info")
		return true
	}
	return false
}

// canGrantAuthority accepts fresh owner approval or that owner's live administrative grant.
func (c *Controller) canGrantAuthority(a *HumanApproval) bool {
	// Local transport alone cannot invent an owner approval.
	if a == nil {
		return false
	}
	// Fresh passkey and hardware-key approval already passed independent verification.
	if a.AuthorizationID == "" {
		return true
	}
	g := c.state.Authorizations[a.AuthorizationID]
	return c.authorizationLive(g) && g.Scope == "all" && g.Device == a.Actor && g.KeyFingerprint == a.CredentialID
}
