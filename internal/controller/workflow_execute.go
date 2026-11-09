package controller

import (
	"encoding/json"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
)

// executeWorkflow completes each package before resolving application environment bindings.
func (c *Controller) executeWorkflow(op *Operation) (workflowExecution, error) {
	return c.executeWorkflowPhase(op, false)
}

// executeWorkflowPhase reuses completed predecessors when running post-readiness hooks.
func (c *Controller) executeWorkflowPhase(op *Operation, after bool) (workflowExecution, error) {
	result := workflowExecution{}
	outputs := map[string]map[string]string{}
	stepOutputs := map[string]map[string]string{}
	// Plans already carry a validated topological order.
	for _, plan := range op.Workflow {
		// Post-readiness hooks cannot supply application startup configuration.
		if plan.Step.Phase == "afterSuccess" && !after {
			continue
		}
		inputs, err := resolveWorkflowValues(plan.Step.Inputs, stepOutputs)
		// Missing predecessor output blocks all dependent side effects.
		if err != nil {
			return result, err
		}
		stepResult, err := c.executeWorkflowStep(plan, inputs, op.ID)
		// Keep completed resources but never proceed past a failed package step.
		if err != nil {
			return result, err
		}
		outputs[plan.Step.ID] = stepResult.Outputs
		stepOutputs[plan.Step.ID] = stepResult.StepOutputs()
		result.networks = append(result.networks, stepResult.Networks...)
	}
	// Projects without workflow bindings preserve their existing secret behavior.
	if op.Spec.Workflow == nil {
		return result, nil
	}
	var err error
	result.bindings, err = resolveWorkflowValues(op.Spec.Workflow.Bindings, outputs)
	return result, err
}

// resolveWorkflowValues binds private outputs without persisting them as public specification values.
func resolveWorkflowValues(bindings map[string]string, outputs map[string]map[string]string) (map[string]string, error) {
	values := map[string]string{}
	// Inputs and application bindings share one strict output reference format.
	for name, ref := range bindings {
		id, field, ok := strings.Cut(ref, ".")
		// A missing value is an execution failure, never an empty credential.
		if !ok || outputs[id][field] == "" {
			return nil, model.Err("WORKFLOW_OUTPUT_MISSING", "Required package output is unavailable")
		}
		values[name] = outputs[id][field]
	}
	return values, nil
}

// executeWorkflowStep records uncertain execution before invoking any package side effects.
func (c *Controller) executeWorkflowStep(plan WorkflowPlan, inputs map[string]string, operationID string) (workflow.Result, error) {
	c.mu.Lock()
	req, reused, err := c.beginWorkflowStep(plan, inputs, operationID)
	c.mu.Unlock()
	// Completed dependencies are reused without repeating external mutations.
	if err != nil || reused != nil {
		// Return a copied retained result when its state is verified.
		if reused != nil {
			return *reused, err
		}
		return workflow.Result{}, err
	}
	result, err := c.opts.Packages.Apply(c.ctx, req, plan.Digest)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Failure leaves the durable uncertain marker intact for operator reconciliation.
	if err != nil {
		return workflow.Result{}, model.Err("PACKAGE_APPLY_FAILED", "Package execution failed; reconcile before retry")
	}
	return c.completeWorkflowStep(plan, result, operationID)
}

// beginWorkflowStep snapshots approved secrets and durably reserves the resource before execution.
func (c *Controller) beginWorkflowStep(plan WorkflowPlan, inputs map[string]string, operationID string) (workflow.Request, *workflow.Result, error) {
	req := workflow.Request{Version: 1, Key: plan.Key, Step: plan.Step, Inputs: inputs, Host: c.workflowHost()}
	// A hook can only run with the project from its own accepted operation.
	if err := c.attachAcceptedProject(&req, plan, operationID); err != nil {
		return req, nil, err
	}
	// A completed step in this same accepted operation is never applied again in a later phase.
	if record := c.state.WorkflowRecords[plan.Key]; record != nil && record.PlanID == plan.ID && record.Status == "succeeded" {
		result, err := c.workflowResult(record)
		return req, &result, err
	}
	// A retained result can be reused only for the identical reviewed step.
	if record := c.state.WorkflowRecords[plan.Key]; record != nil && plan.Step.Lifecycle != "managed" {
		// Reject uncertain state and configuration changes even if the plan predates them.
		if record.Status != "succeeded" || record.SpecHash != config.Fingerprint(plan.Step) {
			return req, nil, model.Err("WORKFLOW_CONFLICT", "Workflow resource requires reconciliation")
		}
		result, err := c.workflowResult(record)
		return req, &result, err
	}
	return c.reserveWorkflowStep(plan, req, operationID)
}

// attachAcceptedProject resolves the same pinned project that was sent during hook planning.
func (c *Controller) attachAcceptedProject(req *workflow.Request, plan WorkflowPlan, operationID string) error {
	// Ordinary providers are independent of the surrounding application rollout.
	if plan.Step.Hook != workflow.ProjectDeployHook {
		return nil
	}
	op := c.state.Operations[operationID]
	// Never substitute a project from a different or expired approval.
	if op == nil || !ownsWorkflowPlan(op.Workflow, plan.ID) {
		return model.Err("WORKFLOW_CONFLICT", "Original project hook approval unavailable")
	}
	return attachWorkflowProject(req, op.Spec)
}

