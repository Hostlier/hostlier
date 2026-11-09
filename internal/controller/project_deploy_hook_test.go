package controller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/model"
	"hostlier/pkg/workflow"
)

// projectDeployFixture handles hook argv separately from ordinary resource package "serve".
func projectDeployFixture() int {
	var req workflow.Request
	// The fixture accepts only the controller workflow input.
	if json.NewDecoder(os.Stdin).Decode(&req) != nil {
		return 1
	}
	// Reject a release with mutable or mismatched runtime identity.
	if !matchesPinnedRelease(req) {
		return 2
	}
	response := workflow.Response{Version: 1}
	result := workflow.Result{State: json.RawMessage(`{"owned":true}`), Outputs: map[string]string{"ready": "yes"}}
	// Planning, observation and execution have distinct effect boundaries.
	switch req.Action {
	// Review retains the project so apply and inspect can verify it.
	case "plan":
		response.Prepared = &workflow.Prepared{Summary: []string{"Bind immutable release"}, Payload: req.Project}
	// Both later phases must reproduce the original review.
	case "apply", "inspect":
		// A changed project must not reach the external journal.
		if string(req.Project) != string(req.Prepared) {
			return 3
		}
		path := filepath.Join(req.Host.StateDir, "hook-journal")
		// Recovery observes prior effects without appending a new record.
		if req.Action == "inspect" {
			// Only a recorded external effect proves completion.
			if _, err := os.Stat(path); err != nil {
				return 4
			}
			response.Inspection = &workflow.Inspection{Status: workflow.Completed, Result: &result}
		} else {
			// An uncertain mutation must fail the hook response.
			if code := recordReleaseEffect(req, path); code != 0 {
				return code
			}
			response.Result = &result
		}
	// Unexpected lifecycle actions have no fixture behavior.
	default:
		return 7
	}
	// A response is successful only when fully written.
	if json.NewEncoder(os.Stdout).Encode(response) != nil {
		return 8
	}
	return 0
}

// describeReleaseFixture publishes the exact capability negotiated by the controller.
func describeReleaseFixture() int {
	capability := workflow.Capability{Version: 1, Name: "release-fixture", ConfigSchema: map[string]any{}, OutputSchema: map[string]any{}}
	// The test installs an explicit capability marker to opt in.
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "hook-enabled")); err == nil {
		capability.Hooks = []string{workflow.ProjectDeployHook}
	}
	// A partial capability document would invalidate discovery.
	if json.NewEncoder(os.Stdout).Encode(capability) != nil {
		return 1
	}
	return 0
}

// matchesPinnedRelease checks both the project identity and the canonical runtime inputs.
func matchesPinnedRelease(req workflow.Request) bool {
	var p model.Project
	// Both plan and execution must carry the resolved project identity and image.
	return json.Unmarshal(req.Project, &p) == nil && p.Name == "sample" && p.Route == "https://sample.example.com" && strings.Contains(p.Image, "@sha256:")
}

// recordReleaseEffect writes the external journal before simulating response delivery.
func recordReleaseEffect(req workflow.Request, path string) int {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	// Never report success if the fixture cannot record an effect.
	if err != nil {
		return 5
	}
	_, err = file.WriteString("applied\n")
	// Simulate a lost response only after recording the mutation.
	if file.Close() != nil || err != nil || req.Step.Config["loseResponse"] == true {
		return 6
	}
	return 0
}

// TestControllerProjectDeployHook verifies negotiation, immutable input, recovery, and executable pinning.
func TestControllerProjectDeployHook(t *testing.T) {
	dir := t.TempDir()
	installWorkflowFixture(t, dir)
	packageDir := filepath.Join(dir, "packages")
	executable := filepath.Join(packageDir, "release-fixture")
	// The fixture is installed under its own capability identity.
	if err := os.Rename(filepath.Join(packageDir, "railway"), executable); err != nil {
		t.Fatal(err)
	}
	runtime := &recordingRuntime{}
	c := newTestController(t, dir, runtime, nil)
	// Close the currently active writer, including after restart.
	t.Cleanup(func() { closeController(t, c) })
	spec := project()
	spec.Workflow = &workflow.Spec{Steps: []workflow.Step{{ID: "release", Package: "release-fixture", Hook: workflow.ProjectDeployHook, Phase: "beforeDeploy", Config: map[string]any{"loseResponse": true}}}, Bindings: map[string]string{"READY": "release.ready"}}
	// Each new proposal targets the current controller and app configuration.
	propose := func() (int, map[string]any) {
		return call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	}
	plan := loseReleaseHookResponse(t, c, propose, packageDir, dir)
	closeController(t, c)
	c = newTestController(t, dir, runtime, nil)
	reconcileLostHook(t, c, propose, plan)
	// A separate approved release may bind its outputs; recovery itself cannot replay effects.
	spec.Workflow.Steps[0].Config["loseResponse"] = false
	verifyPinnedRelease(t, c, runtime, propose, executable, filepath.Join(dir, "hook-journal"))
}

