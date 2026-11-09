package cli

import (
	"net/url"
	"os"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// logs selects the controller or application log stream.
func (s *session) logs() error {
	path := "/v1/logs"
	// Require application identity only for application log commands.
	if s.command == "logs" {
		// Reject this path: Application name required.
		if s.sub == "" {
			return model.Err("ARGUMENTS", "Application name required")
		}
		path = "/v1/apps/" + url.PathEscape(s.sub) + "/logs"
		// Component selection remains an encoded query, never a runtime identifier.
		if s.flags["component"] != "" {
			path += "?component=" + url.QueryEscape(s.flags["component"])
		}
		// Select application logs only when a name was supplied.
	} else if s.flags["operation"] != "" {
		path += "?operationId=" + url.QueryEscape(s.flags["operation"])
	}
	return s.followLogs(path)
}

// pairing manages administrator-controlled enrollment challenges.
func (s *session) pairing() error {
	var e error

	// Select an administrator-controlled enrollment action.
	switch s.sub {
	// Issue a scoped code only through the protected local administration socket.
	case "code":
		return s.pairingCode()
	// Issue a fresh one-time token for administrator delivery.
	case "create":
		name := s.flags["name"]
		// Supply a stable default identity when no pairing name was given.
		if name == "" {
			name = "new-client"
		}
		var result any
		// Require the controller to accept this request before advancing the command workflow.
		if e = s.request("POST", "/v1/pairing/tokens", map[string]string{"name": name}, &result); e != nil {
			return e
		}
		return s.print(result)
	// Show pending identities without secret enrollment material.
	case "list":
		return s.getPrint("/v1/pairing")
	// Invalidate the selected enrollment challenge.
	case "cancel":
		// Reject this path: Enrollment id required.
		if s.target == "" {
			return model.Err("ARGUMENTS", "Enrollment id required")
		}
		var result any
		// Require the controller to accept this request before advancing the command workflow.
		if e = s.request("POST", "/v1/pairing/cancel", map[string]string{"id": s.target}, &result); e != nil {
			return e
		}
		return s.print(result)
	// Inspect and explicitly approve the requested identity.
	case "approve":
		return s.approvePairing()
	}
	return model.Err("ARGUMENTS", "Usage: pairing create | list | cancel ID | approve")
}

// clients lists enrolled clients or revokes a selected credential.
func (s *session) clients() error {
	var e error

	// List public client identities without exposing bearer credentials.
	if s.sub == "" || s.sub == "list" {
		return s.getPrint("/v1/clients")
	}
	// Revoke only an explicitly identified enrolled client.
	if s.sub == "revoke" && s.target != "" {
		var result any
		// Require the controller to accept this request before advancing the command workflow.
		if e = s.request("POST", "/v1/clients/"+url.PathEscape(s.target)+"/revoke", nil, &result); e != nil {
			return e
		}
		return s.print(result)
	}
	return model.Err("ARGUMENTS", "Usage: client list | client revoke ID")
}

// secret validates secret identity and sends encrypted-at-rest revision updates.
func (s *session) secret() error {
	var e error

	// List revision metadata without reading secret values.
	if s.sub == "list" {
		if s.flags["app"] != "" {
			return model.Err("ARGUMENTS", "--app is only valid with secret set")
		}
		return s.getPrint("/v1/secrets")
	}
	// Reject this path: Secret name required.
	if s.target == "" {
		return model.Err("ARGUMENTS", "Secret name required")
	}
	// Send explicit deletions through server-side retained-reference checks.
	if s.sub == "delete" {
		if s.flags["app"] != "" {
			return model.Err("ARGUMENTS", "--app is only valid with secret set")
		}
		var result any
		// Require the controller to accept the protected secret operation before reporting success.
		if e = s.request("DELETE", "/v1/secrets/"+url.PathEscape(s.target), struct{}{}, &result); e != nil {
			return e
		}
		return s.print(result)
	}
	value, e := s.secretValue()
	// Stop before upload when the selected secret source could not be read.
	if e != nil {
		return e
	}

	var metadata secretMetadata
	// Require the controller to accept the protected secret operation before reporting success.
	body := map[string]any{"value": value, "requestId": client.RequestID()}
	if s.flags["app"] != "" {
		body["owner"] = s.flags["app"]
	}
	if e = s.request("PUT", "/v1/secrets/"+url.PathEscape(s.target), body, &metadata); e != nil {
		return e
	}
	return s.print(metadata)
}

// domains lists or edits the server domain allowlist.
func (s *session) domains() error {
	var e error

	// Read the domain allowlist without mutation.
	if s.sub == "" || s.sub == "list" {
		return s.getPrint("/v1/domains")
	}
	// Reject this path: Domain required.
	if s.target == "" {
		return model.Err("ARGUMENTS", "Domain required")
	}
	var result any
	// Send domain additions through server-side normalization.
	if s.sub == "add" {
		e = s.request("POST", "/v1/domains", map[string]string{"domain": s.target}, &result)
		// Send removals through the exact domain deletion route.
	} else if s.sub == "remove" {
		e = s.request("DELETE", "/v1/domains/"+url.PathEscape(s.target), nil, &result)
	} else {
		return model.Err("ARGUMENTS", "Usage: domain list | add HOSTNAME | remove HOSTNAME")
	}
	// Do not print success when the domain update was rejected.
	if e != nil {
		return e
	}
	return s.print(result)
}

// secretValue selects explicit stdin, environment or hidden-prompt secret input.
func (s *session) secretValue() (string, error) {
	// Reject this path: Usage: secret set NAME [--stdin | --from-env VARIABLE]; values must never be arguments.
	if s.sub != "set" || len(s.parsed.args) != 3 {
		return "", model.Err("ARGUMENTS", "Usage: secret set NAME [--stdin | --from-env VARIABLE]; values must never be arguments")
	}
	// Reject this path: Secret input modes conflict.
	if s.flags["stdin"] != "" && s.flags["from-env"] != "" {
		return "", model.Err("ARGUMENTS", "Secret input modes conflict")
	}
	value, e := s.readSecretValue()
	// Reject an unreadable or invalid secret input source.
	if e != nil {
		return "", e
	}
	// Reject this path: Secret input is missing or empty.
	if value == "" {
		return "", model.Err("INPUT", "Secret input is missing or empty")
	}
	// Reject this path: Secret exceeds 65536 byte limit.
	if len(value) > 65536 {
		return "", model.Err("INPUT_LIMIT", "Secret exceeds 65536 byte limit")
	}
	return value, nil
}

// approvePairing shows the pending client identity before administrator approval.
func (s *session) approvePairing() error {
	// Local administrator enrollment retains its explicit interactive review boundary.
	if !s.interactive {
		return model.Err("INTERACTIVE_REQUIRED", "Review pairing approval in an interactive server terminal")
	}
	code := s.target
	var e error
	// A displayed pairing code can be passed directly or entered at the existing prompt.
	if code == "" {
		code, e = s.ask("Approval code: ", false)
	}
	// A canceled prompt must never consume the administrator approval challenge.
	if e != nil {
		return e
	}
	var pending map[string]any
	// Show the requested identity before asking for administrator approval.
	if e = s.pairingAdminRequest("/v1/pairing/inspect", map[string]string{"code": code}, &pending); e != nil {
		return e
	}
	pending["authority"] = "Trusted server administrator: full server management access"
	s.print(pending)
	// Require explicit interactive approval before performing the reviewed mutation.
	if e = s.approve("Grant this client administrator authority? [y/N] "); e != nil {
		return e
	}
	var result any
	// Grant enrollment only after the inspected identity has been approved.
	if e = s.pairingAdminRequest("/v1/pairing/approve", map[string]string{"code": code}, &result); e != nil {
		return e
	}
	return s.print(result)
}

// readSecretValue reads a bounded secret without echoing or trimming its contents.
func (s *session) readSecretValue() (string, error) {
	var e error
	var value string
	// Read stdin only when it is the caller-selected secret source.
	if s.flags["stdin"] != "" {
		value, e = readInput(s.in)
		// Read hidden terminal input when no explicit source was selected.
	} else if variable := s.flags["from-env"]; variable != "" {
		// Use the injected environment reader for deterministic secret lookup.
		if s.options.Environment != nil {
			value = s.options.Environment[variable]
		} else {
			value = os.Getenv(variable)
		}
	} else {
		value, e = s.ask("Secret value: ", true)
	}
	return value, e
}

// secretMetadata decodes secret names and revision identities without secret values.
type secretMetadata struct {
	Name     string `json:"name"`
	Revision any    `json:"revision"`
}

// logResponse decodes only the response fields needed by this CLI workflow.
type logResponse struct {
	Text    string `json:"text"`
	Entries any    `json:"entries"`
}

// pairingAdminRequest preserves root-socket recovery authority while remote approval retains its hardware gate.
func (s *session) pairingAdminRequest(path string, body, result any) error {
	// The explicitly selected Unix socket proves local administrator authority independently of bearer tokens.
	if s.flags["socket"] != "" {
		return s.remote.Request(s.ctx, "POST", path, body, result)
	}
	return s.request("POST", path, body, result)
}
