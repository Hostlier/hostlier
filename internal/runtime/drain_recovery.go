package runtime

import (
	"context"
	"hostlier/internal/model"
)

// finishPendingDrain restores the committed route before stopping retained source capacity.
func (r *Runtime) finishPendingDrain(ctx context.Context, s state, name string, drain func() error) error {
	app := s.Apps[name]
	// Never interpret a corrupt self-reference as permission to stop the active workload.
	if app.PreviousRuntimeID == app.RuntimeID {
		return model.Err("DRAIN_PENDING", "Invalid retained source identity; inspect runtime ownership")
	}
	// Restore the committed edge after a possible crash between cutover and source shutdown.
	if err := r.caddy(ctx, s); err != nil {
		return err
	}
	// Running capacity must still serve its verified route before the old source stops.
	if app.Status == "running" && model.DesiredReplicas(app.Spec) > 0 {
		// Certificate or route failure preserves source capacity and its recovery marker.
		if err := r.publicReady(ctx, app.URL); err != nil {
			return err
		}
	}
	// Each backend verifies source ownership immediately before graceful shutdown.
	if err := drain(); err != nil {
		return err
	}
	app.PreviousRuntimeID = ""
	app.PreviousGroup = nil
	s.Apps[name] = app
	return r.save(s)
}

// recoverDrain completes a previously committed Docker cutover under the next deployment approval.
func (d *dockerDeployment) recoverDrain() error {
	// Most deployments have no interrupted source shutdown to reconcile.
	if d.old.PreviousRuntimeID == "" {
		return nil
	}
	// Corrupt self-references never acquire cleanup authority, even on a restored clean host.
	if d.old.PreviousRuntimeID == d.old.RuntimeID {
		return model.Err("DRAIN_PENDING", "Invalid retained source identity; inspect runtime ownership")
	}
	current, err := d.runtime.container(d.ctx, d.old.RuntimeID, d.spec.Name)
	// A verified restored host can discard a drain only when neither recorded writer exists.
	if d.runtime.recoveredMissingWriter(err) {
		return d.recoverAbsentDrain()
	}
	// Missing or unowned active capacity cannot justify stopping the fallback source.
	if err != nil {
		return err
	}
	// A committed running candidate must still be running before its source is stopped.
	if d.old.Status == "running" && !current.State.Running {
		return model.Err("DRAIN_PENDING", "Active replacement is not running; source remains retained")
	}
	// Preserve the marker until edge validation, source shutdown and durable commit all succeed.
	if err = d.runtime.finishPendingDrain(d.ctx, d.s, d.spec.Name, d.stopPendingSource); err != nil {
		return err
	}
	d.old = d.s.Apps[d.spec.Name]
	return nil
}

// stopPendingSource verifies the retained Docker source and treats prior shutdown or removal idempotently.
func (d *dockerDeployment) stopPendingSource() error {
	source, err := d.runtime.container(d.ctx, d.old.PreviousRuntimeID, d.spec.Name)
	// An independently removed source has no remaining capacity to drain.
	if hasCode(err, "HTTP_404") {
		return d.stopSourceGroup(d.ctx)
	}
	// Ownership and daemon failures must not be mistaken for absence.
	if err != nil {
		return err
	}
	// A previous successful stop may precede an interrupted metadata commit.
	if !source.State.Running {
		return d.stopSourceGroup(d.ctx)
	}
	// The primary must stop successfully before any retained dependencies are drained.
	if err = d.runtime.api(d.ctx, "POST", "/containers/"+source.ID+"/stop?t=20", nil, nil, nil); err != nil {
		return err
	}
	return d.stopSourceGroup(d.ctx)
}

// recoverDrain completes a committed Kubernetes cutover before a fresh version can replace its marker.
func (d *k3sDeployment) recoverDrain() error {
	// Ordinary deployments have no retained source shutdown to reconcile.
	if d.old.PreviousRuntimeID == "" {
		return nil
	}
	k := d.runtime
	current, err := k.get(d.ctx, "deployment", d.old.RuntimeID, d.namespace)
	// Source retirement requires independently verified current application ownership.
	if err != nil {
		return err
	}
	// A replacement from another owner cannot authorize retiring this app's source.
	if !k.ownsApplication(current, d.spec.Name) {
		return model.Err("OWNERSHIP", "Replacement deployment ownership unavailable")
	}
	// Positive committed capacity must be ready before its fallback source is retired.
	if d.old.Status == "running" && model.DesiredReplicas(d.old.Spec) > 0 {
		// Failed rollout observation leaves the durable source marker untouched.
		if err = k.ready(d.ctx, d.namespace, d.old.RuntimeID); err != nil {
			return err
		}
	}
	// Only the completed drain may clear the previous source identity.
	if err = k.base.finishPendingDrain(d.ctx, d.s, d.spec.Name, d.stopPendingSource); err != nil {
		return err
	}
	d.old = d.s.Apps[d.spec.Name]
	return nil
}

// ownsApplication verifies both the server and application labels of a Kubernetes object.
func (k *K3s) ownsApplication(object map[string]any, name string) bool {
	metadata, _ := object["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	return k.owned(object) && labels["io.hostlier.app"] == name
}

// stopPendingSource checks retained Kubernetes ownership immediately before scaling it to zero.
func (d *k3sDeployment) stopPendingSource() error {
	k := d.runtime
	source, err := k.get(d.ctx, "deployment", d.old.PreviousRuntimeID, d.namespace)
	// An absent version has no remaining source replicas to retire.
	if err != nil || source == nil {
		return err
	}
	// Never scale an object whose ownership changed after the original cutover.
	if !k.ownsApplication(source, d.spec.Name) {
		return model.Err("OWNERSHIP", "Retained source deployment is not owned by this application")
	}
	return k.drain(d.ctx, d.namespace, d.old.PreviousRuntimeID)
}

// recoverAbsentDrain clears restored ownership only after observing both recorded writers absent.
func (d *dockerDeployment) recoverAbsentDrain() error {
	r := d.runtime
	_, err := r.container(d.ctx, d.old.PreviousRuntimeID, d.spec.Name)
	// A surviving, foreign or uninspectable source must never be forgotten during clean-host recovery.
	if !r.recoveredMissingWriter(err) {
		// A surviving owned source still requires deliberate recovery rather than a guessed handoff.
		if err == nil {
			return model.Err("DRAIN_PENDING", "Recovered source still exists; inspect before replacement")
		}
		return err
	}
	app := d.old
	app.PreviousRuntimeID = ""
	app.RuntimeID = ""
	app.Anchor = ""
	app.Members = nil
	d.s.Apps[d.spec.Name] = app
	// Do not lose recovery evidence in memory when persisting the cleared marker fails.
	if err := r.save(d.s); err != nil {
		d.s.Apps[d.spec.Name] = d.old
		return err
	}
	d.old = app
	return nil
}
