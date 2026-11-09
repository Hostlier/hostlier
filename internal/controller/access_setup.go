package controller

import (
	"hostlier/internal/model"
	"net/http"
	"net/url"
)

// setupCLIFromAdministrator bridges trusted SSH administration to first-owner browser enrollment.
func (c *Controller) setupCLIFromAdministrator(r *http.Request, actor string) (any, error) {
	// Only the protected Unix administration socket can establish initial owner trust.
	if actor != "local" {
		return nil, model.Err("UNAUTHORIZED", "Owner setup requires protected local administration")
	}
	var input model.OwnerSetupRequest
	// Reject malformed intent before allocating a bootstrap token.
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	address, err := model.ControllerURL(input.URL)
	// A wrong SSH host must never mint a link for another controller.
	if err != nil || address != "https://"+c.state.Hostname {
		return nil, model.Err("CONTROLLER_MISMATCH", "SSH target does not match the requested HTTPS controller")
	}
	// The pending device must actually exist on this controller.
	if _, err = c.livePairing(input.RequestID); err != nil {
		return nil, err
	}
	// Reusing SSH on an initialized controller does not grant another owner credential.
	if c.humanEnrolled() {
		return model.OwnerSetupResult{Status: "ready"}, nil
	}
	link, expires, err := c.ownerEnrollmentLink(input.Name)
	// Validation and token allocation errors cannot be reported as successful setup.
	if err != nil {
		return nil, err
	}
	return model.OwnerSetupResult{Status: "enroll", URL: link + "&request=" + url.QueryEscape(input.RequestID), ExpiresAt: expires}, nil
}
