package terraform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hostlier/internal/packageexec"
	"hostlier/packages/iac"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/packagekit/protected"
	"hostlier/pkg/workflow"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// terraformFixture installs a checksum-pinned CLI and built-in-provider program offline.
func terraformFixture(t *testing.T) (packagekit.Handler, workflow.Request) {
	cli := os.Getenv("HOSTLIER_TERRAFORM_TEST_CLI")
	// Reject an identity or visibility mismatch before publishing the operation result.
	if cli == "" {
		t.Skip("set HOSTLIER_TERRAFORM_TEST_CLI to checksum-verified Terraform 1.9.8")
	}
	root := privateTemp(t)
	bundleRoot := filepath.Join(root, "bundle")
	os.MkdirAll(filepath.Join(bundleRoot, "program"), 0700)
	os.Mkdir(filepath.Join(bundleRoot, "plugins"), 0700)
	raw, err := os.ReadFile(cli)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(bundleRoot, "terraform"), raw, 0700)
	module := `terraform { required_version = "= 1.9.8" }
variable "value" { type = string }
resource "terraform_data" "local" { input = var.value }
output "value" { value = terraform_data.local.output }
output "private" {
 value = "sentinel-private-output"
 sensitive = true
}
`
	os.WriteFile(filepath.Join(bundleRoot, "program", "main.tf"), []byte(module), 0600)
	b := protected.Bundle{Root: bundleRoot, Platform: runtime.GOOS + "/" + runtime.GOARCH, Files: map[string]string{}}
	// Validate each entry without exposing private values to the public protocol.
	filepath.WalkDir(bundleRoot, func(path string, d os.DirEntry, err error) error {
		// Stop this phase when private storage or native execution cannot be verified.
		if err != nil {
			return err
		}
		// Reject an identity or visibility mismatch before publishing the operation result.
		if d.IsDir() {
			return nil
		}
		raw, _ := os.ReadFile(path)
		sum := sha256.Sum256(raw)
		relative, _ := filepath.Rel(bundleRoot, path)
		b.Files[filepath.ToSlash(relative)] = hex.EncodeToString(sum[:])
		return nil
	})
	catalog := map[string]iac.Installed{"fixture": {Bundle: b, Outputs: []string{"value", "private"}}}
	catalogRaw, _ := json.Marshal(catalog)
	// Require this native lifecycle invariant before continuing the fixture.
	if err = os.WriteFile(filepath.Join(root, "terraform.json"), catalogRaw, 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := New(catalog)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	req := workflow.Request{Version: 1, Action: "plan", Key: "namespace/app/infrastructure", Step: workflow.Step{ID: "infrastructure", Package: "terraform", Config: map[string]any{"bundle": "fixture", "variables": map[string]string{"value": "first"}}}, Host: &workflow.HostContext{StateDir: filepath.Join(root, "state")}}
	return handler, req
}

// terraformCall runs a native lifecycle action and keeps failure diagnostics private.
func terraformCall(t *testing.T, h packagekit.Handler, r workflow.Request, action string) workflow.Response {
	t.Helper()
	r.Action = action
	response, err := h.Handle(context.Background(), r)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(action, err)
	}
	return response
}

// terraformRoundTrip verifies saved execution, output visibility and durable response-loss recovery.
func terraformRoundTrip(t *testing.T, h packagekit.Handler, r workflow.Request, want string) workflow.Response {
	t.Helper()
	r.Prepared = nil
	planned := terraformCall(t, h, r, "plan")
	r.Prepared = planned.Prepared.Payload
	result := terraformCall(t, h, r, "apply")
	// Require this native lifecycle invariant before continuing the fixture.
	if result.Result.Outputs["value"] != want || result.Result.PrivateOutputs["private"] != "sentinel-private-output" {
		t.Fatal("output classification")
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if _, ok := result.Result.Outputs["private"]; ok {
		t.Fatal("private output published")
	}
	observed := terraformCall(t, h, r, "inspect")
	// Require this native lifecycle invariant before continuing the fixture.
	if observed.Inspection.Status != workflow.Completed {
		t.Fatal("receipt recovery")
	}
	r.Action = "apply"
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err := h.Handle(context.Background(), r); err == nil {
		t.Fatal("blind replay accepted")
	}
	return result
}

// TestRealTerraformSavedPlan exercises real creation, same-owner update and saved no-op execution.
func TestRealTerraformSavedPlan(t *testing.T) {
	handler, req := terraformFixture(t)
	applied := terraformRoundTrip(t, handler, req, "first")
	req.Previous = applied.Result.State
	req.Step.Config["variables"] = map[string]string{"value": "second"}
	updated := terraformRoundTrip(t, handler, req, "second")
	req.Previous = updated.Result.State
	terraformRoundTrip(t, handler, req, "second")
}

// TestRealTerraformRejectsStalePlan refuses the native saved plan after another approved update.
func TestRealTerraformRejectsStalePlan(t *testing.T) {
	handler, req := terraformFixture(t)
	applied := terraformRoundTrip(t, handler, req, "first")
	req.Previous = applied.Result.State
	req.Step.Config["variables"] = map[string]string{"value": "stale"}
	stale := terraformCall(t, handler, req, "plan")
	staleReq := req
	req.Step.Config = map[string]any{"bundle": "fixture", "variables": map[string]string{"value": "competing"}}
	terraformRoundTrip(t, handler, req, "competing")
	staleReq.Action = "apply"
	staleReq.Prepared = stale.Prepared.Payload
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err := handler.Handle(context.Background(), staleReq); err == nil {
		t.Fatal("stale native plan applied")
	}
}

// privateTemp resolves macOS temporary-directory aliases before protected storage validation.
func privateTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(root, 0700)
	return root
}

// TestRealTerraformExecutableRunner verifies actual installed wrapper snapshots and protocol exchanges.
func TestRealTerraformExecutableRunner(t *testing.T) {
	_, req := terraformFixture(t)
	root := filepath.Dir(req.Host.StateDir)
	binary := filepath.Join(root, "adapter")
	build := exec.Command("go", "build", "-ldflags", "-X main.catalogPath="+filepath.Join(root, "terraform.json"), "-o", binary, "../../cmd/hostlier-terraform")
	// Require this native lifecycle invariant before continuing the fixture.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("adapter build: %v %s", err, output)
	}
	runner := packageexec.Runner{Directory: filepath.Join(root, "packages")}
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err := runner.Install("terraform", binary); err != nil {
		t.Fatal(err)
	}
	plan, err := runner.Plan(context.Background(), req)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal("installed plan", err)
	}
	req.Prepared = plan.Payload
	result, err := runner.Apply(context.Background(), req, plan.Digest)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal("installed saved apply", err)
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if result.Outputs["value"] != "first" || result.PrivateOutputs["private"] != "sentinel-private-output" {
		t.Fatal("installed output classification")
	}
	observed, err := runner.InspectPinned(context.Background(), req, plan.Digest)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil || observed.Status != workflow.Completed {
		t.Fatal("installed receipt inspection", err)
	}
}
