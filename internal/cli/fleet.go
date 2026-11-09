package cli

import (
	"net/url"

	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// hosts lists fleet enrollment, publishes controller identity or proposes an enrollment change.
func (s *session) hosts() error {
	// Apply this check before granting authority or advancing protocol state.
	if s.sub == "" || s.sub == "list" {
		return s.getPrint("/v1/hosts")
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.sub == "identity" {
		return s.getPrint("/v1/hosts/identity")
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.validHostEnrollment() {
		var result any
		body := map[string]string{"name": s.target, "url": s.flags["url"], "fingerprint": s.flags["fingerprint"]}
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err := s.request("POST", "/v1/hosts", body, &result); err != nil {
			return err
		}
		return s.print(result)
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.sub == "revoke" && s.target != "" {
		var result any
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err := s.request("DELETE", "/v1/hosts/"+url.PathEscape(s.target), map[string]any{}, &result); err != nil {
			return err
		}
		return s.print(result)
	}
	return model.Err("ARGUMENTS", "Usage: hosts list|identity; hosts enroll NAME --url https://HOST:PORT --fingerprint SHA256; hosts revoke NAME")
}

// validHostEnrollment requires an explicit peer name, HTTPS endpoint and verified fingerprint.
func (s *session) validHostEnrollment() bool {
	return s.sub == "enroll" && s.target != "" && s.flags["url"] != "" && fleet.ValidPin(s.flags["fingerprint"])
}
