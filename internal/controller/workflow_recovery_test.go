package controller

import (
	"encoding/json"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
	"os"
	"path/filepath"
	"testing"
)

// workflowFixtureInspect models a read-only provider lookup following a lost mutation response.
func workflowFixtureInspect(req workflow.Request, response workflow.Response) int {
	path, _ := req.Step.Config["evidence"].(string)
	evidence, err := os.ReadFile(path)
	// Missing evidence cannot establish absence or completion of an external mutation.
	if err != nil {
		return 7
	}
	var prepared map[string]string
	// Inspect must receive exactly the protected original credentials and predecessor binding.
	if json.Unmarshal(req.Prepared, &prepared) != nil || prepared["private"] != "private-fused-token" || req.Inputs["connection"] == "" {
		return 8
	}
	response.Inspection = &workflow.Inspection{Status: workflow.InspectStatus(evidence)}
	// Independent evidence of completion can recover the lost private result.
	if response.Inspection.Status == workflow.Completed {
		state, _ := json.Marshal(map[string]string{"owned": req.Key})
		response.Inspection.Result = &workflow.Result{State: state, Outputs: map[string]string{"url": "postgres://app:generated-private-password@db.example/" + req.Step.ID}}
	}
	json.NewEncoder(os.Stdout).Encode(response)
	return 0
}

// failedRecoveryFixture creates real subprocess effects with a lost response and durable uncertain state.
func failedRecoveryFixture(t *testing.T) (*Controller, *recordingRuntime, model.Project, string, string) {
	t.Helper()
	dir := t.TempDir()
	installWorkflowFixture(t, dir)
	runtime := &recordingRuntime{}
	c := newTestController(t, dir, runtime, nil)
	_, _ = call(t, c, true, "PUT", "/v1/secrets/fused-token", map[string]any{"value": "private-fused-token", "requestId": "upload"}, "")
	spec := project()
	journal, evidence := filepath.Join(dir, "journal"), filepath.Join(dir, "evidence")
	spec.Workflow = workflowSpec(journal)
	spec.Workflow.Steps[0].Config["fail"] = true
	spec.Workflow.Steps[0].Config["evidence"] = evidence
	setRecoveryEvidence(t, evidence, workflow.Completed)
	status, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Accepted provisioning must fail before app deployment while retaining its predecessor.
	if status != 200 || resourceApply(t, c, plan, "lost-response")["status"] != "failed" {
		t.Fatal("failed to establish uncertain workflow", plan)
	}
	closeController(t, c)
	c = newTestController(t, dir, runtime, nil)
	// Every test leaves its restarted writer closed for temporary-directory cleanup.
	t.Cleanup(func() { closeController(t, c) })
	return c, runtime, spec, journal, evidence
}

// setRecoveryEvidence changes independently observed remote state without editing controller records.
func setRecoveryEvidence(t *testing.T, path string, status workflow.InspectStatus) {
	t.Helper()
	// Only the fixture's external evidence file may establish the observed result.
	if err := os.WriteFile(path, []byte(status), 0600); err != nil {
		t.Fatal(err)
	}
}

// recoveryPreview obtains safe inspection metadata through the authenticated controller boundary.
func recoveryPreview(t *testing.T, c *Controller, expected workflow.InspectStatus) map[string]any {
	t.Helper()
	status, review := call(t, c, true, "GET", "/v1/workflows/sample/database", nil, "")
	// Neither successful inspection nor its response may contain private result values.
	if status != 200 || review["status"] != string(expected) {
		t.Fatal(status, review)
	}
	assertWorkflowPrivate(t, c, c.opts.StateDir, review)
	return review
}

// reconcileRecovery submits the precise evidence fingerprint shown by the preceding review.
func reconcileRecovery(t *testing.T, c *Controller, review map[string]any, approved bool) (int, map[string]any) {
	t.Helper()
	return call(t, c, true, "POST", "/v1/workflows/sample/database", map[string]any{"approved": approved, "fingerprint": review["fingerprint"]}, "")
}

