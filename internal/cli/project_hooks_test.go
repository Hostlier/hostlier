package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/artifact"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
)

// Package code is an independently named executable. It never needs a controller or a prior init.
func writeHookFixture(t *testing.T, hooks string) (root, state string) {
	t.Helper()
	root = t.TempDir()
	state = t.TempDir()
	dir := filepath.Join(state, "packages")
	// Install into the same protected directory used by normal local packages.
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source := `#!/bin/sh
set -eu
case "$1" in
describe) echo '{"version":1,"name":"lantern","configSchema":{},"outputSchema":{},"hooks":HOOKS}' ;;
project-plan) cat > plan-input.json; cat proposal.json ;;
project-package)
  cat > package-input.json
  out=$(sed -n 's/.*"outputDir":"\([^"]*\)".*/\1/p' package-input.json)
  mkdir "$out/image"
  printf 'FROM scratch\nCOPY payload /payload\nCMD ["/payload"]\n' > "$out/image/Dockerfile"
  printf 'build-fixture\n' > "$out/image/payload"
  echo '{"version":1,"artifact":{"kind":"container-context","path":"image"}}'
  ;;
*) exit 3;;
esac
`
	// Expose only the hooks this fixture is testing.
	if err := os.WriteFile(filepath.Join(dir, "lantern"), []byte(strings.Replace(source, "HOOKS", hooks, 1)), 0700); err != nil {
		t.Fatal(err)
	}
	return root, state
}

// testProposal writes package-owned data read by the fixture planner.
func testProposal(t *testing.T, root string, p model.Project) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": 1, "project": p, "declarations": map[string]string{"operation": "read"}})
	// The fixture must encode a real Hostlier project and public declarations.
	if err != nil {
		t.Fatal(err)
	}
	// A failed write would make the hook response meaningless.
	if err = os.WriteFile(filepath.Join(root, "proposal.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// hookService carries a scoped credential and an ordinary controller deploy step.
func hookService(source model.Source) model.Project {
	return model.Project{Version: 2, Name: "lantern", Source: &source, Endpoint: &model.Endpoint{Exposure: "public", Port: 8080, Route: "lantern.example.com"}, Secrets: map[string]string{"RUNNER_CREDENTIAL": "scoped-runner"}, Workflow: &workflow.Spec{Steps: []workflow.Step{{ID: "deploy", Phase: "beforeDeploy", Package: "lantern", Hook: "project-deploy"}}}}
}

// TestPackagePlanWithoutInitOrController exercises grouped commands and independent planning.
func TestPackagePlanWithoutInitOrController(t *testing.T) {
	root, state := writeHookFixture(t, `["project-plan"]`)
	// A planner-only package can select any ordinary Hostlier source including a pinned registry image.
	p := hookService(model.Source{Image: "example/lantern:1.0"})
	testProposal(t, root, p)
	remote := &fakeClient{}
	out, err := invoke([]string{"app", "plan", "--package", "lantern", "--state-dir", state, "--architecture", "arm64", "--route", "selected.example.com", "--json"}, "", Options{CWD: root, Client: remote})
	// A fresh app must plan without init or controller enrollment.
	if err != nil {
		t.Fatal(err)
	}
	// The entire preview remains local.
	if len(remote.calls) != 0 {
		t.Fatal("local preview contacted controller", remote.calls)
	}
	var preview localProjectPlan
	// Read the same public result an external caller receives.
	if err = json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err, out)
	}
	// Route overrides, platform, credential references and controller steps survive.
	if preview.Project.Endpoint.Route != "selected.example.com" || preview.Architecture != "arm64" || preview.Project.Secrets["RUNNER_CREDENTIAL"] != "scoped-runner" || preview.Project.Workflow.Steps[0].Hook != "project-deploy" {
		t.Fatal(preview)
	}
	// Preview must not call the costly packaging stage.
	if _, err = os.Stat(filepath.Join(root, "package-input.json")); !os.IsNotExist(err) {
		t.Fatal("preview triggered packaging")
	}
	// The CLI must not implicitly initialize a package-owned app.
	if _, err = os.Stat(filepath.Join(root, ".hostlier")); !os.IsNotExist(err) {
		t.Fatal("preview initialized a project")
	}
}

