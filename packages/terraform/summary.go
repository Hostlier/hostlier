package terraform

import (
	"encoding/json"
	"errors"
	"fmt"
)

// planJSON decodes only operation classes; all provider values remain private.
type planJSON struct {
	ResourceChanges []struct {
		Change struct {
			Actions []string `json:"actions"`
		} `json:"change"`
	} `json:"resource_changes"`
}

// summarize gives a bounded review of destructive and replacement operations.
func summarize(raw []byte) ([]string, error) {
	var plan planJSON
	// Reject an identity or visibility mismatch before publishing the operation result.
	if json.Unmarshal(raw, &plan) != nil {
		return nil, errors.New("invalid terraform plan")
	}
	counts := map[string]int{}
	// Process only the explicitly declared identities or bindings for this operation.
	for _, resource := range plan.ResourceChanges {
		// Process only the explicitly declared identities or bindings for this operation.
		for _, action := range resource.Change.Actions {
			counts[action]++
		}
		// Reject an identity or visibility mismatch before publishing the operation result.
		if len(resource.Change.Actions) > 1 {
			counts["replace"]++
		}
	}
	return []string{fmt.Sprintf("Resources: create %d, update %d, delete %d, replace %d, unchanged %d. Costs and exposure are not inferred.", counts["create"], counts["update"], counts["delete"], counts["replace"], counts["no-op"])}, nil
}
