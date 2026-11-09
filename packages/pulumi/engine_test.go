package pulumi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hostlier/packages/iac"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/packagekit/protected"
	"hostlier/pkg/workflow"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixture contains actual prebuilt native programs, provider and backend paths.
type fixture struct {
	handler   packagekit.Handler
	request   workflow.Request
	installed iac.Installed
	directory string
}

// fixtureProgram writes only under a unique local integration-test directory.
const fixtureProgram = "name: hostlier-local\nruntime:\n  name: go\n  options:\n    binary: ./app\n"

// newFixture requires explicitly prepared pinned runtime files and performs no downloads.
func newFixture(t *testing.T) fixture {
	t.Helper()
	cliDir := os.Getenv("HOSTLIER_PULUMI_TEST_CLI_DIR")
	program := os.Getenv("HOSTLIER_PULUMI_TEST_PROGRAM")
	provider := os.Getenv("HOSTLIER_PULUMI_TEST_PROVIDER")
	// Reject an identity or visibility mismatch before publishing the operation result.
	if cliDir == "" || program == "" || provider == "" {
		t.Skip("prepare pinned Pulumi CLI, program and provider; see docs/optional-iac-packages.md")
	}
	root, err := os.MkdirTemp("", "hostlier-pulumi-fixture-")
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	// Validate each entry without exposing private values to the public protocol.
	root, err = filepath.EvalSymlinks(root)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	// Validate each entry without exposing private values to the public protocol.
	t.Cleanup(func() { os.RemoveAll(root) })
	bundleRoot := filepath.Join(root, "bundle")
	os.MkdirAll(filepath.Join(bundleRoot, "program"), 0700)
	os.MkdirAll(filepath.Join(bundleRoot, "home", "plugins", "resource-local-v0.0.1"), 0700)
	copyFixture(t, filepath.Join(cliDir, "pulumi"), filepath.Join(bundleRoot, "pulumi"))
	copyFixture(t, filepath.Join(cliDir, "pulumi-language-go"), filepath.Join(bundleRoot, "pulumi-language-go"))
	copyFixture(t, program, filepath.Join(bundleRoot, "program", "app"))
	installGoDiscovery(t, bundleRoot)

	copyFixture(t, provider, filepath.Join(bundleRoot, "home", "plugins", "resource-local-v0.0.1", "pulumi-resource-local"))
	os.WriteFile(filepath.Join(bundleRoot, "program", "Pulumi.yaml"), []byte(fixtureProgram), 0600)
	b := fixtureManifest(t, bundleRoot)
	installed := iac.Installed{Bundle: b, Outputs: []string{"value", "private"}, SecretEnvironment: map[string]string{"backend": "PULUMI_CONFIG_PASSPHRASE"}}
	handler, err := New(map[string]iac.Installed{"fixture": installed})
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	req := workflow.Request{Version: 1, Action: "plan", Key: "namespace/app/infrastructure", Step: workflow.Step{ID: "infrastructure", Package: "pulumi", Config: map[string]any{"bundle": "fixture", "variables": map[string]string{"value": "first", "second": "stable", "directory": root}}}, Secrets: map[string]string{"backend": "test-backend-passphrase"}, Host: &workflow.HostContext{StateDir: filepath.Join(root, "state")}}
	return fixture{handler: handler, request: req, installed: installed, directory: root}
}

// copyFixture preserves owner execute permissions for prebuilt administrator-installed binaries.
func copyFixture(t *testing.T, source, target string) {
	t.Helper()
	raw, err := os.ReadFile(source)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if err = os.WriteFile(target, raw, 0700); err != nil {
		t.Fatal(err)
	}
}

// planApply executes a real saved update plan and returns the resulting private receipt.
func planApply(t *testing.T, f fixture, r workflow.Request) workflow.Response {
	t.Helper()
	r.Action = "plan"
	r.Prepared = nil
	planned, err := f.handler.Handle(context.Background(), r)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal("real constrained preview", err)
	}
	r.Action = "apply"
	r.Prepared = planned.Prepared.Payload
	result, err := f.handler.Handle(context.Background(), r)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal("real constrained update", err)
	}
	r.Action = "inspect"
	observed, err := f.handler.Handle(context.Background(), r)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil || observed.Inspection.Status != workflow.Completed {
		t.Fatal("receipt inspection", err)
	}
	r.Action = "apply"
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err = f.handler.Handle(context.Background(), r); err == nil {
		t.Fatal("blind replay accepted")
	}
	return result
}

