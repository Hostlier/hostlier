package resources

import (
	"context"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
	"hostlier/internal/resourceclient"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// installPackagedResources builds optional executables and installs them through the local administrator CLI.
func installPackagedResources(t *testing.T, ctx context.Context, dir, binary string) {
	t.Helper()
	// Exercise all shipped engine package entry points independently from the controller binary.
	for _, engine := range []string{"postgres", "redis", "neo4j"} {
		source := buildResourcePackage(t, ctx, dir, engine)
		command := exec.CommandContext(ctx, binary, "package", "install", engine, "--file", source, "--state-dir", filepath.Join(dir, "state"))
		// The explicit generic installation command must activate protected executable bytes.
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("install %s: %v %s", engine, err, output)
		}
	}
}

// buildResourcePackage compiles a fixed-engine executable outside the controller installation directory.
func buildResourcePackage(t *testing.T, ctx context.Context, dir, engine string) string {
	t.Helper()
	source := filepath.Join(dir, "hostlier-"+engine)
	command := exec.CommandContext(ctx, "go", "build", "-o", source, "./cmd/hostlier-"+engine)
	command.Dir = filepath.Join("..", "..")
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	// Optional binaries must compile independently from the lightweight controller.
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v %s", engine, err, output)
	}
	return source
}

// checkPackagedDigestApproval ensures replacing reviewed executable bytes cannot provision an engine.
func checkPackagedDigestApproval(t *testing.T, ctx context.Context, dir string, broker *resourceclient.Manager, manager *Manager) {
	t.Helper()
	plan, err := broker.Plan(ctx, model.ResourceSpec{Name: "stale-package", Type: "postgres", Storage: "1Gi"})
	// Real package planning must bind its executable digest before approval.
	if err != nil || len(plan.Spec.PackageDigest) != 64 {
		t.Fatal(plan, err)
	}
	path := filepath.Join(dir, "state", "packages", "postgres")
	// A changed artifact can remain executable while no longer matching reviewed bytes.
	if err = os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = broker.Provision(ctx, plan.Spec, model.ResourceContext{Credentials: model.ResourceCredentials{Username: "hostlier", Database: "app", Password: "protected-password-123"}})
	// Digest mismatch must fail before any adapter receives the provision request.
	if err == nil {
		t.Fatal("changed resource package was provisioned")
	}
	assertNoStaleResource(t, ctx, dir, manager)
}

// assertNoStaleResource checks live Docker and metadata before restoring the installed package fixture.
func assertNoStaleResource(t *testing.T, ctx context.Context, dir string, manager *Manager) {
	t.Helper()
	// A stale package plan cannot produce even an unrecorded Docker container.
	if _, err := manager.inspect(ctx, "stale-package"); !code(err, "HTTP_404") {
		t.Fatalf("stale plan touched Docker: %v", err)
	}
	// Nor may rejected approval write resource runtime metadata.
	if _, err := manager.find("stale-package"); !code(err, "RESOURCE_NOT_FOUND") {
		t.Fatalf("stale plan wrote metadata: %v", err)
	}
	runner := packageexec.Runner{Directory: filepath.Join(dir, "state", "packages")}
	// Restore the original reviewed binary for the following real provisioning lifecycle.
	if _, err := runner.Install("postgres", filepath.Join(dir, "hostlier-postgres")); err != nil {
		t.Fatal(err)
	}
}

// canonicalResourceFixture removes operating-system aliases before secure package installation.
func canonicalResourceFixture(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	// Installation paths must not redirect through a symlink ancestor.
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
