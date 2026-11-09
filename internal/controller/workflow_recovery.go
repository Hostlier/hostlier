package controller

import (
	"context"
	"encoding/json"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/internal/store"
	"hostlier/pkg/workflow"
	"net/http"
	"strings"
)

// pinnedInspector preserves the approved executable identity while observing uncertain effects.
type pinnedInspector interface {
	// InspectPinned reads owned resources through the original approved package.
	InspectPinned(context.Context, workflow.Request, string) (workflow.Inspection, error)
}

// workflowReview contains only recovery evidence safe for the public approval prompt.
type workflowReview struct {
	App         string                 `json:"app"`
	Step        string                 `json:"step"`
	Status      workflow.InspectStatus `json:"status"`
	Fingerprint string                 `json:"fingerprint"`
}

// apiWorkflows inspects uncertain steps and accepts only freshly reviewed reconciliation.
func (c *Controller) apiWorkflows(r *http.Request, p []string, actor string) (any, error) {
	// List retained states without exposing private package results.
	if len(p) == 3 && r.Method == "GET" {
		return c.workflowInventory(r, p[2])
	}
	// Recovery requires an explicit application and step identity.
	if len(p) != 4 || (r.Method != "GET" && r.Method != "POST") {
		return endpointNotFound()
	}
	plan, err := c.recoveryPlan(p[2], p[3])
	// Running work or missing approval evidence cannot be reconciled.
	if err != nil {
		return nil, err
	}
	inspected, err := c.inspectWorkflow(r.Context(), plan)
	// Failed observations leave the uncertain marker intact.
	if err != nil {
		return nil, err
	}
	review := workflowReview{App: p[2], Step: p[3], Status: inspected.Status}
	review.Fingerprint = config.Fingerprint([]any{actor, c.state.Generation, plan, inspected})
	// Inspection does not authorize changing retained workflow state.
	if r.Method == "GET" {
		return review, nil
	}
	return c.reconcileWorkflow(r, plan, review, inspected)
}

// workflowInventory returns safe retained step identities and state for one application.
func (c *Controller) workflowInventory(r *http.Request, app string) ([]map[string]string, error) {
	rows := []map[string]string{}
	prefix := c.state.WorkflowNamespace + "/" + app + "/"
	items, err := c.store.Collection(r.Context(), "workflowRecords", store.CollectionQuery{KeyPrefix: prefix})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		var record WorkflowRecord
		if err := json.Unmarshal(item.Payload, &record); err != nil {
			return nil, err
		}
		rows = append(rows, map[string]string{"step": strings.TrimPrefix(item.Key, prefix), "status": record.Status})
	}
	return rows, nil
}

// recoveryPlan locates the durable original approval instead of reconstructing mutable project input.
func (c *Controller) recoveryPlan(app, step string) (WorkflowPlan, error) {
	key := c.state.WorkflowNamespace + "/" + app + "/" + step
	record := c.state.WorkflowRecords[key]
	// Reconciliation never races an active operation or resets successful resources.
	if c.busy(app) || record == nil || record.Status != "uncertain" {
		return WorkflowPlan{}, model.Err("WORKFLOW_RECOVERY", "Only an idle uncertain workflow step can be reconciled")
	}
	return c.originalWorkflowPlan(key, record)
}

// originalWorkflowPlan matches an execution attempt and refuses ambiguous legacy approvals.
func (c *Controller) originalWorkflowPlan(key string, record *WorkflowRecord) (WorkflowPlan, error) {
	var found WorkflowPlan
	// Operations preserve the private prepared payload identity across restarts.
	for _, op := range c.state.Operations {
		// A repeated configuration may still contain newly generated credentials.
		for _, plan := range op.Workflow {
			// Match the exact attempted plan when present, with a conservative legacy fallback.
			if !matchesRecoveryPlan(plan, key, record) {
				continue
			}
			// Two possible legacy attempts cannot establish the credentials that were used.
			if found.ID != "" && found.ID != plan.ID {
				return WorkflowPlan{}, model.Err("WORKFLOW_RECOVERY", "Original workflow approval is ambiguous")
			}
			found = plan
		}
	}
	// Missing approval evidence cannot be replaced by mutable current configuration.
	if found.ID == "" {
		return found, model.Err("WORKFLOW_RECOVERY", "Original workflow approval unavailable")
	}
	return found, nil
}

