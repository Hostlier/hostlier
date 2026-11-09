package packageexec

import (
	"context"
	"hostlier/pkg/workflow"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuiltTypedContract proves real executable plan/apply/inspect and lost-response recovery.
func TestBuiltTypedContract(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	os.Chmod(dir, 0700)
	binary := filepath.Join(dir, "typedfixture")
	build := exec.Command("go", "build", "-o", binary, "./testdata/typedfixture")
	// Build the actual subprocess fixture so the test covers the executable protocol.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, output)
	}
	runner := Runner{Directory: filepath.Join(dir, "packages")}
	// Install through the same digest-pinned runner used for external packages.
	if _, err := runner.Install("typedfixture", binary); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	req := workflow.Request{Key: "operation", Step: workflow.Step{ID: "fixture", Package: "typedfixture", Config: map[string]any{"loseResponse": true}}, Host: &workflow.HostContext{StateDir: state}}
	plan, err := runner.Plan(context.Background(), req)
	// A valid plan must contain the exact saved artifact needed for execution.
	if err != nil {
		t.Fatal(err)
	}
	// Public approval summaries must not disclose private outputs.
	if strings.Contains(strings.Join(plan.Summary, " "), "sentinel") {
		t.Fatal("private review")
	}
	req.Prepared = plan.Payload
	// The fixture deliberately exits after recording its effect, so apply cannot report success.
	if _, err = runner.Apply(context.Background(), req, plan.Digest); err == nil {
		t.Fatal("lost response accepted")
	}
	observed, err := runner.InspectPinned(context.Background(), req, plan.Digest)
	// Inspection must recover completion from the retained receipt without replaying apply.
	if err != nil || observed.Status != workflow.Completed {
		t.Fatal("recovery failed", observed, err)
	}
	verifySecretVisibility(t, observed.Result)
	verifySuccessfulApply(t, runner, req, filepath.Join(dir, "successful-state"))
	req.Step.Config["loseResponse"] = false
	// Changing approved inputs must invalidate the saved execution envelope.
	if _, err = runner.Apply(context.Background(), req, plan.Digest); err == nil {
		t.Fatal("changed intent reused")
	}
}

// verifySuccessfulApply covers the complete successful response path in a separate operation.
func verifySuccessfulApply(t *testing.T, runner Runner, req workflow.Request, state string) {
	t.Helper()
	req.Key = "successful-operation"
	req.Step.Config = map[string]any{"loseResponse": false}
	req.Host = &workflow.HostContext{StateDir: state}
	req.Prepared = nil
	p, err := runner.Plan(context.Background(), req)
	// The successful operation receives its own newly prepared identity.
	if err != nil {
		t.Fatal(err)
	}
	req.Prepared = p.Payload
	result, err := runner.Apply(context.Background(), req, p.Digest)
	// A normal apply must return the declared application-safe output.
	if err != nil || result.Outputs["status"] != "ready" {
		t.Fatal("successful apply", result, err)
	}
}

// verifySecretVisibility rejects private credentials in application-safe outputs.
func verifySecretVisibility(t *testing.T, result *workflow.Result) {
	t.Helper()
	// Credentials must remain exclusively in the private result channel.
	if result.Outputs["credential"] != "" || result.PrivateOutputs["credential"] != "sentinel-private-output" {
		t.Fatal("secret visibility")
	}
}
