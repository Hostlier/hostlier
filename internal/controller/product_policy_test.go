package controller

import (
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
	"testing"
)

// TestProductPolicySurvivesRestartAndOmission proves the direct API cannot shed captured bounds.
func TestProductPolicySurvivesRestartAndOmission(t *testing.T) {
	dir := t.TempDir()
	c := newTestController(t, dir, &recordingRuntime{}, nil)
	spec := project()
	spec.Resources = &model.Limits{CPU: "1", Memory: "128Mi"}
	c.state.Applications[spec.Name] = &Application{Spec: spec, ProductPolicy: &workflow.ProductPolicy{MaxReplicas: 1, MaxCPU: 1, MaxMemoryBytes: 134217728}}
	// Assert the approved product contract through the real controller boundary.
	if err := c.store.Save(c.state); err != nil {
		t.Fatal(err)
	}
	closeController(t, c)
	c = newTestController(t, dir, &recordingRuntime{}, nil)
	defer closeController(t, c)
	// An ordinary deployment without a workflow still carries the original app policy.
	zero := 0
	spec.Replicas = &zero
	status, denied := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Assert the approved product contract through the real controller boundary.
	if status == 200 || code(denied) != "PRODUCT_POLICY" {
		t.Fatal(status, denied)
	}
	// The scale route receives the same retained policy checks as ordinary deployment.
	status, denied = call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec, "action": "scale"}, "")
	// A separate direct API action cannot remove server-side bounds.
	if status == 200 || code(denied) != "PRODUCT_POLICY" {
		t.Fatal(status, denied)
	}
	spec.Replicas = nil
	spec.SleepAfter = "1m"
	// An idle policy also needs authority for its lower bound of zero.
	if err := c.validateProductPolicy(spec); err == nil {
		t.Fatal("idle policy bypassed retained zero restriction")
	}
	spec.SleepAfter = ""
	spec.Resources.CPU = "2"
	// Assert the approved product contract through the real controller boundary.
	if err := c.validateProductPolicy(spec); err == nil {
		t.Fatal("CPU policy bypassed after restart")
	}
	spec.Resources.CPU = "1"
	// Assert the approved product contract through the real controller boundary.
	if err := c.validateProductPolicy(spec); err != nil {
		t.Fatal(err)
	}
}

// TestProductPolicyValidatesResolvedRollback prevents an old revision escaping current bounds.
func TestProductPolicyValidatesResolvedRollback(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	current := project()
	old := current
	zero := 0
	old.Replicas = &zero
	c.state.Applications[current.Name] = &Application{Spec: current, ProductPolicy: &workflow.ProductPolicy{MaxReplicas: 1}, Revisions: []Revision{{ID: "old", Spec: old}}}
	status, denied := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": current, "action": "rollback", "revision": "old"}, "")
	// Validation must use the resolved revision rather than only the caller's permitted current spec.
	if status == 200 || code(denied) != "PRODUCT_POLICY" {
		t.Fatal(status, denied)
	}
}
