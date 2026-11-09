package controller

import (
	"net/http"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// makePlan resolves immutable deployment inputs without changing the runtime.
func (c *Controller) makePlan(r *http.Request, actor string) (any, error) {
	spec, exact, err := c.planSpecification(r)
	// Stop planning when immutable runtime inputs cannot be resolved.
	if err != nil {
		return nil, err
	}
	// Enforce retained product constraints before provisioning any dependencies.
	if err := c.validateProductPolicy(spec); err != nil {
		return nil, err
	}
	dependencies, err := c.prepareDependencies(r.Context(), spec)
	// Do not approve a deployment whose private dependencies cannot be resolved.
	if err != nil {
		return nil, err
	}
	// Reject this path: Application has active operation.
	if c.busy(spec.Name) {
		return nil, model.Err("APP_BUSY", "Application has active operation")
	}
	route, err := c.routeFree(spec)
	// Stop planning when immutable runtime inputs cannot be resolved.
	if err != nil {
		return nil, err
	}
	spec.Route = route.URL
	syncEndpointRoute(&spec)
	refs, err := c.references(spec, exact)
	// Stop planning when immutable runtime inputs cannot be resolved.
	if err != nil {
		return nil, err
	}
	spec, warnings, err := c.pinDeploymentWorkload(r, spec)
	// Hook review must use images and infrastructure the server will actually deploy.
	if err != nil {
		return nil, err
	}
	// Server-side deployment hooks review the same pinned project as the approved operation.
	workflowPlans, err := c.prepareWorkflow(r.Context(), spec)
	// An unplannable hook must not create an approvable deployment.
	if err != nil {
		return nil, err
	}
	strategy := "replace"
	// Explain the interruption needed to retain mounted application data.
	if model.HasVolumes(spec) {
		strategy = "stop-start with interruption; data retained"
	}
	plan := &Plan{Workflow: workflowPlans, Dependencies: dependencies, ID: random(), Spec: spec, SecretRefs: refs, Generation: c.state.Generation, ExpiresAt: c.opts.Now().Add(c.opts.PlanTTL).UnixMilli(), Actor: actor, Changes: map[string]any{"productPolicy": c.nextProductPolicy(spec), "runtime": c.state.Runtime, "url": route.URL, "image": spec.Image, "secrets": refs, "volumes": spec.Volumes, "storageQuotaEnforced": false, "updateStrategy": strategy, "dependencies": dependencies, "workflow": workflowPlans, "warnings": warnings, "namespaceImage": spec.NamespaceImage, "sidecars": spec.Sidecars, "endpoint": spec.Endpoint, "endpoints": spec.Endpoints, "configs": spec.Configs, "security": spec.Security, "shutdown": spec.Shutdown, "resources": c.resourceChanges(spec)}}
	plan.StatefulSource = c.statefulSource(spec.Name)
	plan.Fingerprint = config.Fingerprint(plan)
	c.state.Plans[plan.ID] = plan
	copy := *plan
	copy.Actor = ""
	return &copy, nil
}

// pinDeploymentWorkload resolves all application and namespace images before hook review.
func (c *Controller) pinDeploymentWorkload(r *http.Request, spec model.Project) (model.Project, []string, error) {
	spec, warnings, err := c.pinWorkload(r, spec)
	// A mutable group member invalidates the entire deployment input.
	if err != nil {
		return spec, nil, err
	}
	// Validate resource and service access before selecting runtime infrastructure.
	if err := c.validateWorkloadReferences(spec); err != nil {
		return spec, nil, err
	}
	spec, err = c.pinNamespaceImage(r, spec)
	return spec, warnings, err
}

// applyPlan validates approval and durably accepts exactly one operation per request.
func (c *Controller) applyPlan(r *http.Request, id, actor string) (any, error) {
	plan, b, err := c.approvedPlan(r, id, actor)
	// Require verified actor ownership and matching approval before accepting work.
	if err != nil {
		return nil, err
	}
	// Reject this path: Plan already applied.
	if plan.OperationID != "" {
		// Reject this path: Plan already applied.
		if plan.RequestID != b.RequestID {
			return nil, model.Err("PLAN_CONSUMED", "Plan already applied")
		}
		return map[string]string{"operationId": plan.OperationID}, nil
	}
	// Reject this path: Plan expired or server changed; review new plan.
	if plan.ExpiresAt <= c.opts.Now().UnixMilli() || plan.Generation != c.state.Generation {
		return nil, model.Err("STALE_PLAN", "Plan expired or server changed; review new plan")
	}
	// Recheck public route reservations only for application deployments.
	if !resourcePlanKind(plan.Kind) {
		// Preserve route reservations held by other active or accepted applications.
		if _, err := c.routeFree(plan.Spec); err != nil {
			return nil, err
		}
	}
	kind, name := "deploy", plan.Spec.Name
	// Use the workflow corresponding to the accepted operation kind.
	if resourcePlanKind(plan.Kind) {
		kind = plan.Kind
		name = plan.ResourceSpec.Name
	}
	op := &Operation{Workflow: plan.Workflow, RestoreBackupID: plan.RestoreBackupID, Dependencies: plan.Dependencies, ResourceSpec: plan.ResourceSpec, ID: random(), Kind: kind, Name: name, Spec: plan.Spec, SecretRefs: plan.SecretRefs, Status: "queued", PlanID: plan.ID, Fingerprint: plan.Fingerprint, Actor: actor}
	plan.OperationID = op.ID
	plan.RequestID = b.RequestID
	return c.acceptOperation(op)
}

// deploymentPlanRequest bounds the recognized JSON fields for this API action.
type deploymentPlanRequest struct {
	Spec     model.Project `json:"spec"`
	Action   string        `json:"action"`
	Revision string        `json:"revision"`
}

// planSpecification validates deployment intent and resolves rollback inputs when requested.
func (c *Controller) planSpecification(r *http.Request) (model.Project, map[string]SecretRef, error) {
	var b deploymentPlanRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return model.Project{}, nil, err
	}
	// Reject this path: Unknown deployment action.
	if b.Action != "" && b.Action != "deploy" && b.Action != "rollback" && b.Action != "scale" {
		return model.Project{}, nil, model.Err("INVALID_ACTION", "Unknown deployment action")
	}
	spec, err := validatePlanProject(b)
	// Reject invalid project inputs before planning any runtime work.
	if err != nil {
		return model.Project{}, nil, err
	}
	// Runtime capability checks happen before approval or provisioning.
	if err := c.validateDeploymentRuntime(spec); err != nil {
		return spec, nil, err
	}
	// Scaling may change only the existing app resource allocation.
	if b.Action == "scale" {
		return c.scaleSpecification(spec)
	}
	var exact map[string]SecretRef
	// Resolve immutable rollback inputs when rollback was explicitly requested.
	if b.Action == "rollback" {
		return c.rollbackSpecification(spec, b.Revision)
	}

	return spec, exact, nil
}