// reserveWorkflowStep pins reviewed inputs and persists uncertainty before any package side effects.
func (c *Controller) reserveWorkflowStep(plan WorkflowPlan, req workflow.Request, operationID string) (workflow.Request, *workflow.Result, error) {
	// Managed changes must still match the exact prior receipt reviewed by the user.
	if err := c.restoreManagedContext(plan, &req); err != nil {
		return req, nil, err
	}
	var err error
	req.Secrets, err = c.operationSecrets(plan.SecretRefs)
	// Pinned credentials must remain available before starting a package.
	if err != nil {
		return req, nil, err
	}
	plain, err := c.store.Decrypt(c.state.WorkflowData[plan.ID])
	// The encrypted payload is the exact plan reviewed by the user.
	if err != nil {
		return req, nil, err
	}
	req.Prepared = json.RawMessage(plain)
	c.state.WorkflowRecords[plan.Key] = c.reservedWorkflowRecord(plan)
	c.state.Generation++
	c.log("Package step started: "+plan.Step.ID, operationID, "info")
	return req, nil, c.store.Save(c.state)
}

// completeWorkflowStep encrypts the full private result before exposing it to subsequent steps.
func (c *Controller) completeWorkflowStep(plan WorkflowPlan, result workflow.Result, operationID string) (workflow.Result, error) {
	// Injected runners and process runners must satisfy the same output contract.
	if !workflow.ValidResult(result) {
		return workflow.Result{}, model.Err("PACKAGE_RESULT_INVALID", "Package result is invalid")
	}
	raw, err := json.Marshal(result)
	// Bound encrypted persistent state before committing a successful step.
	if err != nil || len(raw) > 4*1024*1024 {
		return workflow.Result{}, model.Err("PACKAGE_RESULT_INVALID", "Package result exceeds the storage limit")
	}
	sealed, err := c.store.Encrypt(string(raw))
	// Never persist private package output without authenticated encryption.
	if err != nil {
		return workflow.Result{}, err
	}
	record := c.state.WorkflowRecords[plan.Key]
	record.Result = sealed
	record.Status = "succeeded"
	record.Prior = nil
	c.state.Generation++
	c.log("Package step completed: "+plan.Step.ID, operationID, "info")
	// Failed terminal persistence must not allow in-memory reuse of uncertain external work.
	if err := c.store.Save(c.state); err != nil {
		record.Status = "uncertain"
		return workflow.Result{}, err
	}
	return result, nil
}

// workflowResult authenticates retained private state before reuse or private downstream binding.
func (c *Controller) workflowResult(record *WorkflowRecord) (workflow.Result, error) {
	var result workflow.Result
	plain, err := c.store.Decrypt(record.Result)
	// Corrupt protected state must never be interpreted as an empty successful result.
	if err != nil {
		return result, err
	}
	err = json.Unmarshal([]byte(plain), &result)
	return result, err
}

// workflowSecretReference retains inputs required by accepted operations or owned package resources.
func (c *Controller) workflowSecretReference(name string) error {
	// Retain current and prior credentials while an approved update remains uncertain.
	for _, record := range c.state.WorkflowRecords {
		// A not-started reconciliation can restore the prior ownership receipt.
		if workflowRecordReferences(record, name) {
			return model.Err("SECRET_IN_USE", "Secret is referenced by a workflow resource")
		}
	}
	// Accepted package work needs credentials even before its first durable resource marker.
	for _, op := range c.state.Operations {
		// Completed or rejected operations have no remaining execution requirement.
		if op.Status != "queued" && op.Status != "running" {
			continue
		}
		// Examine each step's independently pinned private inputs.
		for _, plan := range op.Workflow {
			// Reject deletion if any step still needs the secret.
			if workflowReferences(plan, name) {
				return model.Err("SECRET_IN_USE", "Secret is referenced by an accepted workflow")
			}
		}
	}
	return nil
}

// workflowReferences checks one bounded step's named private inputs.
func workflowReferences(plan WorkflowPlan, name string) bool {
	// Aliases have no bearing on secret identity.
	for _, ref := range plan.SecretRefs {
		// A single matching reference keeps the secret available.
		if ref.Name == name {
			return true
		}
	}
	return false
}

// workflowRecordReferences includes credentials retained for reversible uncertain updates.
func workflowRecordReferences(record *WorkflowRecord, name string) bool {
	// Successful completion removes prior receipts; uncertain updates retain their exact lineage.
	for current := record; current != nil; current = current.Prior {
		// Alias spelling never changes which stored credential is retained.
		for _, ref := range current.SecretRefs {
			// One matching old or current reference prevents credential deletion.
			if ref.Name == name {
				return true
			}
		}
	}
	return false
}
