package cli

import (
	"errors"
	"hostlier/internal/model"
)

// reauth preserves the familiar renewal spelling through the shared connection workflow.
func (s *session) reauth() error { return s.connectController(true) }

// authRecoveryHint gives rejected remote credentials a direct recovery command without deleting profiles.
func (s *session) authRecoveryHint(err error) error {
	var failure *model.Error
	// Local administration and unrelated failures do not need remote reauthentication.
	if s.flags["socket"] != "" || !errors.As(err, &failure) || failure.Code != "UNAUTHORIZED" {
		return err
	}
	profiles, readErr := s.store.List()
	// Preserve the original failure when there is no saved profile to renew.
	if readErr != nil {
		return err
	}
	name := defaultValue(s.flags["server"], profiles.Current)
	// Do not suggest a command against an unknown or injected transport.
	if _, ok := profiles.Servers[name]; !ok {
		return err
	}
	return model.Err(failure.Code, failure.Message+". Run hostlier connect "+name+" to request fresh approval; your profile has been kept")
}
