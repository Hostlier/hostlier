package controller

import (
	"context"
	"encoding/json"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
)

// packageDiscoverer exposes lifecycle capabilities of administrator-installed executables.
type packageDiscoverer interface {
	Describe(context.Context, string) (workflow.Capability, error)
}

// prepareManagedInputs restricts same-owner updates to capable packages with fully reviewed inputs.
func (c *Controller) prepareManagedInputs(ctx context.Context, plan WorkflowPlan, predecessors []WorkflowPlan) (map[string]string, error) {
	// Existing retained dependencies preserve their deferred-input protocol.
	if plan.Step.Lifecycle != "managed" {
		return nil, nil
	}
	discovery, ok := c.opts.Packages.(packageDiscoverer)
	// An adapter without discovery cannot silently acquire update authority.
	if !ok {
		return nil, model.Err("WORKFLOW_CAPABILITY", "Managed updates require package capability discovery")
	}
	generation := c.state.Generation
	c.mu.Unlock()
	capability, err := discovery.Describe(ctx, plan.Step.Package)
	c.mu.Lock()
	// Discovered capabilities must belong to the same planning snapshot.
	if c.closed || generation != c.state.Generation {
		return nil, model.Err("STALE_PLAN", "Server changed during package discovery")
	}
	// Require explicit update and complete-input contracts rather than guessing from a package name.
	if err != nil || !capability.Update || !capability.KnownInputs {
		return nil, model.Err("WORKFLOW_CAPABILITY", "Package does not support managed updates with known planning inputs")
	}
	return c.managedInputs(plan.Step, predecessors)
}

// managedInputs resolves only completed unchanged dependencies included in the current reviewed graph.
func (c *Controller) managedInputs(step workflow.Step, predecessors []WorkflowPlan) (map[string]string, error) {
	outputs := map[string]map[string]string{}
	// Only retained results are known before any new provisioning begins.
	for _, prior := range predecessors {
		// A planned mutation cannot promise its future outputs to an exact infrastructure plan.
		if !prior.Reuse {
			continue
		}
		record := c.state.WorkflowRecords[prior.Key]
		// Retained output ownership must still match its reviewed predecessor.
		if record == nil || record.Status != "succeeded" || record.SpecHash != config.Fingerprint(prior.Step) {
			return nil, model.Err("STALE_PLAN", "Predecessor changed during managed planning")
		}
		result, err := c.workflowResult(record)
		// Corrupted private results must never become guessed planning values.
		if err != nil {
			return nil, err
		}
		outputs[prior.Step.ID] = result.StepOutputs()
	}
	inputs, err := resolveWorkflowValues(step.Inputs, outputs)
	// Staging a dependency first requires its own approval; do not execute it during planning.
	if err != nil {
		return nil, model.Err("WORKFLOW_INPUTS_UNRESOLVED", "Managed planning requires completed unchanged input producers; provision dependencies first")
	}
	return inputs, nil
}

// sealManagedContext preserves the exact private previous receipt and resolved inputs for recovery.
func (c *Controller) sealManagedContext(plan *WorkflowPlan, previous json.RawMessage, inputs map[string]string) error {
	// Ordinary package payloads already define their original compatible behavior.
	if plan.Step.Lifecycle != "managed" {
		return nil
	}
	plan.Base = config.Fingerprint(c.state.WorkflowRecords[plan.Key])
	var err error
	plan.Previous, err = c.store.Encrypt(string(previous))
	// Missing encryption cannot produce an approvable infrastructure plan.
	if err != nil {
		return err
	}
	raw, err := json.Marshal(inputs)
	// Preserve exact known bindings without exposing values in public summaries.
	if err != nil {
		return err
	}
	plan.Inputs, err = c.store.Encrypt(string(raw))
	return err
}

// restoreManagedContext rejects changed ownership or inputs before reserving an external mutation.
func (c *Controller) restoreManagedContext(plan WorkflowPlan, req *workflow.Request) error {
	// Existing one-time dependencies do not carry a managed planning context.
	if plan.Step.Lifecycle != "managed" {
		return nil
	}
	// Exact previous state is part of review, even when desired configuration did not change.
	if config.Fingerprint(c.state.WorkflowRecords[plan.Key]) != plan.Base {
		return model.Err("WORKFLOW_CONFLICT", "Managed resource changed after planning")
	}
	executionInputs := config.Fingerprint(req.Inputs)
	// Reconstruct private context from its authenticated original plan.
	if err := c.decodeManagedContext(plan, req); err != nil {
		return err
	}
	// A predecessor's changed result invalidates approval rather than triggering an implicit re-plan.
	if executionInputs != config.Fingerprint(req.Inputs) {
		return model.Err("WORKFLOW_CONFLICT", "Managed input changed after planning")
	}
	return nil
}

// decodeManagedContext restores the previous native checkpoint and inputs used to bind the saved plan.
func (c *Controller) decodeManagedContext(plan WorkflowPlan, req *workflow.Request) error {
	previous, err := c.store.Decrypt(plan.Previous)
	// Original private state is mandatory for receipt verification after response loss.
	if err != nil {
		return err
	}
	inputs, err := c.store.Decrypt(plan.Inputs)
	// Never substitute empty bindings for an unavailable protected snapshot.
	if err != nil {
		return err
	}
	req.Previous = json.RawMessage(previous)
	return json.Unmarshal([]byte(inputs), &req.Inputs)
}

// workflowPackage finds the original package owner for additive legacy workflow records.
func (c *Controller) workflowPackage(key string, record *WorkflowRecord) string {
	// New records persist their owning executable identity directly.
	if record.Package != "" {
		return record.Package
	}
	plan, err := c.originalWorkflowPlan(key, record)
	// Missing historical approval cannot authorize migration to a different owner.
	if err != nil {
		return ""
	}
	return plan.Step.Package
}

// reservedWorkflowRecord retains prior ownership while an explicitly approved update is uncertain.
func (c *Controller) reservedWorkflowRecord(plan WorkflowPlan) *WorkflowRecord {
	record := &WorkflowRecord{PlanID: plan.ID, SpecHash: config.Fingerprint(plan.Step), Status: "uncertain", SecretRefs: plan.SecretRefs, Package: plan.Step.Package}
	// A proven not-started update may restore this exact receipt after explicit reconciliation.
	if plan.Step.Lifecycle == "managed" {
		record.Prior = c.state.WorkflowRecords[plan.Key]
	}
	return record
}
