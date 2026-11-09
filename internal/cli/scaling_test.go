package cli

import (
	"hostlier/internal/model"
	"testing"
)

// TestScaledProjectPreservesOmissions checks desired-state patching and prevents source pointer mutation.
func TestScaledProjectPreservesOmissions(t *testing.T) {
	three := 3
	original := model.Project{Version: 1, Name: "app", Image: "nginx:alpine", Port: 80, Route: "app.example.com", Replicas: &three, Resources: &model.Limits{CPU: "1", Memory: "64Mi"}}
	changed, err := scaledProject(original, map[string]string{"cpu": "2"})
	// CPU-only edits preserve current memory and replica intent.
	if err != nil || changed.Resources.Memory != "64Mi" || model.DesiredReplicas(changed) != 3 {
		t.Fatalf("%+v %v", changed, err)
	}
	// Patching a copy must leave the fetched server specification unchanged.
	if original.Resources.CPU != "1" {
		t.Fatal("source limits mutated")
	}
	changed, err = scaledProject(original, map[string]string{"replicas": "0"})
	// Explicit zero must remain distinguishable from an omitted default.
	if err != nil || changed.Replicas == nil || model.DesiredReplicas(changed) != 0 {
		t.Fatal(changed, err)
	}
}

// TestScaledProjectRejectsInvalidCounts checks integer and nonnegative replica constraints.
func TestScaledProjectRejectsInvalidCounts(t *testing.T) {
	spec := model.Project{Version: 1, Name: "app", Image: "nginx:alpine", Port: 80}
	// Both malformed and negative desired counts must fail before planning.
	for _, value := range []string{"-1", "1.5", "no"} {
		// Invalid scaling inputs cannot produce a deployable desired specification.
		if _, err := scaledProject(spec, map[string]string{"replicas": value}); err == nil {
			t.Fatal("accepted", value)
		}
	}
}
