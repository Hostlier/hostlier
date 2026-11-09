package model

import "net/url"

// OwnerSetupRequest binds local administrator bootstrap to an HTTPS origin and pending device.
type OwnerSetupRequest struct {
	URL       string `json:"url"`
	RequestID string `json:"requestId"`
	Name      string `json:"name"`
}

// OwnerSetupResult carries a short-lived secret link only over trusted administrator transport.
type OwnerSetupResult struct {
	Status    string `json:"status"`
	URL       string `json:"url,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
}

// ControllerURL validates the shared credential-free HTTPS controller identity.
func ControllerURL(value string) (string, error) {
	u, e := url.Parse(value)
	// Reject non-HTTPS addresses and any URL fields that could redirect or expose credentials.
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", Err("HTTPS_REQUIRED", "Controller address must be an HTTPS origin without credentials or a path")
	}
	return "https://" + u.Host, nil
}
