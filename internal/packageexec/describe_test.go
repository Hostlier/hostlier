package packageexec

import (
	"context"
	"encoding/json"
	"hostlier/pkg/workflow"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNativeBinaryDescribe verifies actual executable discovery without runtime access or secret inheritance.
func TestNativeBinaryDescribe(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	// Canonical private paths satisfy production installation checks.
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "postgres")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/hostlier-postgres")
	// Build the same command users install, rather than a protocol fixture.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	t.Setenv("HOSTLIER_TEST_SECRET", "must-not-be-inherited")
	runner := Runner{Directory: directory}
	capability, err := runner.Describe(context.Background(), "postgres")
	// Discovery must work without a runtime, input configuration, or provisioning credentials.
	if err != nil || capability.Version != 1 || capability.Name != "postgres" || !capability.Inspect || capability.OutputSchema == nil {
		t.Fatal(capability, err)
	}
	checkDiscoveryDirectory(t, directory)
	// Static metadata includes names and schemas, never inherited secret values.
	if strings.Contains(stringCapability(t, capability), "must-not-be-inherited") {
		t.Fatal("secret exposed")
	}
}

// checkDiscoveryDirectory verifies discovery leaves no runtime artifacts.
func checkDiscoveryDirectory(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	// Discovery must leave no runtime effects or executable snapshots behind.
	if err != nil || len(entries) != 1 || entries[0].Name() != "postgres" {
		t.Fatal(entries, err)
	}
}

// stringCapability serializes public metadata for accidental-secret checks.
func stringCapability(t *testing.T, capability workflow.Capability) string {
	t.Helper()
	raw, err := json.Marshal(capability)
	// Serialization must preserve the public JSON contract.
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
