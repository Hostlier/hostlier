package controller

import (
	"encoding/json"
	"net/http"

	"hostlier/internal/model"
	"hostlier/internal/sshidentity"
)

// apiSSHAccess admits only independently approved public-key enrollment or revocation.
func (c *Controller) apiSSHAccess(r *http.Request, actor string) (any, error) {
	// Preserve failure before publishing or using SSH authority.
	if err := c.maintenanceAdministrator(actor); err != nil {
		return nil, err
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if actor == "local" {
		return nil, model.Err("SSH_IDENTITY", "SSH enrollment requires a named owner device")
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if c.opts.SSHAccess == nil {
		return nil, model.Err("MAINTENANCE_UNAVAILABLE", "Upgrade the controller for managed SSH access")
	}
	// Dispatch only the explicitly supported SSH operations.
	switch r.Method {
	// Keep this operation within the validated SSH contract.
	case "POST":
		var input model.SSHAccessRequest
		// Preserve failure before publishing or using SSH authority.
		if err := decode(r, &input); err != nil {
			return nil, err
		}
		return c.opts.SSHAccess.Grant(r.Context(), actor, input)
	// Keep this operation within the validated SSH contract.
	case "DELETE":
		// Preserve failure before publishing or using SSH authority.
		if err := emptyBody(r); err != nil {
			return nil, err
		}
		// Preserve failure before publishing or using SSH authority.
		if err := c.opts.SSHAccess.Revoke(r.Context(), actor); err != nil {
			return nil, err
		}
		return map[string]bool{"revoked": true}, nil
	}
	return endpointNotFound()
}

// sshAccessReview binds account provisioning to an exact device and public key.
func (c *Controller) sshAccessReview(request humanRequest, actor string, review map[string]any) (json.RawMessage, error) {
	// Preserve failure before publishing or using SSH authority.
	if err := c.maintenanceAdministrator(actor); err != nil {
		return nil, err
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if c.opts.SSHAccess == nil {
		return nil, model.Err("MAINTENANCE_UNAVAILABLE", "Upgrade the controller for managed SSH access")
	}
	review["linuxUser"] = sshidentity.UserName(actor)
	review["privileges"] = "Unprivileged Linux shell; no sudo, Docker group, agent forwarding or port forwarding. SSH access persists until explicitly revoked."
	// Reject ambiguous state or honor the explicit SSH selection.
	if request.Method == "POST" {
		var input model.SSHAccessRequest
		// Reject ambiguous state or honor the explicit SSH selection.
		if json.Unmarshal(request.Body, &input) != nil {
			return nil, model.Err("INVALID_REQUEST", "Invalid SSH public key request")
		}
		_, fingerprint, err := sshidentity.PublicKey(input.PublicKey)
		// Preserve failure before publishing or using SSH authority.
		if err != nil {
			return nil, err
		}
		review["publicKeyFingerprint"] = fingerprint
		// Keep this value scoped to the SSH access contract.
	} else if request.Method != "DELETE" {
		return nil, model.Err("INVALID_REQUEST", "Unsupported SSH access action")
	}
	return json.Marshal(review)
}
