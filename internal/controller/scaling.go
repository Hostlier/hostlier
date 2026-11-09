package controller

import (
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// validateDeploymentRuntime rejects unsupported replica settings before any package planning.
func (c *Controller) validateDeploymentRuntime(spec model.Project) error {
	// Delegation is limited to the native Kubernetes autoscaler.
	if spec.Autoscaling != nil && c.opts.Runtime.Name() != "k3s" {
		return model.Err("AUTOSCALING_RUNTIME", "Autoscaling requires k3s")
	}
	// Automatic replica bounds must remain inside a durable product resource contract.
	if spec.Autoscaling != nil && c.retainedProductPolicy(spec) == nil {
		return model.Err("PRODUCT_POLICY", "Autoscaling requires an explicit retained product policy")
	}
	// The simple Docker backend supports one app instance or intentional scale-to-zero.
	if c.opts.Runtime.Name() == "docker" && model.DesiredReplicas(spec) > 1 {
		return model.Err("UNSUPPORTED_REPLICAS", "Multiple application replicas require k3s")
	}
	return nil
}

// scaleSpecification preserves all non-resource settings and exact secret revisions.
func (c *Controller) scaleSpecification(spec model.Project) (model.Project, map[string]SecretRef, error) {
	app := c.state.Applications[spec.Name]
	// Scaling cannot create an app or revive an explicitly deleted app by accident.
	if app == nil || app.Status == "deleted" {
		return spec, nil, model.Err("NOT_FOUND", "Application not found")
	}
	prior, err := config.ValidateProject(app.Spec)
	// Invalid retained desired state requires inspection rather than a partial scale.
	if err != nil {
		return spec, nil, err
	}
	prior.Resources = spec.Resources
	prior.Replicas = spec.Replicas
	// Manual scale explicitly revokes delegation; only deploy can approve it again.
	prior.SleepAfter = ""
	prior.Autoscaling = nil
	spec.SleepAfter = ""
	spec.Autoscaling = nil
	// Changes to images, routes, dependencies or workflow inputs need an ordinary deployment.
	if config.Fingerprint(prior) != config.Fingerprint(spec) {
		return spec, nil, model.Err("INVALID_SCALE", "Scale may change only CPU, memory and replicas")
	}
	return spec, app.SecretRefs, nil
}

// resourceChanges displays current and proposed resource limits in every approval preview.
func (c *Controller) resourceChanges(spec model.Project) map[string]any {
	proposed := model.DesiredReplicas(spec)
	change := map[string]any{"proposed": spec.Resources, "proposedReplicas": proposed, "proposedAggregate": aggregateCapacity(spec, proposed), "runtime": c.state.Runtime, "route": spec.Route, "rollout": "Create ready replacement capacity, verify route, then gracefully drain and retain the source workload"}
	// Explicit deploy approval authorizes the complete bounded native policy.
	if spec.SleepAfter != "" {
		change["idleScaling"] = map[string]any{"sleepAfter": spec.SleepAfter, "idleReplicas": 0, "activeReplicas": 1, "wake": "First HTTP request waits for readiness and is forwarded once"}
	}
	// Show native HPA delegation separately from HTTP request-driven sleep.
	if spec.Autoscaling != nil {
		change["autoscalingDelegation"] = map[string]any{"policy": spec.Autoscaling, "requests": spec.Requests, "maximumCapacity": aggregateCapacity(spec, spec.Autoscaling.MaxReplicas), "behavior": "+1 per 60 seconds; -1 per 300 seconds; 300 second downscale stabilization", "revocation": "Manual scale, stop, restart, rollback or replacement pauses HPA before mutation; deploy explicitly approves resumption", "persistence": "HPA continues while Hostlier is down; no automatic expiry guarantee; metrics missing behavior follows Kubernetes"}
	}
	// Zero withdraws traffic while retaining routing, desired configuration and all data.
	if proposed == 0 {
		change["rollout"] = "Withdraw traffic, gracefully stop running capacity, retain route and data; no automatic wake"
	}
	// Existing desired state makes resource reversals and temporary overlap reviewable.
	if app := c.state.Applications[spec.Name]; app != nil {
		current := model.DesiredReplicas(app.Spec)
		// HPA may already own any count up to its approved maximum during overlap.
		if app.Spec.Autoscaling != nil {
			current = app.Spec.Autoscaling.MaxReplicas
		}
		change["current"] = app.Spec.Resources
		change["currentReplicas"] = current
		change["currentAggregate"] = aggregateCapacity(app.Spec, current)
		change["replacementSource"] = map[string]any{"runtimeId": app.RuntimeID, "revision": app.Revision, "retainedAfterCutover": true}
		change["temporaryOverlap"] = map[string]any{"sourceReplicas": current, "candidateReplicas": proposed, "peakReplicas": current + proposed, "sourceCapacity": aggregateCapacity(app.Spec, current), "candidateCapacity": aggregateCapacity(spec, proposed)}
	}
	// Local persistent storage requires an exclusive-writer maintenance window.
	if len(spec.Volumes) > 0 {
		change["storageConstraint"] = "Single writer; stop the source before mounting its retained volume; readiness failure restores the prior writer"
		change["rollout"] = "Exclusive writer replacement with a maintenance window; retained data is never resized or deleted"
		delete(change, "temporaryOverlap")
	}
	return change
}
