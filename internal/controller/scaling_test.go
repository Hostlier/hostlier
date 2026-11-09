package controller

import (
	"hostlier/internal/model"
	"testing"
)

// TestScalePinsSecretsAndPreservesApplication checks the full approval path with rotated source credentials.
func TestScalePinsSecretsAndPreservesApplication(t *testing.T) {
	runtime := &recordingRuntime{}
	c := newTestController(t, t.TempDir(), runtime, nil)
	defer closeController(t, c)
	spec := project()
	spec.Secrets = map[string]string{"KEY": "app-key"}
	_, _ = call(t, c, true, "PUT", "/v1/secrets/app-key", map[string]any{"value": "original-private-value", "requestId": "first"}, "")
	_, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Establish an approved deployment with a pinned credential revision.
	if op := resourceApply(t, c, plan, "first"); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	_, _ = call(t, c, true, "PUT", "/v1/secrets/app-key", map[string]any{"value": "rotated-private-value", "requestId": "second"}, "")
	spec = c.state.Applications["sample"].Spec
	spec.Resources = &model.Limits{CPU: "2", Memory: "256Mi"}
	status, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec, "action": "scale"}, "")
	// Scaling retains the deployed revision rather than silently rotating its secret input.
	if status != 200 {
		t.Fatal(status, plan)
	}
	// The same ordinary deployment approval and execution applies to resource-only changes.
	if op := resourceApply(t, c, plan, "scale"); op["status"] != "succeeded" || runtime.values["KEY"] != "original-private-value" {
		t.Fatal("scale changed the deployed secret revision", op)
	}
	zero := 0
	spec.Replicas = &zero
	_, plan = call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec, "action": "scale"}, "")
	// Zero desired capacity is explicitly stopped in durable controller application status.
	if op := resourceApply(t, c, plan, "zero"); op["status"] != "succeeded" || c.state.Applications["sample"].Status != "stopped" {
		t.Fatal("scale-to-zero status incorrect", op)
	}
}

// TestScaleRejectsUnsupportedChangesBeforeMutation enforces runtime and resource-only boundaries.
func TestScaleRejectsUnsupportedChangesBeforeMutation(t *testing.T) {
	runtime := &recordingRuntime{}
	c := newTestController(t, t.TempDir(), runtime, nil)
	defer closeController(t, c)
	spec := project()
	_, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Existing application state is required before scale can patch an owned deployment.
	if op := resourceApply(t, c, plan, "first"); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	spec = c.state.Applications["sample"].Spec
	spec.Route = "different.example.com"
	status, denied := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec, "action": "scale"}, "")
	// Route replacement cannot be concealed inside resource-only scaling approval.
	if status == 200 || code(denied) != "INVALID_SCALE" {
		t.Fatal(status, denied)
	}
	spec = c.state.Applications["sample"].Spec
	three := 3
	spec.Replicas = &three
	status, denied = call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec, "action": "scale"}, "")
	// Docker cannot accept multiple replicas or perform side effects for an invalid request.
	if status == 200 || code(denied) != "UNSUPPORTED_REPLICAS" || runtime.calls != 1 {
		t.Fatal(status, denied)
	}
}
