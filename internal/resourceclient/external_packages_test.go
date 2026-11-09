package resourceclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"hostlier/internal/model"
	"hostlier/internal/packageexec"
	"hostlier/pkg/workflow"
)

// TestExternalPackageRepository checks real artifacts against the core's independently compiled protocol types.
func TestExternalPackageRepository(t *testing.T) {
	directory := os.Getenv("HOSTLIER_PACKAGES_DIR")
	// Ordinary tests need no additional checkout or prebuilt artifacts.
	if directory == "" {
		t.Skip("set HOSTLIER_PACKAGES_DIR to a built hostlier-packages checkout")
	}
	state, err := filepath.EvalSymlinks(t.TempDir())
	// Protected installations cannot traverse platform temporary-directory aliases.
	if err != nil {
		t.Fatal(err)
	}
	runner := packageexec.Runner{Directory: filepath.Join(state, "packages")}
	manager, err := New(Options{Type: "docker", StateDir: state})
	// Read-only package plans must initialize without a running Docker daemon.
	if err != nil {
		t.Fatal(err)
	}
	// Resource operations preserve their existing version-one wire shapes across repositories.
	for _, engine := range []string{"postgres", "redis", "neo4j"} {
		_, err := runner.Install(engine, filepath.Join(directory, "dist", "hostlier-"+engine))
		// Exercise the production protected local installer.
		if err != nil {
			t.Fatal(engine, err)
		}
		plan, err := manager.Plan(context.Background(), model.ResourceSpec{Name: "sample-db", Type: engine, Storage: "1Gi"})
		// Core decoding and executable digest binding must both succeed.
		if err != nil || plan.Spec.Type != engine || len(plan.Spec.PackageDigest) != 64 {
			t.Fatal(engine, plan, err)
		}
	}
	checkExternalWorkflowPackage(t, runner, state)
	checkExternalProviderPackage(t, runner, directory)
}

// checkExternalProviderPackage verifies the standalone Aiven adapter's public enrollment contract.
func checkExternalProviderPackage(t *testing.T, runner packageexec.Runner, directory string) {
	t.Helper()
	_, err := runner.Install("aiven", filepath.Join(directory, "dist", "hostlier-aiven"))
	// Installation must accept the standalone repository's executable format and permissions.
	if err != nil {
		t.Fatal("aiven install", err)
	}
	capability, err := runner.Describe(context.Background(), "aiven")
	// Discovery must satisfy the core decoder without linking the provider into Hostlier.
	if err != nil || capability.Version != 1 || capability.Name != "aiven" || capability.ConfigSchema == nil || capability.OutputSchema == nil {
		t.Fatal("aiven discovery", capability, err)
	}
}

// checkExternalWorkflowPackage verifies capability discovery and typed workflow planning through private pipes.
func checkExternalWorkflowPackage(t *testing.T, runner packageexec.Runner, state string) {
	t.Helper()
	capability, err := runner.Describe(context.Background(), "postgres")
	// A matching JSON shape must be accepted by the core capability decoder.
	if err != nil || capability.Version != 1 || capability.Name != "postgres" {
		t.Fatal(capability, err)
	}
	request := workflow.Request{Version: 1, Key: "external-repository-check", Step: workflow.Step{ID: "database", Package: "postgres", Config: map[string]any{"mode": "new-instance", "storage": "1Gi"}}, Host: &workflow.HostContext{Runtime: "docker", StateDir: state}}
	plan, err := runner.Plan(context.Background(), request)
	// Planning may prepare intent but must not need live infrastructure or execute it.
	if err != nil || len(plan.Digest) != 64 || len(plan.Summary) == 0 {
		t.Fatal(plan, err)
	}
}
