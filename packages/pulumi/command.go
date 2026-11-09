package pulumi

import (
	"context"
	"errors"
	"hostlier/packages/iac"
	"io"
	"strings"

	"github.com/blang/semver"
)

// command routes Automation API argv through the pinned clean-environment executor.
type command struct {
	execution   iac.Execution
	environment map[string]string
}

// Version pins the CLI contract to the optional SDK release.
func (command) Version() semver.Version { return semver.MustParse("3.142.0") }

// Run ignores ambient process environment and never publishes native diagnostics.
func (c command) Run(ctx context.Context, workdir string, stdin io.Reader, stdout, stderr []io.Writer, additionalEnv []string, args ...string) (string, string, int, error) {
	env := map[string]string{}
	// Process only the explicitly declared identities or bindings for this operation.
	for k, v := range c.environment {
		env[k] = v
	}
	// Process only the explicitly declared identities or bindings for this operation.
	for _, entry := range additionalEnv {
		key, value, ok := strings.Cut(entry, "=")
		// Reject an identity or visibility mismatch before publishing the operation result.
		if ok && strings.HasPrefix(key, "PULUMI_") {
			env[key] = value
		}
	}
	raw, err := c.execution.Installed.Bundle.Run(ctx, "pulumi", args, workdir, env)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return "", "", 1, errors.New("pinned pulumi operation failed")
	}
	return string(raw), "", 0, nil
}
