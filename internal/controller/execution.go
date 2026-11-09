package controller

import (
	"errors"
	"regexp"
	"time"

	"hostlier/internal/model"
)

var safeCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
var safeDiagnosticStage = regexp.MustCompile(`^[a-z][a-z0-9/-]{0,119}$`)

// enqueue keeps accepted operations alive independently of the requesting CLI.
func (c *Controller) enqueue(id string) {
	c.wg.Add(1)
	go c.runOperation(id)
}

// runOperation serializes runtime changes while releasing the state lock during engine work.
func (c *Controller) runOperation(id string) {
	defer c.wg.Done()
	c.execMu.Lock()
	defer c.execMu.Unlock()
	c.mu.Lock()
	op := c.state.Operations[id]
	// Release the state lock when shutdown or persistence prevents execution.
	if !c.beginOperation(op) {
		c.mu.Unlock()
		return
	}
	input := c.operationInput(op)
	c.mu.Unlock()
	result := c.executeOperation(input)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finishOperation(op, result)
}

// beginOperation durably records execution before any runtime side effects occur.
// The caller holds the state lock.
func (c *Controller) beginOperation(op *Operation) bool {
	// Do not begin new runtime work after shutdown starts.
	if c.closed {
		return false
	}
	op.Status = "running"
	c.log("Operation started", op.ID, "info")
	// Mark the operation failed when running state cannot be durably recorded.
	if err := c.store.Save(c.state); err != nil {
		op.Status = "failed"
		op.Error = &model.Error{Code: "STATE_WRITE_FAILED", Message: "Unable to persist execution state"}
		return false
	}
	return true
}

// operationInput snapshots prior deployment and decrypts exactly the approved secret revisions.
// The caller holds the state lock; accepted operation specifications are immutable.
func (c *Controller) operationInput(op *Operation) operationInput {
	input := operationInput{operation: op}
	input.secrets, input.err = c.operationSecrets(op.SecretRefs)
	// Snapshot the existing deployment for runtime replacement and lifecycle work.
	if app := c.state.Applications[op.Name]; app != nil {
		previous := app.Deployment
		input.previous = &previous
	}
	return input
}

// operationSecrets fails closed if an accepted secret revision is unavailable.
func (c *Controller) operationSecrets(refs map[string]SecretRef) (map[string]string, error) {
	secrets := map[string]string{}
	// Inspect all pinned references before changing protected deployment inputs.
	for env, ref := range refs {
		secret := c.state.Secrets[ref.Name]
		// Reject this path: Secret unavailable.
		if secret == nil {
			return nil, model.Err("SECRET_NOT_FOUND", "Secret unavailable")
		}
		plain, err := c.store.Decrypt(secret.Revisions[ref.Revision])
		// Stop when encrypted protected data cannot be authenticated and decrypted.
		if err != nil {
			return nil, err
		}
		secrets[env] = plain
	}
	return secrets, nil
}

// executeOperation chooses one engine workflow without holding the controller state lock.
func (c *Controller) executeOperation(input operationInput) operationResult {
	result := operationResult{err: input.err, resourceRefs: map[string]string{}}
	// Avoid runtime side effects when approved secrets could not be loaded.
	if input.err != nil {
		return result
	}
	op := input.operation
	// Route runtime events back through controller sanitization and persistence.
	onEvent := func(event model.Event) { c.runtimeEvent(op.ID, event) }
	// Choose the runtime workflow represented by the accepted operation kind.
	switch op.Kind {
	// Run dependency binding and application replacement together.
	case "deploy":
		return c.executeDeployment(input, onEvent)
	// Provision the approved private resource and optional restore.
	case "resource-resize":
		result.err = c.executeResourceResize(op, onEvent)
	// Provision the approved database instance.
	case "resource":
		result.err = c.executeResource(op, onEvent)
	// Run the accepted private-resource lifecycle operation.
	case "resource-action":
		result.err = c.executeResourceAction(op, onEvent)
	// Run an ordinary application lifecycle action for remaining operation kinds.
	default:
		result.err = c.opts.Runtime.Action(c.ctx, op.Name, op.Action, model.ActionContext{Purge: op.Purge, Application: input.previous, OnEvent: onEvent})
	}
	return result
}

// runtimeEvent records only approved step names and levels, never arbitrary engine messages.
func (c *Controller) runtimeEvent(id string, event model.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	level := "info"
	// Preserve error severity without trusting arbitrary engine messages.
	if event.Level == "error" {
		level = "error"
		// Preserve warning severity without trusting arbitrary engine messages.
	} else if event.Level == "warn" {
		level = "warn"
	}
	message := "Runtime step transition"
	// Expose only known safe runtime step names.
	switch event.Step {
	// Expose only known step names, never arbitrary runtime event text.
	case "pull", "stop-old", "health", "route", "certificate", "ready", "cleanup", "resource-pull":
		message = "Runtime step: " + event.Step
	}
	c.log(message, id, level)
}

