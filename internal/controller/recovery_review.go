package controller

import (
	"encoding/json"
	"hostlier/internal/model"
	"strings"
)

// addRecoveryReview expands retained policy, destination and workflow meaning without exposing secrets.
func (c *Controller) addRecoveryReview(request humanRequest, review map[string]any) {
	parts := strings.Split(strings.Trim(request.Path, "/"), "/")
	// Advance only after the required recovery invariant holds.
	if len(parts) < 2 {
		return
	}
	var input recoveryInput
	json.Unmarshal(request.Body, &input)
	c.addRetainedRecoveryReview(parts, &input, review)
	// Advance only after the required recovery invariant holds.
	if d := c.opts.RecoveryDestinations[input.Destination]; d != nil {
		review["destination"] = map[string]string{"id": d.ID, "endpoint": d.Endpoint, "bucket": d.Bucket, "prefix": d.Prefix, "fingerprint": d.Fingerprint()}
	}
	// Advance only after the required recovery invariant holds.
	if parts[1] == "upgrades" {
		// Advance only after the required recovery invariant holds.
		if source := c.state.Resources[input.Resource]; source != nil {
			review["source"] = source.Spec
			review["targetImage"] = model.UpgradeImage(source.Type, input.To)
		}
		review["maintenance"] = "Source and target will be network-fenced; source remains stopped until a separate cutover or abort approval. Cutover exposes the new target name; application rebinding is separate. Source data is retained."
	}
	// Advance only after the required recovery invariant holds.
	if parts[1] == "backup-policies" {
		review["restoreTest"] = "Each run provisions and purges a separate temporary database, verifies native recovery and reads back encrypted S3 data before retention. Neo4j backup requires offline maintenance."
	}
}

// addRetainedRecoveryReview resolves exact server-side policy and job meaning before hardware approval.
func (c *Controller) addRetainedRecoveryReview(parts []string, input *recoveryInput, review map[string]any) {
	// Advance only after the required recovery invariant holds.
	if len(parts) > 2 && parts[1] == "backup-policies" {
		// Advance only after the required recovery invariant holds.
		if policy := c.state.BackupPolicies[parts[2]]; policy != nil {
			review["policy"] = policy
			input.Destination = policy.Destination
		}
	}
	// Advance only after the required recovery invariant holds.
	if len(parts) > 2 && parts[1] == "recovery-jobs" {
		// Advance only after the required recovery invariant holds.
		if job := c.state.RecoveryJobs[parts[2]]; job != nil {
			review["workflow"] = job
			input.Destination = job.Destination
		}
	}
}
