package controller

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/pkg/workflow"
)

// TestMain also supplies a real subprocess fixture for the executable package protocol.
func TestMain(m *testing.M) {
	// Package child processes handle just one protocol command.
	if len(os.Args) == 2 {
		// Keep capability discovery separate from resource and deploy execution.
		switch os.Args[1] {
		// Discovery publishes metadata without consuming private stdin.
		case "describe":
			os.Exit(describeReleaseFixture())
		// Deployment receives the controller-pinned project.
		case workflow.ProjectDeployHook:
			os.Exit(projectDeployFixture())
		// Ordinary providers continue to receive their independent resource contract.
		case "serve":
			os.Exit(workflowFixture())
		}
	}
	os.Exit(m.Run())
}

// workflowFixture mimics a private provisioning package without sockets, Docker or cloud accounts.
func workflowFixture() int {
	var req workflow.Request
	// Decode exactly the runner's private request and fail without output on malformed input.
	if json.NewDecoder(os.Stdin).Decode(&req) != nil {
		return 2
	}
	response := workflow.Response{Version: 1}
	// Planning reports secret-bearing private payloads to exercise controller encryption and redaction.
	if req.Action == "plan" {
		payload, _ := json.Marshal(map[string]string{"private": req.Secrets["executionToken"]})
		response.Prepared = &workflow.Prepared{Summary: []string{"Provision " + req.Step.ID, "credential " + req.Secrets["executionToken"]}, Payload: payload}
		json.NewEncoder(os.Stdout).Encode(response)
		return 0
	}
	// Trusted ownership context must come from the controller for every invocation.
	if req.Host == nil || req.Host.Runtime != "docker" || req.Host.StateDir == "" {
		return 6
	}
	// Recovery inspects external evidence without executing another provisioning mutation.
	if req.Action == "inspect" {
		return workflowFixtureInspect(req, response)
	}
	return workflowFixtureApply(req, response)
}

// workflowFixtureApply records actual process execution and returns deterministic private outputs.
func workflowFixtureApply(req workflow.Request, response workflow.Response) int {
	path, _ := req.Step.Config["journal"].(string)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	// The test fixture never writes outside its explicitly supplied temporary journal.
	if err != nil {
		return 3
	}
	fmt.Fprintln(file, req.Step.ID)
	file.Close()
	// Simulate an uncertain provider mutation whose response is lost.
	if req.Step.Config["fail"] == true {
		fmt.Fprintln(os.Stderr, "private-provider-diagnostic")
		return 4
	}
	// Downstream private outputs must arrive only after their producer has completed.
	if len(req.Step.Inputs) > 0 && req.Inputs["connection"] == "" {
		return 5
	}
	state, _ := json.Marshal(map[string]string{"owned": req.Key})
	response.Result = &workflow.Result{State: state, Outputs: map[string]string{"url": "postgres://app:generated-private-password@db.example/" + req.Step.ID}}
	// The controller must refuse deployment if a declared binding cannot be satisfied.
	if req.Step.Config["missing"] == true {
		response.Result.Outputs = map[string]string{"different": "private"}
	}
	applyWorkflowFixtureVisibility(req, response.Result)
	json.NewEncoder(os.Stdout).Encode(response)
	return 0
}

// applyWorkflowFixtureVisibility exercises administrative output and local network channels.
func applyWorkflowFixtureVisibility(req workflow.Request, result *workflow.Result) {
	// Provider administrative connections may flow only into another package.
	if req.Step.Config["private"] == true {
		result.PrivateOutputs = map[string]string{"connection": "postgres://admin:private-admin-password@db.example/postgres"}
	}
	// Optional local resource networks must reach the application runtime.
	if req.Step.Config["network"] == true {
		result.Networks = []string{"hostlier-0123456789abcdef-resource-db"}
	}
}

