package database

import (
	"errors"
	"fmt"
)

// ValidateProvider shares lifecycle-mode validation while leaving placement and engine allowlists with each provider.
func (c Config) ValidateProvider(capabilities []Capability, instancePresent bool, provider string) error {
	// Common engine, identifier and connection-reference validation takes precedence over provider placement.
	if err := c.Validate(capabilities); err != nil {
		return err
	}
	// Existing administrative connections must not silently ignore paid-instance settings.
	if c.Mode == NewDatabase {
		// Reject mixed lifecycle modes even when every supplied placement field is otherwise valid.
		if instancePresent {
			return errors.New("new-database cannot include instance settings")
		}
		return nil
	}
	// Creating billable infrastructure requires explicit provider settings rather than implicit account defaults.
	if !instancePresent {
		return fmt.Errorf("new-instance requires %s instance settings", provider)
	}
	return nil
}

// ValidateTokenReference prevents output publication from replacing an input provider credential.
func ValidateTokenReference(token, output string) error {
	// Tokens are protected references and must remain distinct from the application's output connection.
	if !SecretReference(token) || token == output {
		return errors.New("instance.tokenSecret must be a distinct secret reference")
	}
	return nil
}
