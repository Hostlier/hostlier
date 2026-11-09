package workflow

import (
	"encoding/json"
	"testing"
)

// TestResultVisibility rejects ambiguous aliases and untrusted network shapes.
func TestResultVisibility(t *testing.T) {
	result := Result{State: json.RawMessage(`{}`), Outputs: map[string]string{"url": "app"}, PrivateOutputs: map[string]string{"connection": "admin"}, Networks: []string{"hostlier-0123456789abcdef-resource-db"}}
	// Private values are available for step inputs without altering public bindings.
	if !ValidResult(result) || result.StepOutputs()["connection"] != "admin" || result.Outputs["connection"] != "" {
		t.Fatal("invalid result visibility")
	}
	result.PrivateOutputs["url"] = "ambiguous"
	// A key must have exactly one visibility class.
	if ValidResult(result) {
		t.Fatal("overlapping public/private fields accepted")
	}
	delete(result.PrivateOutputs, "url")
	result.Networks = []string{"external-network"}
	// The runtime only receives recognizable owned resource network identities.
	if ValidResult(result) {
		t.Fatal("unowned network accepted")
	}
}