// TestPackagingOnlyUsesAuthoredProject covers the saved selection and an optional plan hook.
func TestPackagingOnlyUsesAuthoredProject(t *testing.T) {
	root, state := writeHookFixture(t, `["project-package"]`)
	args := []string{"plan", "--package", "lantern", "--state-dir", state}
	opts := Options{CWD: root}
	// A packaging-only provider must either have a project or report what is missing.
	if _, err := invoke(args, "", opts); err == nil || !strings.Contains(err.Error(), "project-plan") {
		t.Fatal(err)
	}
	src := "version: 2\npackage: lantern\nname: lantern\nsource:\n  container:\n    context: build/artifact\n    architecture: arm64\nendpoint:\n  exposure: public\n  port: 8080\n  route: lantern.example.com\n"
	// The saved project is also usable without an init hook in this fixture.
	if err := os.WriteFile(filepath.Join(root, ".hostlier"), []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := invoke([]string{"plan", "--state-dir", state, "--json"}, "", opts)
	// Resolving a saved package must not require supplying the name again.
	if err != nil {
		t.Fatal(err)
	}
	checkAuthoredPackagePreview(t, out)
	// A conflicting explicit source owner cannot silently replace the saved choice.
	if _, err = invoke([]string{"plan", "--package", "wrong", "--state-dir", state}, "", opts); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatal(err)
	}
}

// checkAuthoredPackagePreview verifies the package-only source declaration.
func checkAuthoredPackagePreview(t *testing.T, out string) {
	t.Helper()
	var preview localProjectPlan
	// Preserve app identity and target architecture while selecting the generated artifact.
	if json.Unmarshal([]byte(out), &preview) != nil {
		t.Fatal("invalid preview JSON")
	}
	// Retain the source architecture selected in the authored project.
	if preview.Project.Name != "lantern" || preview.Architecture != "arm64" || preview.Project.Source.Container.Context != "." {
		t.Fatal(out)
	}
}

// TestPackageHookDeployWithDocker builds a real Linux image, uploads it and checks final review.
func TestPackageHookDeployWithDocker(t *testing.T) {
	// Keep ordinary test runs independent of Docker and its build cache.
	if os.Getenv("HOSTLIER_TEST_PROJECT_PACKAGE_DOCKER") == "" {
		t.Skip("set HOSTLIER_TEST_PROJECT_PACKAGE_DOCKER=1 for a real Docker build and verified artifact upload")
	}
	root, state := writeHookFixture(t, `["project-plan","project-package"]`)
	p := hookService(model.Source{Container: &model.ContainerSource{Context: ".", Architecture: "amd64"}})
	testProposal(t, root, p)
	fixture := &containerPlanFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "local-test"}}
	applied := false
	opts := Options{CWD: root}
	verifyInstalledEnrollment(t, opts, root, state)
	// The fixture supplies controller decisions while Hostlier builds and verifies real bytes.
	remote := &fakeClient{reply: func(c call) (any, error) {
		// Declare the small set of controller operations allowed by this test.
		switch c.path {
		// Exercise fallback for controllers without asynchronous plan preparation.
		case "/v1/packages/platform":
			return map[string]string{"os": "linux", "arch": "amd64"}, nil
		// The planning fixture deliberately omits asynchronous plan preparation.
		case "/v1/planning":
			// A legacy planning endpoint exercises the existing compatibility path.
			return nil, model.Err("NOT_FOUND", "fixture")
		// The immutable deployment must still contain the package’s declared workflow.
		case "/v1/plans":
			spec := c.body.(map[string]any)["spec"].(model.Project)
			// The reviewed request must retain both independent packages' contributions.
			if err := verifyFixtureProject(spec); err != nil {
				return nil, err
			}
		// Only an accepted plan can reach the apply endpoint.
		case "/v1/plans/container-plan/apply":
			applied = true
			return map[string]string{"operationId": "local-test"}, nil
		}
		return fixture.reply(c)
	}}
	// Fail on any hook, build, upload or deployment protocol error.
	if _, err := invoke([]string{"deploy", "--state-dir", state, "--detach"}, "", Options{CWD: root, Client: remote}); err != nil {
		t.Fatal(err)
	}
	// A passing deploy must exercise upload, plan and apply.
	if !applied || !verifiedHookUpload(fixture) {
		t.Fatal("unverified deploy", applied, fixture.chunks, fixture.plans)
	}
	checkPackagerWireInput(t, root)
}