// matchesRecoveryPlan binds both the resource definition and its original prepared credentials.
func matchesRecoveryPlan(plan WorkflowPlan, key string, record *WorkflowRecord) bool {
	return plan.Key == key && config.Fingerprint(plan.Step) == record.SpecHash && (record.PlanID == "" || record.PlanID == plan.ID)
}

// inspectWorkflow releases the state lock during read-only package observation and checks for drift.
func (c *Controller) inspectWorkflow(ctx context.Context, plan WorkflowPlan) (workflow.Inspection, error) {
	req, err := c.recoveryRequest(plan)
	// Lost protected inputs must not become empty credentials during recovery.
	if err != nil {
		return workflow.Inspection{}, err
	}
	generation := c.state.Generation
	c.mu.Unlock()
	inspection, err := c.callWorkflowInspector(ctx, req, plan.Digest)
	c.mu.Lock()
	// Concurrent changes require a fresh inspection and approval.
	if c.closed || generation != c.state.Generation {
		return workflow.Inspection{}, model.Err("STALE_PLAN", "Server changed during workflow inspection")
	}
	// Provider diagnostics and private outputs must never leak through errors.
	if err != nil || !inspection.Valid() {
		return workflow.Inspection{}, model.Err("WORKFLOW_INSPECTION", "Package cannot verify the retained workflow outcome")
	}
	return inspection, nil
}

// callWorkflowInspector prefers digest-verified process observation and supports injected SDK runners.
func (c *Controller) callWorkflowInspector(ctx context.Context, req workflow.Request, digest string) (workflow.Inspection, error) {
	// Production package runners must preserve executable approval across recovery.
	if inspector, ok := c.opts.Packages.(pinnedInspector); ok {
		return inspector.InspectPinned(ctx, req, digest)
	}
	// Explicit embedded runners may implement the optional SDK lifecycle capability.
	if inspector, ok := c.opts.Packages.(workflow.Inspector); ok {
		return inspector.Inspect(ctx, req)
	}
	return workflow.Inspection{}, model.Err("UNSUPPORTED_OPERATION", "Package does not support inspection")
}

// recoveryRequest restores the approved encrypted payload and predecessor outputs for inspection.
func (c *Controller) recoveryRequest(plan WorkflowPlan) (workflow.Request, error) {
	req := workflow.Request{Version: 1, Action: "inspect", Key: plan.Key, Step: plan.Step, Host: c.workflowHost()}
	// Reconciliation uses the original deployment, even after a controller restart.
	if plan.Step.Hook == workflow.ProjectDeployHook {
		op := c.workflowOperation(plan)
		// A missing approval cannot be replaced with the current app specification.
		if op == nil {
			return req, model.Err("WORKFLOW_RECOVERY", "Original project hook approval unavailable")
		}
		// A malformed stored project prevents inspection.
		if err := attachWorkflowProject(&req, op.Spec); err != nil {
			return req, err
		}
	}
	secrets, err := c.operationSecrets(plan.SecretRefs)
	// Original secret revisions must remain available.
	if err != nil {
		return req, err
	}
	req.Secrets = secrets
	payload, err := c.store.Decrypt(c.state.WorkflowData[plan.ID])
	// Inspection uses the credentials generated before the first side effect.
	if err != nil {
		return req, err
	}
	req.Prepared = json.RawMessage(payload)
	// Managed inspection must use the original protected previous state and planned inputs.
	if plan.Step.Lifecycle == "managed" {
		err = c.decodeManagedContext(plan, &req)
		return req, err
	}
	outputs, err := c.recoveryPredecessors(plan)
	// Unverified predecessor outputs cannot authorize recovery of a dependent step.
	if err != nil {
		return req, err
	}
	req.Inputs, err = resolveWorkflowValues(plan.Step.Inputs, outputs)
	return req, err
}