// executeDeployment resolves private dependency bindings before invoking the app runtime.
func (c *Controller) executeDeployment(input operationInput, onEvent func(model.Event)) operationResult {
	op := input.operation
	workflowBindings, workflowErr := c.executeWorkflow(op)
	// Package provisioning and output publication must complete before deploying the app.
	if workflowErr != nil {
		return operationResult{err: workflowErr}
	}
	// Only explicitly declared package outputs become app environment values.
	for key, value := range workflowBindings.bindings {
		input.secrets[key] = value
	}
	bindings, networks, refs, err := c.executeDependencies(c.ctx, op, onEvent)
	result := operationResult{resourceRefs: refs, err: err}
	// Do not deploy the application when dependency preparation fails.
	if err != nil {
		return result
	}
	// Inject only the approved private bindings into deployment environment values.
	for key, value := range bindings {
		input.secrets[key] = value
	}
	result.deployment, result.err = c.opts.Runtime.Deploy(c.ctx, op.Spec, model.DeployContext{OperationID: op.ID, Secrets: input.secrets, DependencyNetworks: append(networks, workflowBindings.networks...), Previous: input.previous, OnEvent: onEvent})
	// Only healthy application replacement authorizes the reviewed after-success hooks.
	if result.err == nil {
		result.deployed = true
		_, result.err = c.executeWorkflowPhase(op, true)
	}
	return result
}

// executeResource provisions a durable instance before optionally restoring its approved backup.
func (c *Controller) executeResource(op *Operation, onEvent func(model.Event)) error {
	_, _, err := c.provisionResource(c.ctx, *op.ResourceSpec, op.ID, onEvent)
	// Restore only after the approved target resource was provisioned successfully.
	if err == nil && op.RestoreBackupID != "" {
		err = c.executeRestore(c.ctx, op, onEvent)
	}
	return err
}

// executeResourceAction reads credentials under lock and performs the engine action outside it.
func (c *Controller) executeResourceAction(op *Operation, onEvent func(model.Event)) error {
	c.mu.Lock()
	credentials, err := c.resourceCredentials(op.Name, *op.ResourceSpec)
	c.mu.Unlock()
	// Require stable protected resource credentials before invoking runtime work.
	if err != nil {
		return err
	}
	return c.opts.ResourceManager.Action(c.ctx, op.Name, op.Action, model.ResourceContext{Credentials: credentials, OperationID: op.ID, Purge: op.Purge, OnEvent: onEvent})
}

// finishOperation persists the terminal result, retaining a recovery marker if persistence fails.
// The caller holds the state lock.
func (c *Controller) finishOperation(op *Operation, result operationResult) {
	// Record a safe terminal failure when runtime work did not succeed.
	if result.err != nil {
		// Preserve the observed healthy deployment when a post-readiness hook fails.
		if result.deployed {
			c.recordDeployment(op, result)
		}
		c.failOperation(op, result.err)
	} else {
		c.recordSuccessfulOperation(op, result)
		c.supersedeInterrupted(op)
		op.Status = "succeeded"
		c.log("Operation succeeded", op.ID, "info")
	}
	// Retain an actionable recovery marker when the terminal result cannot be saved.
	if err := c.store.Save(c.state); err != nil {
		op.Status = "interrupted"
		op.Error = &model.Error{Code: "STATE_WRITE_FAILED", Message: "Runtime completed but durable result could not be saved; inspect before retry"}
	}
}

// failOperation exposes a safe error code while keeping runtime diagnostics out of public responses.
func (c *Controller) failOperation(op *Operation, err error) {
	// Mark a failed provisioned resource so later status exposes the failure.
	if op.Kind == "resource" && c.state.Resources[op.Name] != nil && !resourcePackagePreflight(err) {
		c.state.Resources[op.Name].Status = "failed"
	}
	code := "RUNTIME_FAILED"
	var typed *model.Error
	// Expose only bounded machine-readable error codes from typed runtime failures.
	if errors.As(err, &typed) && safeCode.MatchString(typed.Code) {
		code = typed.Code
	}
	op.Status = "failed"
	op.Error = &model.Error{Code: code, Message: "Runtime operation failed; inspect server diagnostics"}
	c.log("Operation failed", op.ID, "error")
	// A fixed-stage marker is useful after Nginx rolls back its temporary
	// challenge include. Keep it in the root-only file, never public events.
	if stage := protectedFailureStage(err); stage != "" && c.opts.LogPath != "" {
		c.appendLog(LogEntry{Timestamp: c.opts.Now().UTC().Format(time.RFC3339Nano), Level: "error", Message: "Nginx activation failed at " + stage, OperationID: op.ID, Application: op.Name})
	}
}

