package cli

import (
	"net/url"
	"strings"

	"hostlier/internal/model"
)

// fused manages controller-owned OAuth grants without handling Engine tokens in the CLI.
func (s *session) fused() error {
	// Status reads never create an approval request.
	if s.sub == "list" || s.sub == "status" {
		return s.getPrint("/v1/fused")
	}
	// A mutation must identify its exact connection.
	if s.target == "" {
		return model.Err("ARGUMENTS", "Fused connection name required")
	}
	return s.fusedChange()
}

// fusedChange dispatches named mutations after the common status and identity checks.
func (s *session) fusedChange() error {
	path := "/v1/fused/" + url.PathEscape(s.target)
	method := "POST"
	var body any
	// Each mutation uses the ordinary exact-request owner approval mechanism.
	switch s.sub {
	// Provisioning binds compiler output to per-app Fused consent and a stored runtime secret.
	case "provision":
		return s.fusedProvision(true)
	// Binding can recover a local export failure without replaying OAuth or issuing another token.
	case "bind":
		return s.fusedProvision(false)
	// Handle only this explicitly supported connection action.
	case "connect":
		// An omitted client ID uses Engine's short-lived dynamic registration.
		if s.flags["client-id"] == "" {
			return s.fusedConnectDynamic()
		}
		path += "/connect"
		body = map[string]string{"engineUrl": s.flags["engine-url"], "clientId": s.flags["client-id"], "clientSecretRef": s.flags["client-secret-ref"]}
	// Handle only this explicitly supported connection action.
	case "refresh":
		path += "/refresh"
	// Handle only this explicitly supported connection action.
	case "disconnect":
		method = "DELETE"
	// Handle only this explicitly supported connection action.
	case "token":
		path += "/token"
		// The CLI accepts an explicit comma-separated operation allowlist.
		body = map[string]any{"familyId": s.flags["family-id"], "secretName": s.flags["secret-name"], "tokenName": s.flags["name"], "allow": strings.FieldsFunc(s.flags["allow"], func(r rune) bool { return r == ',' })}
	// Unknown actions must not reach Engine.
	default:
		return model.Err("ARGUMENTS", "Use fused connect|provision|bind|status|refresh|token|disconnect")
	}
	var result any
	// Propagate failure before publishing any connection state.
	if err := s.request(method, path, body, &result); err != nil {
		return err
	}
	return s.print(result)
}
