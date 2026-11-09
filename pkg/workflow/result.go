package workflow

import (
	"encoding/json"
	"regexp"
)

var networkName = regexp.MustCompile(`^hostlier-[a-f0-9]{16}-resource-[a-z][a-z0-9-]{0,62}$`)

// ValidResult checks public bindings, private step inputs and owned network references.
func ValidResult(result Result) bool {
	// Every success needs durable state and at least one application-safe output.
	if !json.Valid(result.State) || !ValidOutput(result.Outputs) || !validPrivateOutputs(result) {
		return false
	}
	// Each workflow can attach only a bounded set of resource networks.
	if len(result.Networks) > 32 {
		return false
	}
	// The runtime additionally verifies that each network belongs to its controller.
	for _, network := range result.Networks {
		// Reject paths, external networks and unbounded identifiers before persistence.
		if !networkName.MatchString(network) {
			return false
		}
	}
	return true
}

// validPrivateOutputs prevents ambiguous output references across the two visibility classes.
func validPrivateOutputs(result Result) bool {
	// Packages without administrative outputs remain protocol-compatible.
	if len(result.PrivateOutputs) == 0 {
		return true
	}
	// Administrative fields have the same bounded string contract as other fields.
	if !ValidOutput(result.PrivateOutputs) {
		return false
	}
	// A field cannot change visibility depending on the consumer.
	for name := range result.PrivateOutputs {
		// Overlapping aliases would make downstream references ambiguous.
		if _, exists := result.Outputs[name]; exists {
			return false
		}
	}
	return true
}

// StepOutputs exposes administrative values only to explicitly dependent package steps.
func (result Result) StepOutputs() map[string]string {
	values := map[string]string{}
	// Application-safe outputs are also valid step inputs.
	for name, value := range result.Outputs {
		values[name] = value
	}
	// Private fields are never included in the application's binding lookup.
	for name, value := range result.PrivateOutputs {
		values[name] = value
	}
	return values
}
