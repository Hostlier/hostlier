package runtime

import (
	"context"
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// validateReplicas rejects unsupported scheduling and storage before runtime mutation.
func validateReplicas(spec model.Project, backend string) error {
	// Direct adapter callers must honor the same idle policy as deployment plans.
	if spec.SleepAfter != "" {
		// Validate idle constraints before creating or stopping any runtime object.
		if _, err := config.ValidateProject(spec); err != nil {
			return err
		}
	}
	// Native HPA delegation is never supported by Docker.
	if spec.Autoscaling != nil && backend != "k3s" {
		return model.Err("AUTOSCALING_RUNTIME", "Autoscaling requires k3s")
	}
	count := model.DesiredReplicas(spec)
	// Negative counts never represent a valid desired state.
	if count < 0 {
		return model.Err("REPLICAS", "Replicas must be zero or greater")
	}
	// Docker intentionally supports only one container or explicit zero.
	if backend == "docker" && count > 1 {
		return model.Err("REPLICAS", "Multiple replicas require k3s")
	}
	// Local persistent storage has no multi-writer concurrency capability.
	if count > 1 && model.HasVolumes(spec) {
		return model.Err("REPLICAS", "Persistent volumes support only one replica")
	}
	return nil
}

// deployZero creates an inert approved workload while retaining configuration and data.
func (r *Runtime) deployZero(ctx context.Context, spec model.Project, dc model.DeployContext) (out model.Deployment, err error) {
	d := &dockerDeployment{runtime: r, ctx: ctx, spec: spec, dc: dc}
	// Validate immutable identity and existing route ownership before mutation.
	if err = d.prepare(); err != nil {
		return out, err
	}
	// Restore the old workload and route on any interrupted transition.
	defer func() {
		// A completed zero transition keeps the stopped desired record.
		if err != nil {
			d.rollback()
		}
	}()
	// Retain a concrete stopped container so ordinary status and deletion remain supported.
	for _, stage := range []func() error{d.pull, d.prepareVolumes, d.candidateContainer, d.activateZero, d.cleanup, d.completeTransition} {
		// Stop at the first failure and let rollback restore prior capacity.
		if err = stage(); err != nil {
			return out, err
		}
	}
	return d.result, nil
}

// activateZero withdraws traffic before gracefully stopping the previous workload.
func (d *dockerDeployment) activateZero() error {
	d.result = model.Deployment{Name: d.spec.Name, URL: d.route.URL, RuntimeID: d.candidate.ID, ImageDigest: d.spec.Image, Status: "stopped"}
	d.s.Apps[d.spec.Name] = appRecord{PreviousRuntimeID: d.previousID(), PreviousGroup: d.sourceGroup(), Deployment: d.result, Spec: d.spec, Volumes: d.volumes, OperationID: d.operation, Anchor: d.anchor, Members: d.members}
	d.activated = true
	// Withdraw live upstream routing before stopping existing capacity.
	if err := d.runtime.caddy(d.ctx, d.s); err != nil {
		return err
	}
	// Stop only the previously owned active container, retaining its data.
	if d.hasOld && d.old.RuntimeID != "" && d.old.RuntimeID != d.candidate.ID {
		// Verify ownership immediately before stopping a previous workload.
		if _, err := d.runtime.container(d.ctx, d.old.RuntimeID, d.spec.Name); err != nil {
			return err
		}
		// Allow bounded graceful termination before committing zero desired state.
		if err := d.runtime.api(d.ctx, "POST", "/containers/"+d.old.RuntimeID+"/stop?t=20", nil, nil, nil); err != nil {
			return err
		}
		d.oldStopped = d.old.Status == "running"
	}
	return d.runtime.save(d.s)
}

// zeroLifecycle prevents lifecycle commands from silently changing approved scaling intent.
func zeroLifecycle(spec model.Project, action string) bool {
	return model.DesiredReplicas(spec) == 0 && (action == "start" || action == "restart")
}
