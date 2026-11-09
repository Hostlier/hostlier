package controller

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkflowPrivateInputsAndNetworks keeps admin credentials within the package graph across restart.
func TestWorkflowPrivateInputsAndNetworks(t *testing.T) {
	dir := t.TempDir()
	installWorkflowFixture(t, dir)
	runtime := &recordingRuntime{}
	c := newTestController(t, dir, runtime, nil)
	_, _ = call(t, c, true, "PUT", "/v1/secrets/fused-token", map[string]any{"value": "private-fused-token", "requestId": "upload"}, "")
	spec := project()
	spec.Workflow = workflowSpec(filepath.Join(dir, "journal"))
	spec.Workflow.Steps[0].Inputs["connection"] = "instance.connection"
	spec.Workflow.Steps[0].Config["network"] = true
	spec.Workflow.Steps[1].Config["private"] = true
	_, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// A dependent package receives the private connection and emits only a scoped application URL.
	if op := resourceApply(t, c, plan, "private"); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	assertWorkflowVisibility(t, c, runtime, dir)
	closeController(t, c)
	c = newTestController(t, dir, runtime, nil)
	defer closeController(t, c)
	_, plan = call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Retained encrypted outputs must preserve both private dataflow and network membership.
	if op := resourceApply(t, c, plan, "restart"); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	assertWorkflowVisibility(t, c, runtime, dir)
	spec.Workflow.Bindings["ADMIN_URL"] = "instance.connection"
	_, plan = call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// An administrative output cannot be selected as an application environment binding.
	if op := resourceApply(t, c, plan, "leak"); op["status"] != "failed" {
		t.Fatal("private output exposed to app", op)
	}
	// Binding failure must happen before the runtime runs again.
	if runtime.calls != 2 {
		t.Fatal("runtime ran with invalid private binding")
	}
}

// assertWorkflowVisibility checks durable encryption, password redaction and runtime attachment.
func assertWorkflowVisibility(t *testing.T, c *Controller, runtime *recordingRuntime, dir string) {
	t.Helper()
	// Packages must attach their local resource network before application health checks.
	if len(runtime.networks) != 1 || runtime.networks[0] != "hostlier-0123456789abcdef-resource-db" {
		t.Fatal("missing workflow resource network")
	}
	// Only explicitly declared public outputs enter the application environment.
	if len(runtime.values) != 1 || runtime.values["DATABASE_URL"] == "" {
		t.Fatal("unexpected application bindings")
	}
	data := controllerSnapshot(t, c)
	// Administrative values must never appear in plaintext persistent state.
	if strings.Contains(string(data), "private-admin-password") {
		t.Fatal("administrative output was not encrypted")
	}
	c.mu.Lock()
	redacted := c.redact("private-admin-password")
	c.mu.Unlock()
	// Logs must mask passwords extracted from private administrative connection URLs.
	if redacted != "[REDACTED]" {
		t.Fatal("administrative output was not redacted")
	}
}
