package workflow

import "testing"

// TestGraphValidation verifies invalid dependency graphs cannot reach provisioning.
func TestGraphValidation(t *testing.T) {
	cases := []Spec{
		{},
		{Steps: []Step{{ID: "one", Package: "../escape"}}},
		{Steps: []Step{{ID: "one", Package: "aiven"}, {ID: "one", Package: "railway"}}},
		{Steps: []Step{{ID: "one", Package: "aiven", Needs: []string{"missing"}}}},
		{Steps: []Step{{ID: "one", Package: "aiven", Needs: []string{"two"}}, {ID: "two", Package: "railway", Needs: []string{"one"}}}},
		{Steps: []Step{{ID: "one", Package: "aiven"}, {ID: "two", Package: "railway", Inputs: map[string]string{"connection": "one.url"}}}},
		{Steps: []Step{{ID: "one", Package: "aiven"}}, Bindings: map[string]string{"DATABASE_URL": "unknown.url"}},
	}
	// Every malformed graph is rejected without evaluating a package.
	for i, spec := range cases {
		// Validation must fail for missing references, duplicate identities and cycles alike.
		if spec.Validate() == nil {
			t.Fatalf("invalid graph %d accepted", i)
		}
	}
}

// TestStableOrder demonstrates explicit dependency order wins over declaration order.
func TestStableOrder(t *testing.T) {
	spec := Spec{Steps: []Step{
		{ID: "db", Package: "aiven", Needs: []string{"instance"}, Inputs: map[string]string{"connection": "instance.url"}},
		{ID: "instance", Package: "aiven"},
	}, Bindings: map[string]string{"DATABASE_URL": "db.url"}}
	// Well-formed graphs should validate without package-specific execution.
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	steps, err := spec.Order()
	// Consumers must run after their producers.
	if err != nil || steps[0].ID != "instance" || steps[1].ID != "db" {
		t.Fatal(steps, err)
	}
}