// loseReleaseHookResponse negotiates an optional hook and simulates response loss.
func loseReleaseHookResponse(t *testing.T, c *Controller, propose func() (int, map[string]any), packageDir, dir string) map[string]any {
	t.Helper()
	// Installing an executable does not implicitly enable its project hook.
	if status, response := propose(); status == 200 || code(response) != "WORKFLOW_CAPABILITY" {
		t.Fatal(status, response)
	}
	// Advertise the hook only after proving the default rejection.
	if err := os.WriteFile(filepath.Join(packageDir, "hook-enabled"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	status, plan := propose()
	// The advertised hook should now produce an approvable plan.
	if status != 200 {
		t.Fatal(status, plan)
	}
	journal := filepath.Join(dir, "hook-journal")
	// A reviewed plan alone cannot trigger an external mutation.
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("planning ran apply")
	}
	// A lost apply response leaves the hook uncertain.
	if op := resourceApply(t, c, plan, "lost"); op["status"] != "failed" {
		t.Fatal(op)
	}
	return plan
}

// reconcileLostHook inspects and accepts the recorded result using the restored controller.
func reconcileLostHook(t *testing.T, c *Controller, propose func() (int, map[string]any), plan map[string]any) {
	t.Helper()
	// A fresh release must not conceal an unresolved provider mutation.
	if status, response := propose(); status == 200 || code(response) != "WORKFLOW_CONFLICT" {
		t.Fatal("replayed uncertain release", status, response)
	}
	key := strings.Split(plan["workflow"].([]any)[0].(map[string]any)["key"].(string), "/")
	path := "/v1/workflows/sample/" + key[len(key)-1]
	status, review := call(t, c, true, "GET", path, nil, "")
	// Recovery must observe the original completed external effect.
	if status != 200 || review["status"] != "completed" {
		t.Fatal(status, review)
	}
	status, receipt := call(t, c, true, "POST", path, map[string]any{"approved": true, "fingerprint": review["fingerprint"]}, "")
	// Only approval of the inspected receipt resolves that uncertainty.
	if status != 200 {
		t.Fatal(status, receipt)
	}
}

// verifyPinnedRelease exercises an ordinary approved rollout and then swaps a reviewed executable.
func verifyPinnedRelease(t *testing.T, c *Controller, runtime *recordingRuntime, propose func() (int, map[string]any), executable, journal string) {
	t.Helper()
	// A reconciled application can request a fresh release.
	status, plan := propose()
	// A reconciled application can request a fresh release.
	if status != 200 {
		t.Fatal(status, plan)
	}
	// Confirmed package output must reach the workload environment.
	if op := resourceApply(t, c, plan, "next"); op["status"] != "succeeded" || runtime.values["READY"] != "yes" {
		t.Fatal(op)
	}
	status, plan = propose()
	// Capture a plan while the originally approved executable is still installed.
	if status != 200 {
		t.Fatal(status, plan)
	}
	// Simulate an administrator replacing the executable after planning.
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// Execution cannot silently adopt the replacement binary.
	if op := resourceApply(t, c, plan, "changed"); op["status"] != "failed" {
		t.Fatal("accepted changed executable", op)
	}
	data, err := os.ReadFile(journal)
	// Count effects to distinguish inspection from replay.
	if err != nil || string(data) != "applied\napplied\n" {
		t.Fatalf("unexpected external effects: %q %v", data, err)
	}
}
