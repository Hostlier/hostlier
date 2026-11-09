package cli

import (
	"fmt"
	"hostlier/internal/client"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"net/url"
	"os"
)

// projectSecret retains a resolved local snapshot only for this CLI invocation.
type projectSecret struct {
	name  string
	value string
}

// readProjectInput resolves all local values before validation, builds or remote side effects.
func (s *session) readProjectInput(namespace string) (model.Project, error) {
	input, err := config.ReadProjectSources(s.projectPath(), namespace)
	// Invalid input cannot cause partial secret uploads.
	if err != nil {
		return input.Project, err
	}
	values := make([]projectSecret, 0, len(input.Secrets))
	// Resolve every source before retaining the complete private snapshot.
	for _, source := range input.Secrets {
		value, err := s.projectSecretValue(source)
		// Missing local variables must fail before packaging or contacting the controller.
		if err != nil {
			return input.Project, err
		}
		values = append(values, projectSecret{name: source.Name, value: value})
	}
	s.projectSecrets = values
	return input.Project, nil
}

// projectSecretValue reads exact local bytes without printing values or performing interpolation.
func (s *session) projectSecretValue(source config.SecretSource) (string, error) {
	value := ""
	// Literal values have already passed bounded strict configuration validation.
	if source.Value != nil {
		value = *source.Value
		// An injected environment makes tests independent of the user's shell.
	} else if s.options.Environment != nil {
		value = s.options.Environment[source.FromEnv]
	} else {
		value = os.Getenv(source.FromEnv)
	}
	// Missing, empty and oversized values never become empty controller credentials.
	if len(value) == 0 || len(value) > 65536 {
		return "", model.Err("SECRET_SOURCE", "Local secret source "+source.FromEnv+" must contain 1–65536 bytes")
	}
	return value, nil
}

// uploadProjectSecrets uses the ordinary authenticated, approved encrypted-secret write path.
func (s *session) uploadProjectSecrets() error {
	// Reference-only projects do not issue secret writes.
	if len(s.projectSecrets) == 0 {
		return nil
	}
	fmt.Fprintf(s.errOut, "Synchronizing %d declared secret(s) before planning; values hidden.\n", len(s.projectSecrets))
	// Stable names preserve revision pinning while keeping values out of the plan specification.
	for _, secret := range s.projectSecrets {
		var result secretMetadata
		// Normal secret authorization still governs creation and replacement.
		if err := s.request("PUT", "/v1/secrets/"+url.PathEscape(secret.name), map[string]any{"value": secret.value, "requestId": client.RequestID()}, &result); err != nil {
			return err
		}
	}
	s.projectSecrets = nil
	return nil
}