// recoveryPredecessors resolves only successful explicit dependencies using their protected results.
func (c *Controller) recoveryPredecessors(plan WorkflowPlan) (map[string]map[string]string, error) {
	outputs := map[string]map[string]string{}
	prefix := strings.TrimSuffix(plan.Key, plan.Step.ID)
	// Declared graph edges define the only possible private input producers.
	for _, id := range plan.Step.Needs {
		record := c.state.WorkflowRecords[c.predecessorKey(plan, prefix, id)]
		// Recovery must begin with the earliest uncertain predecessor.
		if record == nil || record.Status != "succeeded" {
			return nil, model.Err("WORKFLOW_RECOVERY", "Reconcile predecessor steps first")
		}
		result, err := c.workflowResult(record)
		// Authenticate stored private values before resolving bindings.
		if err != nil {
			return nil, err
		}
		outputs[id] = result.StepOutputs()
	}
	return outputs, nil
}

// reconcileWorkflow applies only verified metadata recovery after an explicit matching approval.
func (c *Controller) reconcileWorkflow(r *http.Request, plan WorkflowPlan, review workflowReview, inspection workflow.Inspection) (any, error) {
	var approval approvalRequest
	// Fresh inspection evidence must match the exact user's reviewed fingerprint.
	if err := decode(r, &approval); err != nil {
		return nil, err
	}
	// Never infer permission from inspection or a previous deployment approval.
	if !approval.Approved || approval.Fingerprint != review.Fingerprint {
		return nil, model.Err("APPROVAL_REQUIRED", "Review and approve the current recovery observation")
	}
	// Unknown outcomes remain blocked rather than retrying potentially completed work.
	if inspection.Status == workflow.Uncertain {
		return nil, model.Err("WORKFLOW_UNCERTAIN", "Outcome remains uncertain; no state changed")
	}
	// Independently verified results restore private bindings without replaying apply.
	if inspection.Status == workflow.Completed {
		_, err := c.completeWorkflowStep(plan, *inspection.Result, "")
		return review, err
	}
	previous := c.state.WorkflowRecords[plan.Key]
	// A proven not-started update restores the previous owned resource receipt.
	if previous.Prior != nil {
		c.state.WorkflowRecords[plan.Key] = previous.Prior
	} else {
		delete(c.state.WorkflowRecords, plan.Key)
	}
	c.state.Generation++
	// Failed persistence keeps the conservative marker even in the current process.
	if err := c.store.Save(c.state); err != nil {
		c.state.WorkflowRecords[plan.Key] = previous
		return nil, err
	}
	return review, nil
}

// predecessorKey finds per-deployment hook identities in the owning accepted operation.
func (c *Controller) predecessorKey(plan WorkflowPlan, prefix, id string) string {
	// Per-release predecessor keys live with the original approved operation.
	if op := c.workflowOperation(plan); op != nil {
		// Each accepted step carries its own fully qualified record key.
		for _, candidate := range op.Workflow {
			// Match the declared producer rather than a similarly named app step.
			if candidate.Step.ID == id {
				return candidate.Key
			}
		}
	}
	return prefix + id
}

// workflowOperation locates the original approval for both pinned inputs and project context.
func (c *Controller) workflowOperation(plan WorkflowPlan) *Operation {
	// Locate the operation that accepted this particular plan revision.
	for _, op := range c.state.Operations {
		// The immutable plan ID distinguishes successive releases of the same step.
		if ownsWorkflowPlan(op.Workflow, plan.ID) {
			return op
		}
	}
	return nil
}

// ownsWorkflowPlan prevents a similarly named hook in another deployment supplying private inputs.
func ownsWorkflowPlan(plans []WorkflowPlan, id string) bool {
	// Inspect only declared product graph entries.
	for _, plan := range plans {
		// Reject a missing or out-of-policy declaration before runtime mutation.
		if plan.ID == id {
			return true
		}
	}
	return false
}
