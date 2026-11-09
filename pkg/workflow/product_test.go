package workflow

import "testing"

// TestProductHookOrdering rejects startup edges and bindings to effects that run after readiness.
func TestProductHookOrdering(t *testing.T) {
	spec := Spec{Steps: []Step{{ID: "after", Package: "example", Phase: "afterSuccess"}, {ID: "before", Package: "example", Needs: []string{"after"}}}}
	// Assert the approved product contract through the real controller boundary.
	if spec.Validate() == nil {
		t.Fatal("startup depends on post-readiness hook")
	}
	spec.Steps = spec.Steps[:1]
	spec.Bindings = map[string]string{"KEY": "after.value"}
	// Assert the approved product contract through the real controller boundary.
	if spec.Validate() == nil {
		t.Fatal("startup binds post-readiness hook")
	}
	spec.Bindings = nil
	// Assert the approved product contract through the real controller boundary.
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	spec.Steps[0].Phase = "shell"
	// Assert the approved product contract through the real controller boundary.
	if spec.Validate() == nil {
		t.Fatal("unknown phase accepted")
	}
}
