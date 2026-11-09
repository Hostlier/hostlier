package workflow

import (
	"encoding/json"
	"testing"
)

// TestInspectionValidation ensures uncertainty cannot accidentally publish unverified private state.
func TestInspectionValidation(t *testing.T) {
	result := &Result{State: json.RawMessage(`{"owned":true}`), Outputs: map[string]string{"url": "private"}}
	// Each allowed status has exactly one result publication rule.
	for _, test := range []struct {
		observation Inspection
		valid       bool
	}{{Inspection{Status: Completed, Result: result}, true}, {Inspection{Status: NotStarted}, true}, {Inspection{Status: Uncertain}, true}, {Inspection{Status: Completed}, false}, {Inspection{Status: NotStarted, Result: result}, false}, {Inspection{Status: Uncertain, Result: result}, false}, {Inspection{Status: "unsupported"}, false}} {
		// Protocol validation must reject all malformed publication combinations.
		if test.observation.Valid() != test.valid {
			t.Fatal(test.observation)
		}
	}
	result.State = nil
	// Even completed observations need valid encrypted durable state at the controller boundary.
	if (Inspection{Status: Completed, Result: result}).Valid() {
		t.Fatal("missing durable state accepted")
	}
}