// installWorkflowFixture places the current test executable in a temporary administrator package directory.
func installWorkflowFixture(t *testing.T, stateDir string) {
	t.Helper()
	dir := filepath.Join(stateDir, "packages")
	// Every fixture installation is isolated from the user's controller and provider packages.
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path, err := os.Executable()
	// The test executable includes the exact protocol helper used by child processes.
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	// Preserve executable bytes for digest pinning and process tests.
	if err != nil {
		t.Fatal(err)
	}
	// Both provider names use the same fixture implementation with distinct identities.
	for _, provider := range []string{"aiven", "railway"} {
		// Package installation requires private executable permissions.
		if err := os.WriteFile(filepath.Join(dir, provider), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
}

// workflowSpec includes deliberately reversed declaration order to exercise graph ordering.
func workflowSpec(journal string) *workflow.Spec {
	return &workflow.Spec{Steps: []workflow.Step{
		{ID: "database", Package: "railway", Needs: []string{"instance"}, Inputs: map[string]string{"connection": "instance.url"}, Secrets: map[string]string{"executionToken": "fused-token"}, Config: map[string]any{"journal": journal}},
		{ID: "instance", Package: "aiven", Secrets: map[string]string{"executionToken": "fused-token"}, Config: map[string]any{"journal": journal}},
	}, Bindings: map[string]string{"DATABASE_URL": "database.url"}}
}

// TestWorkflowLocalE2E executes real package subprocesses through approval, encrypted persistence and app binding.
func TestWorkflowLocalE2E(t *testing.T) {
	dir := t.TempDir()
	installWorkflowFixture(t, dir)
	runtime := &recordingRuntime{}
	c := newTestController(t, dir, runtime, nil)
	_, _ = call(t, c, true, "PUT", "/v1/secrets/fused-token", map[string]any{"value": "private-fused-token", "requestId": "upload"}, "")
	spec := project()
	journal := filepath.Join(dir, "journal")
	spec.Workflow = workflowSpec(journal)
	status, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Planning is side-effect-free even though it invokes real package executables.
	if status != 200 {
		t.Fatal(status, plan)
	}
	assertWorkflowPrivate(t, c, dir, plan)
	// Provider mutations must not happen before explicit approval.
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("planning provisioned resources")
	}
	status, _ = call(t, c, true, "POST", "/v1/plans/"+plan["id"].(string)+"/apply", map[string]any{"approved": false, "fingerprint": plan["fingerprint"], "requestId": "denied"}, "")
	// The existing approval boundary covers package work as well as app deployment.
	if status == 200 {
		t.Fatal("unapproved workflow accepted")
	}
	op := resourceApply(t, c, plan, "approved")
	// The runtime must receive only the explicitly selected application binding.
	if op["status"] != "succeeded" || runtime.values["DATABASE_URL"] == "" || runtime.values["executionToken"] != "" {
		t.Fatal(op, "missing or leaked binding")
	}
	assertWorkflowPrivate(t, c, dir, op)
	closeController(t, c)
	c = newTestController(t, dir, runtime, nil)
	defer closeController(t, c)
	_, plan = call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// A second approved deployment reuses encrypted successful outputs across a restart.
	if op := resourceApply(t, c, plan, "redeploy"); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	raw, _ := os.ReadFile(journal)
	// Each external mutation runs once and dependency order determines execution order.
	if string(raw) != "instance\ndatabase\n" {
		t.Fatalf("unexpected package execution order: %q", raw)
	}
	status, _ = call(t, c, true, "DELETE", "/v1/secrets/fused-token", nil, "")
	// Retained package resources preserve the credentials needed for inspection.
	if status == 200 {
		t.Fatal("retained package credential deleted")
	}
}

// assertWorkflowPrivate checks public output and durable state for both source and generated credentials.
func assertWorkflowPrivate(t *testing.T, c *Controller, dir string, public any) {
	t.Helper()
	raw := controllerSnapshot(t, c)
	encoded, _ := json.Marshal(public)
	// Private execution inputs and outputs must not appear outside authenticated ciphertext.
	for _, value := range []string{"private-fused-token", "generated-private-password", "private-provider-diagnostic"} {
		// Cover both public API exposure and persistence leaks.
		if strings.Contains(string(raw), value) || strings.Contains(string(encoded), value) {
			t.Fatal("workflow secret exposed")
		}
	}
	c.mu.Lock()
	redacted := c.redact("generated-private-password")
	hasResults := len(c.state.WorkflowRecords) > 0
	c.mu.Unlock()
	// Runtime diagnostics must redact password fragments after provisioning.
	if hasResults && redacted != "[REDACTED]" {
		t.Fatal("package password not redacted")
	}
}

// TestWorkflowFailureStopsDeploymentAndReplay verifies durable uncertain-state handling after external mutation.
func TestWorkflowFailureStopsDeploymentAndReplay(t *testing.T) {
	dir := t.TempDir()
	installWorkflowFixture(t, dir)
	runtime := &recordingRuntime{}
	c := newTestController(t, dir, runtime, nil)
	_, _ = call(t, c, true, "PUT", "/v1/secrets/fused-token", map[string]any{"value": "private-fused-token", "requestId": "upload"}, "")
	spec := project()
	journal := filepath.Join(dir, "journal")
	spec.Workflow = workflowSpec(journal)
	spec.Workflow.Steps[0].Config["fail"] = true
	_, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	op := resourceApply(t, c, plan, "failed")
	// A failed package prevents application deployment while retaining its completed predecessor.
	if op["status"] != "failed" || runtime.calls != 0 {
		t.Fatal("deployment ran after failed provisioning", op)
	}
	closeController(t, c)
	c = newTestController(t, dir, runtime, nil)
	defer closeController(t, c)
	status, out := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// A controller restart cannot blindly repeat uncertain billable side effects.
	if status == 200 || code(out) != "WORKFLOW_UNCERTAIN" {
		t.Fatal(status, out)
	}
	raw, _ := os.ReadFile(journal)
	// The failed operation is recorded once, with no implicit retry.
	if string(raw) != "instance\ndatabase\n" {
		t.Fatal("uncertain mutation repeated")
	}
}

// TestWorkflowExecutableChangeRejectsApply verifies the package digest is part of the approval boundary.
func TestWorkflowExecutableChangeRejectsApply(t *testing.T) {
	dir := t.TempDir()
	installWorkflowFixture(t, dir)
	c := newTestController(t, dir, &recordingRuntime{}, nil)
	defer closeController(t, c)
	_, _ = call(t, c, true, "PUT", "/v1/secrets/fused-token", map[string]any{"value": "private-fused-token", "requestId": "upload"}, "")
	spec := project()
	journal := filepath.Join(dir, "journal")
	spec.Workflow = workflowSpec(journal)
	_, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	file, err := os.OpenFile(filepath.Join(dir, "packages", "aiven"), os.O_APPEND|os.O_WRONLY, 0)
	// Simulate an administrator upgrading the package after a deployment was reviewed.
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(file, "changed")
	file.Close()
	// A changed executable must not run against the old approval.
	if op := resourceApply(t, c, plan, "changed"); op["status"] != "failed" {
		t.Fatal(op)
	}
	// Digest verification must precede child process execution.
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("changed package was executed")
	}
}
