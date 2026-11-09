package iac

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// bindingName is the existing protocol's finite output and secret-alias key syntax.
var bindingName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// validateInstalled prevents catalog credential mappings from overriding native backend or CLI controls.
func validateInstalled(b Installed) error {
	// The existing protocol requires an explicit application-safe output contract.
	if len(b.Outputs) == 0 {
		return errors.New("installed bundle requires declared outputs")
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err := b.Bundle.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	// Process only the explicitly declared identities or bindings for this operation.
	for _, name := range b.Outputs {
		// Reject an identity or visibility mismatch before publishing the operation result.
		if !bindingName.MatchString(name) || seen[name] {
			return errors.New("invalid output declaration")
		}
		seen[name] = true
	}
	return validateCredentials(b.SecretEnvironment)
}

// validateCredentials admits only provider credential namespaces and the local backend passphrase.
func validateCredentials(mapping map[string]string) error {
	seen := map[string]bool{}
	// Process only the explicitly declared identities or bindings for this operation.
	for alias, name := range mapping {
		// Reject an identity or visibility mismatch before publishing the operation result.
		if !bindingName.MatchString(alias) || seen[name] || !credentialEnvironment(name) {
			return errors.New("invalid credential mapping")
		}
		seen[name] = true
	}
	return nil
}

// credentialEnvironment excludes process injection, backend selectors and Pulumi program controls.
func credentialEnvironment(name string) bool {
	// Reject an identity or visibility mismatch before publishing the operation result.
	if strings.ContainsAny(name, "=\x00") {
		return false
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if name == "PULUMI_CONFIG_PASSPHRASE" {
		return true
	}
	// Process only the explicitly declared identities or bindings for this operation.
	for _, prefix := range []string{"AWS_", "AZURE_", "ARM_", "GOOGLE_", "TF_VAR_"} {
		// Reject an identity or visibility mismatch before publishing the operation result.
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// privateBackend verifies native checkpoint directories cannot redirect writes or expose other owners.
func privateBackend(path string) error {
	// Stop this phase when private storage or native execution cannot be verified.
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil || resolved != path {
		return errors.New("redirected native backend")
	}
	info, err := os.Lstat(path)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("unsafe native backend")
	}
	return nil
}