// rollbackSpecification selects the retained specification and secret revisions of an exact prior deployment.
func (c *Controller) rollbackSpecification(spec model.Project, revisionID string) (model.Project, map[string]SecretRef, error) {
	var exact map[string]SecretRef

	a := c.state.Applications[spec.Name]
	found := false
	// Search retained history only when the application exists.
	if a != nil {
		// Inspect retained revisions because rollback still depends on their immutable inputs.
		for _, rev := range a.Revisions {
			// Select the exact revision the caller requested.
			if rev.ID == revisionID {
				spec = rev.Spec
				// Rollback restores reviewed manual capacity and never renews autonomous authority.
				spec.SleepAfter = ""
				spec.Autoscaling = nil
				exact = rev.SecretRefs
				found = true
			}
		}
	}
	// Reject this path: Revision not found.
	if !found {
		return model.Project{}, nil, model.Err("NOT_FOUND", "Revision not found")
	}

	return spec, exact, nil
}

// immutableImage pins an image to a resolved digest before presenting it for approval.
func immutableImage(spec model.Project, digest string) (model.Project, error) {
	// Use a resolved digest when the runtime supplied one.
	if digest != "" {
		// Preserve a complete canonical image reference returned by the runtime.
		if strings.Contains(digest, "@") {
			spec.Image = digest
		} else {
			spec.Image = strings.Split(spec.Image, "@")[0] + "@" + digest
		}
	}
	// Reject this path: Image must resolve to immutable digest.
	if !strings.Contains(spec.Image, "@sha256:") {
		return model.Project{}, model.Err("IMAGE_UNRESOLVED", "Image must resolve to immutable digest")
	}

	// The source must identify the same approved content when revalidated.
	if spec.Source != nil {
		spec.Source.Image = spec.Image
	}
	return spec, nil
}

// approvalRequest bounds the recognized JSON fields for this API action.
type approvalRequest struct {
	Fingerprint string `json:"fingerprint"`
	Approved    bool   `json:"approved"`
	RequestID   string `json:"requestId"`
}

// approvedPlan requires actor ownership and explicit approval of the matching plan fingerprint.
func (c *Controller) approvedPlan(r *http.Request, id, actor string) (*Plan, approvalRequest, error) {
	var b approvalRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return nil, b, err
	}
	plan := c.state.Plans[id]
	// Reject this path: Plan not found.
	if plan == nil || plan.Actor != actor {
		return nil, b, model.Err("NOT_FOUND", "Plan not found")
	}
	// Reject this path: Matching explicit approval required.
	if !b.Approved || b.Fingerprint != plan.Fingerprint {
		return nil, b, model.Err("APPROVAL_REQUIRED", "Matching explicit approval required")
	}
	// Require a nonempty bounded identity before continuing.
	if err := required(b.RequestID); err != nil {
		return nil, b, err
	}

	return plan, b, nil
}

// pinNamespaceImage resolves Docker's stable network holder before approval.
func (c *Controller) pinNamespaceImage(r *http.Request, spec model.Project) (model.Project, error) {
	// Docker group namespaces use an immutable, reviewed infrastructure image.
	if spec.Version == 2 && c.opts.Runtime.Name() == "docker" {
		info, e := c.opts.Runtime.InspectImage(r.Context(), "registry.k8s.io/pause:3.10")
		// Namespace ownership must not depend on a mutable image after approval.
		if e != nil {
			return spec, e
		}
		pinned, e := immutableImage(model.Project{Image: "registry.k8s.io/pause:3.10"}, info.Digest)
		// Reject unresolved infrastructure images alongside application images.
		if e != nil {
			return spec, e
		}
		spec.NamespaceImage = pinned.Image
	}
	return spec, nil
}

// syncEndpointRoute retains one canonical route across the versioned schema fields.
func syncEndpointRoute(spec *model.Project) {
	// The selected named listener follows canonical primary-route normalization.
	if len(spec.Endpoints) > 0 {
		key := model.PrimaryEndpointName(*spec)
		e := spec.Endpoints[key]
		e.Route = spec.Route
		spec.Endpoints[key] = e
	}
	// Legacy projects have no endpoint declaration to normalize.
	if spec.Endpoint != nil {
		spec.Endpoint.Route = spec.Route
	}
}

// statefulSource prevents removing existing volumes or dependencies through a stateless delegation.
func (c *Controller) statefulSource(name string) bool {
	app := c.state.Applications[name]
	return app != nil && (model.HasVolumes(app.Spec) || len(app.ResourceRefs) > 0 || app.Spec.Workflow != nil)
}