// verifyFixtureProject ensures artifact preparation retains scoped credentials and contributor output bindings.
func verifyFixtureProject(spec model.Project) error {
	// An immutable artifact cannot authorize dropping the runner grant or the cache connection.
	if spec.Bindings["CACHE_URL"] != "cache.url" || spec.Secrets["RUNNER_CREDENTIAL"] != "scoped-runner" || spec.Workflow.Steps[0].Hook != "project-deploy" {
		return fmt.Errorf("lost composed workflow or bindings")
	}
	return nil
}

// verifiedHookUpload requires a verified upload followed by exactly one reviewed plan.
func verifiedHookUpload(f *containerPlanFixture) bool { return f.chunks > 0 && f.plans == 1 }

// checkPackagerWireInput checks that the packager received and used the reviewed proposal.
func checkPackagerWireInput(t *testing.T, root string) {
	t.Helper()
	var input struct {
		OutputDir    string
		Project      model.Project
		Declarations map[string]string
	}
	raw, err := os.ReadFile(filepath.Join(root, "package-input.json"))
	// Inspect the public wire input received by the packager.
	if err != nil || json.Unmarshal(raw, &input) != nil || input.Declarations["operation"] != "read" || input.Project.Workflow.Steps[0].Hook != "project-deploy" {
		t.Fatal(err, string(raw))
	}
	// Temporary packaging output must be cleaned up after acceptance.
	if _, err = os.Stat(input.OutputDir); !os.IsNotExist(err) {
		t.Fatal("packaging stage leaked after deploy")
	}
}

// TestPackageDeclarationDriftStopsBeforeUpload exercises the generic check for packages without their own drift detection.
func TestPackageDeclarationDriftStopsBeforeUpload(t *testing.T) {
	root, state := writeHookFixture(t, `["project-plan","project-package"]`)
	p := hookService(model.Source{Container: &model.ContainerSource{Context: ".", Architecture: "amd64"}})
	testProposal(t, root, p)
	path := filepath.Join(state, "packages", "lantern")
	script, err := os.ReadFile(path)
	// Modify the fixture packaging behavior before taking its verified executable snapshot.
	if err != nil {
		t.Fatal(err)
	}
	mutation := `  sed 's/"operation":"read"/"operation":"write"/' proposal.json > changed.json
  mv changed.json proposal.json
`
	script = []byte(strings.Replace(string(script), "  mkdir", mutation+"  mkdir", 1))
	// The fixture simulates another editor changing the requested scope during packaging.
	if err = os.WriteFile(path, script, 0700); err != nil {
		t.Fatal(err)
	}
	// A deploy now reads the receiver platform before preparing the image.
	remote := &fakeClient{reply: func(c call) (any, error) { return map[string]string{"os": "linux", "arch": "amd64"}, nil }}
	_, err = invoke([]string{"deploy", "--package", "lantern", "--state-dir", state, "--detach"}, "", Options{CWD: root, Client: remote})
	// A changed declaration invalidates the artifact before any upload or OAuth request.
	if err == nil || !strings.Contains(err.Error(), "changed during packaging") || len(remote.calls) != 1 || remote.calls[0].path != "/v1/packages/platform" {
		t.Fatal(err, remote.calls)
	}
}

// TestServerTargetedPlan uses the receiver's platform even when the CLI runs on another architecture.
func TestServerTargetedPlan(t *testing.T) {
	root, state := writeHookFixture(t, `["project-plan","project-package"]`)
	p := hookService(model.Source{Container: &model.ContainerSource{Context: ".", Architecture: "arm64"}})
	testProposal(t, root, p)
	// Only the platform read is required for a remote package preview.
	remote := &fakeClient{reply: func(c call) (any, error) { return map[string]string{"os": "linux", "arch": "arm64"}, nil }}
	args := []string{"plan", "--package", "lantern", "--server", "fixture", "--state-dir", state, "--json"}
	out, err := invoke(args, "", Options{CWD: root, Client: remote})
	// The planned target is taken from the same receiver that will admit the image.
	if err != nil || len(remote.calls) != 1 || remote.calls[0].path != "/v1/packages/platform" || !strings.Contains(out, `"architecture":"arm64"`) {
		t.Fatal(out, err, remote.calls)
	}
	_, err = invoke(append(args, "--architecture", "amd64"), "", Options{CWD: root, Client: remote})
	// Explicit mismatches stop before invoking build tools.
	if err == nil || !strings.Contains(err.Error(), "Controller targets linux/arm64") {
		t.Fatal(err)
	}
}
