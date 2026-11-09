package controller

import (
	"hostlier/internal/config"
	"hostlier/internal/model"
	"net/http"
)

// resourceResizeRequest permits only a resource identity and two allocation quantities.
type resourceResizeRequest struct {
	Name   string `json:"name"`
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// makeResourceResizePlan binds a data-preserving maintenance preview to actor and generation.
func (c *Controller) makeResourceResizePlan(r *http.Request, actor string) (any, error) {
	var body resourceResizeRequest
	// Strict decoding rejects storage or secret mutations hidden in allocation intent.
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	resource := c.state.Resources[body.Name]
	// Resize requires an existing running resource without conflicting accepted work.
	if resource == nil || resource.Status != "running" {
		return nil, model.Err("RESOURCE_NOT_FOUND", "Running resource required for allocation maintenance")
	}
	// Accepted operations reserve the database until their durable completion.
	if c.resourceBusy(body.Name) {
		return nil, model.Err("RESOURCE_BUSY", "Resource has active operation")
	}
	capability, ok := c.opts.ResourceManager.(model.ResourceResizeManager)
	// Backends advertise allocation support independently from provisioning.
	if !ok {
		return nil, model.Err("UNSUPPORTED_OPERATION", "Resource allocation maintenance unavailable")
	}
	next := resource.Spec
	next.CPU = body.CPU
	next.Memory = body.Memory
	planned, err := capability.PlanResize(r.Context(), next)
	// No approval plan can authorize unsupported allocation changes.
	if err != nil {
		return nil, err
	}
	changes := map[string]any{"resource": planned.Changes, "dependents": c.resourceDependents(body.Name), "maintenanceApproval": "Approval explicitly accepts database interruption; dependent applications may see errors during maintenance"}
	plan := &Plan{ID: random(), Kind: "resource-resize", ResourceSpec: &planned.Spec, Changes: changes, Actor: actor, ExpiresAt: c.opts.Now().Add(c.opts.PlanTTL).UnixMilli(), Generation: c.state.Generation}
	plan.Fingerprint = config.Fingerprint(plan)
	c.state.Plans[plan.ID] = plan
	return plan, nil
}

// executeResourceResize uses stable credentials and commits only a successful allocation change.
func (c *Controller) executeResourceResize(op *Operation, onEvent func(model.Event)) error {
	c.mu.Lock()
	credentials, err := c.resourceCredentials(op.Name, *op.ResourceSpec)
	c.mu.Unlock()
	// Undecryptable credentials cannot authorize runtime allocation work.
	if err != nil {
		return err
	}
	capability, ok := c.opts.ResourceManager.(model.ResourceResizeManager)
	// Missing allocation capability cannot authorize a resource mutation.
	if !ok {
		return model.Err("UNSUPPORTED_OPERATION", "Resource allocation maintenance unavailable")
	}
	resource, err := capability.Resize(c.ctx, *op.ResourceSpec, model.ResourceContext{Credentials: credentials, OperationID: op.ID, OnEvent: onEvent})
	// A failed runtime change leaves controller desired allocation unchanged.
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	prior := c.state.Resources[op.Name]
	prior.Resource = resource
	prior.Spec = *op.ResourceSpec
	return c.store.Save(c.state)
}

// resourcePlanKind identifies approval plans that own database rather than application state.
func resourcePlanKind(kind string) bool { return kind == "resource" || kind == "resource-resize" }
