package controller

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
	"hostlier/pkg/workflow"
)

// initializeWorkflows migrates additive durable fields and configures the lightweight process bridge.
func (c *Controller) initializeWorkflows() {
	// Namespace external ownership so independent Hostlier servers cannot adopt each other's resources.
	if c.state.WorkflowNamespace == "" {
		c.state.WorkflowNamespace = random()
	}
	// Earlier controller states have no package execution data.
	if c.state.WorkflowData == nil {
		c.state.WorkflowData = map[string]string{}
	}
	// Package resources outlive application deletion and failed deployments.
	if c.state.WorkflowRecords == nil {
		c.state.WorkflowRecords = map[string]*WorkflowRecord{}
	}
	// Tests may inject a runner; production discovers administrator-installed binaries only.
	if c.opts.Packages == nil {
		c.opts.Packages = packageexec.Runner{Directory: filepath.Join(c.opts.StateDir, "packages"), PlanTimeout: c.opts.PlanningTimeout}
	}
}

// prepareWorkflow resolves immutable package plans within the existing deployment approval boundary.
func (c *Controller) prepareWorkflow(ctx context.Context, spec model.Project) ([]WorkflowPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.PlanningTimeout)
	defer cancel()
	// Existing deployments without workflows retain their behavior.
	if spec.Workflow == nil {
		return nil, nil
	}
	// A new lifecycle event must not hide an unresolved effect from an earlier hook attempt.
	if c.uncertainProductHook(spec.Name) {
		return nil, model.Err("WORKFLOW_CONFLICT", "Reconcile the earlier uncertain product hook before a new deployment")
	}
	ordered, err := spec.Workflow.Order()
	// Cycles must never reach a package runner.
	if err != nil {
		return nil, err
	}
	plans := []WorkflowPlan{}
	// Review in execution order so dependent provisioning is comprehensible at the CLI.
	for _, step := range ordered {
		plan, err := c.prepareWorkflowStep(ctx, spec, step, plans)
		// No deployment may be approved with an unresolved package step.
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// prepareWorkflowStep snapshots private provider input and retains an encrypted execution payload.
func (c *Controller) prepareWorkflowStep(ctx context.Context, spec model.Project, step workflow.Step, predecessors []WorkflowPlan) (WorkflowPlan, error) {
	plan := WorkflowPlan{ID: random(), Key: c.state.WorkflowNamespace + "/" + spec.Name + "/" + step.ID, Step: step}
	// Hooks execute once per reviewed deployment, while ordinary dependency keys remain retained.
	if step.Phase != "" {
		plan.Key += "@" + plan.ID
	}
	inputs, err := c.prepareManagedInputs(ctx, plan, predecessors)
	// Capabilities and resolved predecessor identity must be established before inspection.
	if err != nil {
		return plan, err
	}
	previous, err := c.workflowPrevious(plan)
	// Configuration replacement and uncertain prior side effects require explicit operator reconciliation.
	if err != nil {
		return plan, err
	}
	plan.Reuse = previous != nil && step.Lifecycle != "managed"
	// Retain encrypted original state and inputs for exact apply and recovery.
	if err = c.sealManagedContext(&plan, previous, inputs); err != nil {
		return plan, err
	}
	plan.SecretRefs, err = c.references(model.Project{Secrets: step.Secrets}, nil)
	// Pin secret revisions without exposing their contents in the review.
	if err != nil {
		return plan, err
	}
	secrets, err := c.operationSecrets(plan.SecretRefs)
	// Unavailable protected inputs invalidate planning.
	if err != nil {
		return plan, err
	}
	req := workflow.Request{Version: 1, Key: plan.Key, Step: step, Secrets: secrets, Inputs: inputs, Previous: previous, Host: c.workflowHost()}
	// Plan with the canonical route and immutable images already resolved.
	if err := attachWorkflowProject(&req, spec); err != nil {
		return plan, err
	}
	prepared, err := c.callPackagePlan(ctx, req)
	// Do not expose arbitrary package errors or diagnostics to the API.
	if err != nil {
		return plan, err
	}
	return c.sealWorkflowPlan(plan, prepared)
}

// callPackagePlan releases the controller lock during provider inspection and detects intervening mutations.
func (c *Controller) callPackagePlan(ctx context.Context, request workflow.Request) (workflow.Prepared, error) {
	generation := c.state.Generation
	c.mu.Unlock()
	prepared, err := c.opts.Packages.Plan(ctx, request)
	c.mu.Lock()
	// Plans inspected across state changes require a fresh review rather than mixing snapshots.
	if c.closed || generation != c.state.Generation {
		return prepared, model.Err("STALE_PLAN", "Server changed during package planning; retry")
	}
	// Capability errors identify the package but contain no private process output.
	if errors.Is(err, packageexec.ErrMissingDeployHook) {
		return prepared, model.Err("WORKFLOW_CAPABILITY", "Controller package "+request.Step.Package+" does not provide the project-deploy hook")
	}
	// Package-specific exceptions never become public API messages.
	if err != nil {
		return prepared, model.Err("PACKAGE_PLAN_FAILED", "Package planning failed; check the installed package and configuration")
	}
	return prepared, err
}

// workflowPrevious returns private state only for a completed, unchanged owned dependency.
func (c *Controller) workflowPrevious(plan WorkflowPlan) (json.RawMessage, error) {
	record := c.state.WorkflowRecords[plan.Key]
	// New keys have no previous package state.
	if record == nil {
		return nil, nil
	}
	// Changed desired state cannot silently replace retained data.
	if record.SpecHash != config.Fingerprint(plan.Step) && plan.Step.Lifecycle != "managed" {
		return nil, model.Err("WORKFLOW_CONFLICT", "Retained workflow step differs; use a new step identity for deliberate replacement")
	}
	// Lost responses are never blindly replayed, even after a fresh approval.
	if record.Status != "succeeded" {
		return nil, model.Err("WORKFLOW_UNCERTAIN", "Prior package execution needs reconciliation before this step can run again")
	}
	// Managed updates retain backend identity across changes to the desired configuration.
	if plan.Step.Lifecycle == "managed" && c.workflowPackage(plan.Key, record) != plan.Step.Package {
		return nil, model.Err("WORKFLOW_CONFLICT", "A managed update cannot replace its owning package")
	}
	result, err := c.workflowResult(record)
	return result.State, err
}

// sealWorkflowPlan stores private payloads encrypted and redacts known secrets from the review text.
func (c *Controller) sealWorkflowPlan(plan WorkflowPlan, prepared workflow.Prepared) (WorkflowPlan, error) {
	// Every runner must return a bounded plan with an immutable executable identity.
	if len(prepared.Digest) != 64 || len(prepared.Summary) == 0 || len(prepared.Summary) > 32 || !json.Valid(prepared.Payload) {
		return plan, model.Err("PACKAGE_PLAN_FAILED", "Package returned an invalid plan")
	}
	plan.Digest = prepared.Digest
	// Public summaries must never expose stored credentials.
	for _, summary := range prepared.Summary {
		// Bound public diagnostics before redacting and persisting them.
		if len(summary) > 4096 {
			return plan, model.Err("PACKAGE_PLAN_FAILED", "Package summary exceeds the review limit")
		}
		plan.Summary = append(plan.Summary, c.redact(summary))
	}
	sealed, err := c.store.Encrypt(string(prepared.Payload))
	// No plan can reference a missing encrypted payload.
	if err != nil {
		return plan, err
	}
	c.state.WorkflowData[plan.ID] = sealed
	return plan, nil
}

// workflowHost provides trusted runtime ownership independently of project settings.
func (c *Controller) workflowHost() *workflow.HostContext {
	return &workflow.HostContext{Runtime: c.opts.Runtime.Name(), StateDir: c.opts.StateDir}
}

// attachWorkflowProject binds only a project hook to the reviewed, controller-resolved spec.
func attachWorkflowProject(req *workflow.Request, spec model.Project) error {
	// Ordinary dependency packages receive only their declared configuration.
	if req.Step.Hook != workflow.ProjectDeployHook {
		return nil
	}
	data, err := json.Marshal(spec)
	// Only publish a fully encoded project to the package.
	if err == nil {
		req.Project = data
	}
	return err
}
