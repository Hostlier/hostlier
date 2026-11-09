package resourceclient

import (
	"context"
	"errors"
	"hostlier/internal/model"
	"hostlier/pkg/resourceprotocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMissingPackagesRemainLazy confirms controller construction needs no engine package.
func TestMissingPackagesRemainLazy(t *testing.T) {
	m, err := New(Options{StateDir: t.TempDir()})
	// Construction must not inspect optional installed executables.
	if err != nil || m.Name() != "docker" {
		t.Fatal(err)
	}
	_, err = m.Plan(context.Background(), model.ResourceSpec{Type: "postgres", Name: "db", Storage: "1Gi"})
	// Resource requests explain the required package installation.
	if err == nil || !strings.Contains(err.Error(), "hostlier package install") {
		t.Fatal(err)
	}
	assertResourceError(t, err, "RESOURCE_PACKAGE_PREFLIGHT")
}

// TestPlanPinsDigestAndRefusesReplacement verifies approval binds executable bytes.
func TestPlanPinsDigestAndRefusesReplacement(t *testing.T) {
	m, path := fakePackage(t)
	plan, err := m.Plan(context.Background(), model.ResourceSpec{Type: "postgres", PackageDigest: "untrusted"})
	// Incoming approval metadata must be replaced with the measured executable digest.
	if err != nil || len(plan.Spec.PackageDigest) != 64 {
		t.Fatal(plan, err)
	}
	// Simulate an administrator replacing the installed engine after approval.
	if err = os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = m.Provision(context.Background(), plan.Spec, model.ResourceContext{})
	// A replacement is rejected before any process can change retained resources.
	assertResourceError(t, err, "RESOURCE_PACKAGE_PREFLIGHT")
}

// fakePackage installs a deterministic private protocol fixture.
func fakePackage(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	packages := filepath.Join(dir, "packages")
	// Secure administrator permissions are required by the shared executable verifier.
	if err := os.Mkdir(packages, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(packages, "postgres")
	script := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = resource ] || exit 1\ncat >/dev/null\nprintf '%s' '{\"version\":1,\"plan\":{\"spec\":{\"name\":\"db\",\"type\":\"postgres\",\"storage\":\"1Gi\"},\"changes\":[]}}'\n"
	// Fixtures run through the exact same snapshot transport as production packages.
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	m, _ := New(Options{StateDir: dir})
	return m, path
}

// TestResponseBoundsAndTrailingDocuments rejects malformed private package output.
func TestResponseBoundsAndTrailingDocuments(t *testing.T) {
	_, err := response([]byte(`{"version":1} {"version":1}`))
	// A second document cannot be smuggled through the private transport.
	if err == nil {
		t.Fatal("trailing JSON accepted")
	}
	output := &boundedOutput{}
	n, err := output.Write(make([]byte, resourceprotocol.MaxWireBytes+1))
	// Overflow is drained but never retained beyond the configured wire limit.
	if err != nil || n != resourceprotocol.MaxWireBytes+1 || !output.overflow || output.Len() != resourceprotocol.MaxWireBytes {
		t.Fatal(n, err, output.Len())
	}
}

// TestRestorePinsProvisionedExecutable prevents replacement between approved provisioning and restore.
func TestRestorePinsProvisionedExecutable(t *testing.T) {
	m, path := fakePackage(t)
	plan, err := m.Plan(context.Background(), model.ResourceSpec{Type: "postgres", Name: "db", Storage: "1Gi"})
	// The original executable is approved and invoked for target provisioning.
	if err != nil {
		t.Fatal(err)
	}
	// Provision must complete with the approved executable before testing the restore boundary.
	if _, err = m.Provision(context.Background(), plan.Spec, model.ResourceContext{}); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"db":{"name":"db","type":"postgres"}}`)
	// Native managers persist resource identities after authenticated provisioning.
	if err = os.WriteFile(filepath.Join(m.options.StateDir, "resources-docker.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	approved := model.ResourceContext{PackageDigest: plan.Spec.PackageDigest}
	// Unchanged executable bytes remain eligible for the separately approved restore.
	if err = m.Restore(context.Background(), "db", []byte("backup"), approved); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(m.options.StateDir, "unexpected-execution")
	script := "#!/bin/sh\ncat >/dev/null\nprintf invoked > '" + marker + "'\nprintf '%s' '{\"version\":1}'\n"
	// A replacement would leave a marker if the broker ever started it.
	if err = os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	err = m.Restore(context.Background(), "db", []byte("backup"), approved)
	assertResourceError(t, err, "RESOURCE_PACKAGE_PREFLIGHT")
	// Preflight rejection proves that no replacement process or restore side effect occurred.
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("replacement executable was invoked")
	}
}

// TestMissingDigestsRejectBeforeProcess keeps unreviewed mutation failures distinct from uncertain process failures.
func TestMissingDigestsRejectBeforeProcess(t *testing.T) {
	m, _ := fakePackage(t)
	_, err := m.Provision(context.Background(), model.ResourceSpec{Type: "postgres"}, model.ResourceContext{})
	assertResourceError(t, err, "RESOURCE_PACKAGE_PREFLIGHT")
	err = m.Restore(context.Background(), "db", []byte("backup"), model.ResourceContext{})
	assertResourceError(t, err, "RESOURCE_PACKAGE_PREFLIGHT")
}

// TestProcessFailureIsNotPreflight preserves uncertainty once an engine process can have performed side effects.
func TestProcessFailureIsNotPreflight(t *testing.T) {
	m, path := fakePackage(t)
	// A failing installed executable is still a verified process that may have started mutations.
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncat >/dev/null\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := m.Plan(context.Background(), model.ResourceSpec{Type: "postgres"})
	assertResourceError(t, err, "RESOURCE_PACKAGE")
}

// assertResourceError checks the machine-readable controller classification rather than matching diagnostics.
func assertResourceError(t *testing.T, err error, code string) {
	t.Helper()
	var failure *model.Error
	// Controller state transitions depend on this exact before-process distinction.
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}
