package cli

import (
	"strings"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// connect creates or renews a profile through one authentication and persistence workflow.
func (s *session) connect() error {
	if !strings.Contains(s.sub, "://") && strings.Contains(s.sub, "@") {
		s.target = s.sub
		return s.addServer()
	}
	return s.connectController(false)
}

// connectController shares target resolution, enrollment and atomic profile publication with reauth.
func (s *session) connectController(renewOnly bool) error {
	// Reject ambiguous options before reading credentials or opening a browser.
	if err := s.validateConnectionArguments(); err != nil {
		return err
	}
	target, err := s.resolveConnection(renewOnly)
	// Never infer a controller origin from an unknown profile name.
	if err != nil {
		return err
	}
	enrollment, err := s.connectionEnrollment(target)
	// Invalid or conflicting authentication choices cannot initiate enrollment.
	if err != nil {
		return err
	}
	// SSH bootstrap intent must be checked before any enrollment request is created.
	if err = s.validateSSHBootstrap(enrollment); err != nil {
		return err
	}
	s.serverTarget = target.URL
	remote, err := s.enrollmentRemote(target.URL)
	// HTTPS validation remains mandatory for every supported method.
	if err != nil {
		return err
	}
	credential, err := s.enrollConnection(remote, target.Name, enrollment)
	// Failed or cancelled authentication never modifies the saved profile.
	if err != nil {
		return err
	}
	// Every method must return a complete identity before publication.
	if credential.ClientID == "" || credential.Token == "" {
		return model.Err("ENROLLMENT", "Controller returned no device credential")
	}
	// New and replacement credentials use the same explicit publication boundary.
	if err = s.saveConnection(target, enrollment, credential); err != nil {
		return err
	}
	return s.connectionResult(target)
}

// saveConnection preserves the current selection when renewing an existing profile.
func (s *session) saveConnection(target connectionTarget, enrollment client.Enrollment, credential enrollmentCredential) error {
	// Existing profiles are checked against the identity captured before browser approval.
	if target.Previous != nil {
		return s.store.RenewEnrollment(target.Name, *target.Previous, credential.ClientID, credential.Token, enrollment)
	}
	return s.store.SaveEnrollment(target.Name, target.URL, credential.ClientID, credential.Token, enrollment)
}

// connectionResult keeps legacy reauth output while providing a common connect result.
func (s *session) connectionResult(target connectionTarget) error {
	// Existing integrations may depend on the reauth success field.
	if s.command == "reauth" {
		return s.print(map[string]any{"reauthenticated": true, "name": target.Name, "url": target.URL})
	}
	return s.print(map[string]any{"connected": true, "renewed": target.Previous != nil, "name": target.Name, "url": target.URL})
}

// enrollmentRemote opens the unauthenticated HTTPS transport used for fresh enrollment.
func (s *session) enrollmentRemote(address string) (Requester, error) {
	// Injected transports share the same enrollment and publication boundaries in tests.
	if s.options.Client != nil {
		return s.options.Client, nil
	}
	return client.NewGRPC(client.Options{URL: address})
}