// TestWorkflowRecoveryProcessE2E restores lost subprocess results after restart without replaying effects.
func TestWorkflowRecoveryProcessE2E(t *testing.T) {
	c, runtime, spec, journal, _ := failedRecoveryFixture(t)
	review := recoveryPreview(t, c, workflow.Completed)
	status, denied := reconcileRecovery(t, c, review, false)
	// Read-only inspection and rejected approval leave external effects unchanged.
	if status == 200 || code(denied) != "APPROVAL_REQUIRED" {
		t.Fatal(status, denied)
	}
	status, recovered := reconcileRecovery(t, c, review, true)
	// Explicit approval records verified encrypted results, not another apply request.
	if status != 200 {
		t.Fatal(status, recovered)
	}
	_, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// A new approved deployment consumes recovered output and the successful predecessor.
	if op := resourceApply(t, c, plan, "after-recovery"); op["status"] != "succeeded" || runtime.values["DATABASE_URL"] == "" {
		t.Fatal(op)
	}
	raw, err := os.ReadFile(journal)
	// Both external effects occurred exactly once despite response loss and controller restart.
	if err != nil || string(raw) != "instance\ndatabase\n" {
		t.Fatal("recovery replayed external work", string(raw), err)
	}
	assertWorkflowPrivate(t, c, c.opts.StateDir, recovered)
}

// TestWorkflowRecoveryEvidenceChangesRequireNewApproval refuses changed or inconclusive observations.
func TestWorkflowRecoveryEvidenceChangesRequireNewApproval(t *testing.T) {
	c, _, _, _, evidence := failedRecoveryFixture(t)
	review := recoveryPreview(t, c, workflow.Completed)
	setRecoveryEvidence(t, evidence, workflow.Uncertain)
	status, denied := reconcileRecovery(t, c, review, true)
	// A previously reviewed completed outcome cannot override a newer uncertain observation.
	if status == 200 || code(denied) != "APPROVAL_REQUIRED" {
		t.Fatal(status, denied)
	}
	review = recoveryPreview(t, c, workflow.Uncertain)
	status, denied = reconcileRecovery(t, c, review, true)
	// Even an explicitly approved uncertain outcome remains blocked.
	if status == 200 || code(denied) != "WORKFLOW_UNCERTAIN" {
		t.Fatal(status, denied)
	}
	setRecoveryEvidence(t, evidence, workflow.NotStarted)
	review = recoveryPreview(t, c, workflow.NotStarted)
	status, result := reconcileRecovery(t, c, review, true)
	// Independently verified absence clears only the uncertain reservation for later planning.
	if status != 200 || c.state.WorkflowRecords[c.state.WorkflowNamespace+"/sample/database"] != nil {
		t.Fatal(status, result)
	}
}

// TestWorkflowRecoveryPinsAttemptCredentials rejects ambiguous historical approvals with identical configurations.
func TestWorkflowRecoveryPinsAttemptCredentials(t *testing.T) {
	step := workflow.Step{ID: "db", Package: "postgres"}
	old := WorkflowPlan{ID: "old", Key: "owner/app/db", Step: step}
	current := old
	current.ID = "current"
	c := &Controller{state: State{Operations: map[string]*Operation{"old": {Workflow: []WorkflowPlan{old}}, "current": {Workflow: []WorkflowPlan{current}}}}}
	record := &WorkflowRecord{SpecHash: config.Fingerprint(step), PlanID: "current"}
	found, err := c.originalWorkflowPlan(current.Key, record)
	// A later attempt uses its own prepared credential identity despite identical desired input.
	if err != nil || found.ID != "current" {
		t.Fatal(found, err)
	}
	record.PlanID = ""
	_, err = c.originalWorkflowPlan(current.Key, record)
	// Legacy state with two possible attempts must not choose by map iteration order.
	if err == nil {
		t.Fatal("ambiguous legacy approval was guessed")
	}
}
