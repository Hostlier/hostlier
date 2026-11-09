package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/model"
	"hostlier/pkg/packagekit"
)

// TestEnrolledPackagesPlan checks installation before enrollment and plans with the installed fixtures.
func TestEnrolledPackagesPlan(t *testing.T) {
	root, state := writeHookFixture(t, `["project-plan","project-package"]`)
	testProposal(t, root, hookService(model.Source{Container: &model.ContainerSource{Context: ".", Architecture: "amd64"}}))
	opts := Options{CWD: root}
	args := verifyInstalledEnrollment(t, opts, root, state)
	out, err := invoke(args, "", opts)
	// The ordinary CLI plan must include the contributor and its binding.
	if err != nil || !strings.Contains(out, "CACHE_URL") || !strings.Contains(out, "cache-provider") {
		t.Fatal(out, err)
	}
	request, err := os.ReadFile(filepath.Join(root, "plan-input.json"))
	// Only the binding name crosses into the build request.
	if err != nil || !strings.Contains(string(request), `"providedBindings":["CACHE_URL"]`) {
		t.Fatal(string(request), err)
	}
}

// TestContributionConflict checks that neither enrollment order nor map order selects an owner.
func TestContributionConflict(t *testing.T) {
	proposals := map[string]packagekit.ProjectContribution{"one": {Bindings: map[string]string{"CACHE_URL": "one.url"}}, "two": {Bindings: map[string]string{"CACHE_URL": "two.url"}}}
	err := mergeContributions(&model.Project{}, proposals)
	// Different values for the same output must identify both owners.
	if err == nil || !strings.Contains(err.Error(), "one and two") {
		t.Fatal(err)
	}
}

// verifyInstalledEnrollment checks that a missing package leaves the manifest unchanged.
func verifyInstalledEnrollment(t *testing.T, opts Options, root, state string) []string {
	t.Helper()
	path := filepath.Join(root, ".hostlier")
	original := "version: 2\npackages: {}\n"
	writeContractFile(t, path, original)
	add := []string{"package", "add", "cache-provider", "--state-dir", state}
	// Missing packages must fail before any edits, including partial enrollment.
	if _, err := invoke(add, "", opts); err == nil || !strings.Contains(err.Error(), "not installed") || readContractFile(t, path) != original {
		t.Fatal("missing package changed enrollment", err)
	}
	installContributorFixture(t, state)
	for _, name := range []string{"lantern", "cache-provider", "cache-provider"} {
		// Repeated enrollment uses the same verified executable.
		if _, err := invoke([]string{"package", "add", name, "--state-dir", state}, "", opts); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(readContractFile(t, path), "cache-provider:") != 1 {
		t.Fatal("duplicate enrollment")
	}
	return []string{"plan", "--state-dir", state}
}

// installContributorFixture supplies an independent package that requests one Redis binding.
func installContributorFixture(t *testing.T, state string) {
	t.Helper()
	// An unrelated installed binary would fail discovery immediately if Hostlier scanned installed packages.
	if err := os.WriteFile(filepath.Join(state, "packages", "unenrolled"), []byte("#!/bin/sh\nexit 91\n"), 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1" in
 describe) echo '{"version":1,"name":"cache-provider","configSchema":{},"outputSchema":{},"hooks":["project-contribute"]}' ;;
 project-contribute) echo '{"version":1,"dependencies":{"cache":{"type":"redis","source":"provision","storage":"1Gi"}},"bindings":{"CACHE_URL":"cache.url"}}' ;;
 *) exit 1 ;;
esac
`
	// Install a separate package that has no artifact builder.
	if err := os.WriteFile(filepath.Join(state, "packages", "cache-provider"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}
