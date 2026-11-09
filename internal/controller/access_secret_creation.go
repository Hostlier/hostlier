package controller

import (
	"hostlier/internal/model"
	"strings"
)

// validateSecretCreationScope requires bounded exact names already approved for use.
func validateSecretCreationScope(s deploymentScope) error {
	// Separate creation authority must not expand the set of usable secrets.
	if len(s.CreateSecrets) > 20 {
		return model.Err("INVALID_AUTHORIZATION", "At most 20 secret creation names are allowed")
	}
	// Check every name, including names that already exist.
	for _, name := range s.CreateSecrets {
		// Wildcards and names outside the use scope cannot confer creation rights.
		if !scopeName.MatchString(name) || !containsName(s.Secrets, name) {
			return model.Err("INVALID_AUTHORIZATION", "Secrets allowed for creation must also be listed as allowed secret names")
		}
	}
	return nil
}

// secretCreationAllowed permits only first creation of an explicitly named local secret.
func (c *Controller) secretCreationAllowed(g *Authorization, a *HumanApproval) bool {
	name := strings.TrimPrefix(a.Path, "/v1/secrets/")
	return a.Method == "PUT" && a.Path == "/v1/secrets/"+name && scopeName.MatchString(name) && g.Host == "local" && containsName(g.CreateSecrets, name) && containsName(g.Secrets, name) && c.state.Secrets[name] == nil
}

// applySecretCreationAuthorization retains an encrypted, actor-bound execution receipt.
func (c *Controller) applySecretCreationAuthorization(a *HumanApproval) bool {
	// Each grant must independently authorize the entire request.
	for _, g := range c.state.Authorizations {
		// Creation cannot borrow authority from another device or expired grant.
		if g.Device != a.Actor || !c.authorizationLive(g) || !c.secretCreationAllowed(g, a) {
			continue
		}
		a.Status, a.Approver, a.CredentialID = "approved", g.KeyFingerprint, g.KeyFingerprint
		a.AuthorizationID, a.ApprovedAt = g.ID, c.opts.Now().UnixMilli()
		c.log("Secret creation authorized by bounded grant: "+g.ID, "", "info")
		return true
	}
	return false
}