func protectedFailureStage(err error) string {
	var diagnostic interface{ DiagnosticStage() string }
	if errors.As(err, &diagnostic) {
		stage := diagnostic.DiagnosticStage()
		if safeDiagnosticStage.MatchString(stage) {
			return stage
		}
	}
	return ""
}

// recordSuccessfulOperation updates only the state owned by the completed operation kind.
func (c *Controller) recordSuccessfulOperation(op *Operation, result operationResult) {
	// Update only the durable state owned by the completed operation kind.
	switch op.Kind {
	// Activate the new deployment and preserve rollback history.
	case "deploy":
		c.recordDeployment(op, result)
	// Update resource lifecycle state and approved purge effects.
	case "resource-action":
		c.recordResourceAction(op)
	// Provisioning and allocation maintenance already persisted the resource state.
	case "resource", "resource-resize":
	// Update application lifecycle state for the remaining action kind.
	default:
		c.recordApplicationAction(op)
	}
}

// recordDeployment appends an immutable rollback revision and activates the new deployment.
func (c *Controller) recordDeployment(op *Operation, result operationResult) {
	deployment := result.deployment
	deployment.Name = op.Spec.Name
	deployment.URL = op.Spec.Route
	deployment.Status = "running"
	// Scaling to zero persists the stopped state across controller restarts.
	if model.DesiredReplicas(op.Spec) == 0 {
		deployment.Status = "stopped"
	}
	revisions := []Revision{}
	// Preserve previous revisions so rollback remains available after replacement.
	if prior := c.state.Applications[op.Name]; prior != nil {
		revisions = prior.Revisions
	}
	revision := Revision{ResourceRefs: result.resourceRefs, ID: op.ID, Spec: op.Spec, SecretRefs: op.SecretRefs}
	revisions = append(revisions, revision)
	c.state.Applications[op.Name] = &Application{ProductPolicy: c.nextProductPolicy(op.Spec), ResourceRefs: result.resourceRefs, Deployment: deployment, Spec: op.Spec, SecretRefs: op.SecretRefs, Revision: op.ID, Revisions: revisions}
}

// actionStatus maps lifecycle actions to their durable observed state.
func actionStatus(action string) string {
	// Translate the completed lifecycle action into retained resource state.
	switch action {
	// Keep ordinary deletion visible as a retained deleted record.
	case "delete":
		return "deleted"
	// Record stopped instances without discarding their data.
	case "stop":
		return "stopped"
	// Treat successful start and restart actions as running.
	default:
		return "running"
	}
}

// recordResourceAction retains data and credentials unless purge was explicitly approved.
func (c *Controller) recordResourceAction(op *Operation) {
	c.state.Resources[op.Name].Status = actionStatus(op.Action)
	// Discard retained data only after explicit purge approval.
	if op.Purge {
		delete(c.state.ResourceCredentials, op.Name)
		delete(c.state.Resources, op.Name)
	}
}

// recordApplicationAction retains rollback history until an explicit data purge.
func (c *Controller) recordApplicationAction(op *Operation) {
	// Update lifecycle state only for an application that still exists.
	if app := c.state.Applications[op.Name]; app != nil {
		if op.Purge {
			// A successful runtime purge retires the app and its controller-owned secrets together.
			delete(c.state.Applications, op.Name)
			for name, secret := range c.state.Secrets {
				if secret.Owner == op.Name {
					c.deleteSecretState(name)
				}
			}
			return
		}
		app.Status = actionStatus(op.Action)
	}
}

// supersedeInterrupted releases obsolete route reservations after successful recovery or deletion.
func (c *Controller) supersedeInterrupted(op *Operation) {
	// Only a replacement deployment or deletion can release interrupted reservations.
	if op.Kind != "deploy" && !(op.Kind == "action" && op.Action == "delete") {
		return
	}
	// Inspect accepted operations as well as active state before allowing conflicting work.
	for _, prior := range c.state.Operations {
		// Release only older interrupted deployments belonging to the replaced application.
		if prior.ID != op.ID && prior.Kind == "deploy" && prior.Name == op.Name && prior.Status == "interrupted" {
			prior.Status = "superseded"
			prior.SupersededBy = op.ID
			prior.Error = &model.Error{Code: "SUPERSEDED", Message: "A later approved operation replaced this interrupted deployment"}
		}
	}
}