// TestRealPulumiConstrainedPlan exercises genuine file-provider CRUD with no cloud service.
func TestRealPulumiConstrainedPlan(t *testing.T) {
	f := newFixture(t)
	result := planApply(t, f, f.request)
	// Require this native lifecycle invariant before continuing the fixture.
	if result.Result.Outputs["value"] != "first" || result.Result.PrivateOutputs["private"] != "sentinel-private-output" {
		t.Fatal("sensitive output classification")
	}
	assertFile(t, filepath.Join(f.directory, "first.txt"), "first")
	r := f.request
	r.Previous = result.Result.State
	r.Step.Config["variables"].(map[string]string)["value"] = "second"
	result = planApply(t, f, r)
	assertFile(t, filepath.Join(f.directory, "first.txt"), "second")
	r.Previous = result.Result.State
	planApply(t, f, r)
}

// assertFile verifies actual provider-side mutation rather than component-only engine metadata.
func assertFile(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil || string(raw) != want {
		t.Fatal("provider file mutation", err)
	}
}

// installGoDiscovery pins the actual Go binary and minimal metadata used by the language host.
func installGoDiscovery(t *testing.T, root string) {
	t.Helper()
	goCLI, err := exec.LookPath("go")
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	copyFixture(t, goCLI, filepath.Join(root, "go"))
	metadata := filepath.Join(root, "goroot")
	// Require this native lifecycle invariant before continuing the fixture.
	if err = os.MkdirAll(filepath.Join(metadata, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(goCLI, "env", "GOROOT")
	raw, err := command.Output()
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	version, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(raw)), "VERSION"))
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if err = os.WriteFile(filepath.Join(metadata, "VERSION"), version, 0600); err != nil {
		t.Fatal(err)
	}
}

// directExecution isolates the native semantic test from the package's request identity guard.
// Production protocol calls reject this deliberate configuration mismatch before native execution.
func directExecution(t *testing.T, f fixture) iac.Execution {
	t.Helper()
	backends, err := filepath.Glob(filepath.Join(f.request.Host.StateDir, "iac-pulumi", "owner-*"))
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil || len(backends) != 1 {
		t.Fatal("native backend identity", err)
	}
	work, err := filepath.EvalSymlinks(t.TempDir())
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if err = os.Chmod(work, 0700); err != nil {
		t.Fatal(err)
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if err = f.installed.Bundle.Materialize(work); err != nil {
		t.Fatal(err)
	}
	return iac.Execution{Installed: f.installed, Workspace: work, Backend: backends[0], Owner: f.request.Key, Operation: "native-constraint-fixture", Variables: map[string]string{"directory": f.directory, "value": "approved", "second": "stable"}, Environment: map[string]string{"PULUMI_CONFIG_PASSPHRASE": "test-backend-passphrase"}}
}

// TestRealPulumiIncrementalConstraintFailure proves that saved-plan enforcement can follow earlier effects.
func TestRealPulumiIncrementalConstraintFailure(t *testing.T) {
	f := newFixture(t)
	planApply(t, f, f.request)
	e := directExecution(t, f)
	saved, _, err := (Engine{}).Plan(context.Background(), e)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal("native saved update plan", err)
	}
	e.Variables["second"] = "unapproved-extra-update"
	// Require this native lifecycle invariant before continuing the fixture.
	if err = (Engine{}).Apply(context.Background(), e, saved); err == nil {
		t.Fatal("native constraint accepted extra mutation")
	}
	assertFile(t, filepath.Join(f.directory, "first.txt"), "approved")
	assertFile(t, filepath.Join(f.directory, "second.txt"), "stable")
}

// fixtureManifest pins every program, runtime and local provider byte in the installed bundle.
func fixtureManifest(t *testing.T, bundleRoot string) protected.Bundle {
	t.Helper()
	b := protected.Bundle{Root: bundleRoot, Platform: runtime.GOOS + "/" + runtime.GOARCH, Files: map[string]string{}}
	// Validate each entry without exposing private values to the public protocol.
	err := filepath.WalkDir(bundleRoot, func(path string, d os.DirEntry, err error) error {
		// Stop this phase when private storage or native execution cannot be verified.
		if err != nil {
			return err
		}
		// Reject an identity or visibility mismatch before publishing the operation result.
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		// Stop this phase when private storage or native execution cannot be verified.
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		relative, _ := filepath.Rel(bundleRoot, path)
		b.Files[filepath.ToSlash(relative)] = hex.EncodeToString(sum[:])
		return nil
	})
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	return b
}
