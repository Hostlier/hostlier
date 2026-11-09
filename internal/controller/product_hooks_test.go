package controller

import (
	"hostlier/pkg/workflow"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProductHooksUseOperationKeys exercises real installed processes and persistent hook ownership.
func TestProductHooksUseOperationKeys(t *testing.T) {
	dir := t.TempDir()
	installWorkflowFixture(t, dir)
	c := newTestController(t, dir, &recordingRuntime{}, nil)
	defer closeController(t, c)
	journal := filepath.Join(dir, "hooks")
	spec := project()
	spec.Workflow = &workflow.Spec{ProductPolicy: &workflow.ProductPolicy{MaxReplicas: 1}, Steps: []workflow.Step{
		{ID: "before", Package: "aiven", Phase: "beforeDeploy", Config: map[string]any{"journal": journal}},
		{ID: "after", Package: "railway", Phase: "afterSuccess", Needs: []string{"before"}, Config: map[string]any{"journal": journal}},
	}}
	// Exercise each reviewed deployment with distinct durable operation ownership.
	for _, request := range []string{"first", "second"} {
		status, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
		// Assert the approved product contract through the real controller boundary.
		if status != 200 {
			t.Fatal(status, plan)
		}
		// Assert the approved product contract through the real controller boundary.
		if op := resourceApply(t, c, plan, request); op["status"] != "succeeded" {
			t.Fatal(op)
		}
	}
	data, err := os.ReadFile(journal)
	// Assert the approved product contract through the real controller boundary.
	if err != nil {
		t.Fatal(err)
	}
	// Assert the approved product contract through the real controller boundary.
	if strings.TrimSpace(string(data)) != "before\nafter\nbefore\nafter" {
		t.Fatalf("unexpected hook effects: %q", data)
	}
}
