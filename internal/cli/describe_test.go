package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"hostlier/pkg/workflow"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestNativePackageDescribe verifies CLI discovery with an actual native executable.
func TestNativePackageDescribe(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	// Canonical paths satisfy package installation checks.
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "postgres")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/hostlier-postgres")
	// Exercise the shipped command instead of a protocol fixture.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	state := filepath.Join(directory, "state")
	runLocalPackage(t, directory, []string{"package", "install", "postgres", "--file", binary, "--state-dir", state})
	output := runLocalPackage(t, directory, []string{"package", "describe", "postgres", "--state-dir", state})
	var capability workflow.Capability
	// The CLI publishes the versioned identity and both public schemas.
	if json.Unmarshal(output, &capability) != nil || capability.Version != 1 || capability.Name != "postgres" || capability.ConfigSchema == nil || capability.OutputSchema == nil {
		t.Fatal(string(output))
	}
	checkNativeEnrollment(t, directory, state)
}

// runLocalPackage runs offline CLI commands without a controller profile.
func runLocalPackage(t *testing.T, home string, args []string) []byte {
	t.Helper()
	var output bytes.Buffer
	err := RunWithOptions(context.Background(), args, bytes.NewReader(nil), &output, &output, Options{Home: home})
	// Package commands require no authenticated remote session.
	if err != nil {
		t.Fatal(err, output.String())
	}
	return output.Bytes()
}
