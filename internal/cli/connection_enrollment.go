package cli

import (
	"hostlier/internal/client"
	emailauth "hostlier/internal/emailprotocol"
	"hostlier/internal/model"
	"strings"
)

// connectionEnrollment chooses one explicit method or reuses the saved non-secret preference.
func (s *session) connectionEnrollment(target connectionTarget) (client.Enrollment, error) {
	enrollment := client.Enrollment{Method: "browser"}
	// Profiles created before method persistence retain the existing browser default.
	if target.Previous != nil && target.Previous.Enrollment.Method != "" {
		enrollment = target.Previous.Enrollment
	}
	methods := map[string]string{"email": "email", "request": "request", "token-stdin": "token"}
	chosen := 0
	// Compatibility flags choose one method; they are never combined or tried as fallbacks.
	for flag, method := range methods {
		// Only explicitly supplied flags replace the saved method.
		if value, ok := s.flags[flag]; ok {
			chosen++
			enrollment = client.Enrollment{Method: method}
			// Remember the email identity rather than requesting it again on each renewal.
			if method == "email" {
				enrollment.Email = value
			}
		}
	}
	// Reject conflicting methods before any network request, prompt or token consumption.
	if chosen > 1 {
		return enrollment, model.Err("ARGUMENTS", "Choose only one enrollment option: --email, --request or --token-stdin")
	}
	return s.explicitConnectionEnrollment(enrollment, chosen)
}

// validateConnectionEnrollment refuses corrupt preferences and requires explicit secret input.
func (s *session) validateConnectionEnrollment(enrollment client.Enrollment) (client.Enrollment, error) {
	// Authentication methods have different authority; unknown values cannot silently fall back.
	switch enrollment.Method {
	// Browser and administrator-reviewed requests require no saved secret input.
	case "browser", "request":
		return enrollment, nil
	// Normalize and validate saved or explicitly selected mailbox identities.
	case "email":
		address, err := emailauth.Address(enrollment.Email)
		enrollment.Email = address
		return enrollment, err
	// One-time tokens must always be supplied afresh, never saved as a renewal input.
	case "token":
		// Ordinary renewal must not unexpectedly consume an unrelated stdin stream.
		if s.flags["token-stdin"] == "" {
			return enrollment, model.Err("INPUT", "This profile uses token enrollment; supply a fresh token with connect PROFILE --token-stdin")
		}
		return enrollment, nil
	}
	return enrollment, model.Err("ENROLLMENT", "Unsupported authentication method; use browser, email, request or token")
}

// enrollConnection dispatches one validated method with no cross-method retry or privilege expansion.
func (s *session) enrollConnection(remote Requester, name string, enrollment client.Enrollment) (enrollmentCredential, error) {
	// All connection spellings share the same method dispatch.
	switch enrollment.Method {
	// Email login reuses its saved identity but still requires a fresh code and device proof.
	case "email":
		s.flags["email"] = enrollment.Email
		return s.enrollEmail(remote, name)
	// Preserve explicitly selected legacy administrator request enrollment.
	case "request":
		return s.requestEnrollment(remote, name)
	// Token input stays separate from durable profile metadata.
	case "token":
		return s.enrollToken(remote, name)
	}
	return s.enrollBrowserCLI(remote, name)
}

// enrollToken redeems bounded stdin input without ever printing the enrollment token.
func (s *session) enrollToken(remote Requester, name string) (enrollmentCredential, error) {
	var credential enrollmentCredential
	token, err := readInput(s.in)
	// Input failures cannot consume a token on the controller.
	if err != nil {
		return credential, err
	}
	token = strings.TrimSpace(token)
	// Empty input must not initiate an ambiguous enrollment request.
	if token == "" {
		return credential, model.Err("INPUT", "Enrollment token is empty")
	}
	err = remote.Request(s.ctx, "POST", "/v1/pairing/redeem", map[string]string{"token": token, "name": name}, &credential)
	return credential, err
}

// explicitConnectionEnrollment makes one method selector authoritative without accepting conflicting aliases.
func (s *session) explicitConnectionEnrollment(enrollment client.Enrollment, chosen int) (client.Enrollment, error) {
	// Omission reuses the saved method or the browser default.
	if method, ok := s.flags["auth"]; ok {
		// Method inputs cannot select a different flow from the explicit selector.
		if chosen > 0 && enrollment.Method != method {
			return enrollment, model.Err("ARGUMENTS", "Authentication method conflicts with its enrollment options")
		}
		enrollment.Method = method
		// A mailbox identity is irrelevant to other methods and must not be retained accidentally.
		if method != "email" {
			enrollment.Email = ""
		}
	}
	return s.validateConnectionEnrollment(enrollment)
}
