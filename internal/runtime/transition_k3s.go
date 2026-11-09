package runtime

import "hostlier/internal/model"

// prepareRecovery repairs interrupted single-workload Kubernetes transitions before a new approved revision.
func (d *k3sDeployment) prepareRecovery() error {
	// The separate workload-group implementation owns its version-two recovery semantics.
	if d.spec.Version != 1 {
		return d.recoverDrain()
	}
	intent, err := d.runtime.base.readIntent(d.spec.Name)
	// Invalid journals must not become permission to remove cluster objects.
	if err != nil {
		return err
	}
	// Preserve committed versions and roll back only interrupted precommit work.
	if intent != nil && d.old.OperationID != intent.Operation {
		// Restore retained source traffic before creating the next candidate.
		if err = d.recoverUncommitted(intent); err != nil {
			return err
		}
	}
	// Committed traffic changes may still need a previous source drain.
	if err = d.recoverDrain(); err != nil {
		return err
	}
	return d.runtime.base.beginIntent(d.spec, d.dc.OperationID)
}

// recoverUncommitted removes the operation-owned candidate before restoring the committed Service and writer.
func (d *k3sDeployment) recoverUncommitted(intent *deploymentIntent) error {
	k := d.runtime
	// Version names derive from the exact reviewed operation and namespace ownership was checked.
	for _, kind := range []string{"deployment", "secret"} {
		// Every deletion rechecks server ownership and retains application PVCs.
		if err := d.removeInterruptedObject(kind, intent); err != nil {
			return err
		}
	}
	// Wait for asynchronous Kubernetes garbage collection before reactivating a volume writer.
	if _, err := k.run(d.ctx, []string{"wait", "--for=delete", "pods", "-n", d.namespace, "-l", "io.hostlier.version=" + intent.Candidate, "--timeout=60s"}, nil); err != nil {
		return err
	}
	// Existing applications restore their previous replica count and private endpoint.
	if d.hasOld {
		// An independently missing or changed source cannot be guessed into readiness.
		if err := d.restoreCommittedSource(); err != nil {
			return err
		}
	} else {
		// A failed first deployment has no prior service reservation in runtime metadata.
		if err := k.remove(d.ctx, "service", "app", d.namespace); err != nil {
			return err
		}
	}
	return k.base.caddy(d.ctx, d.s)
}

// restoreCommittedSource verifies existing application ownership before restoring its approved capacity.
func (d *k3sDeployment) restoreCommittedSource() error {
	k := d.runtime
	source, err := k.get(d.ctx, "deployment", d.old.RuntimeID, d.namespace)
	// Foreign or missing source state cannot authorize a recovery scale operation.
	if err != nil {
		return err
	}
	// Require application identity in addition to the server's namespace ownership.
	if !k.ownsApplication(source, d.spec.Name) {
		return model.Err("OWNERSHIP", "Committed source ownership unavailable")
	}
	// Restore the original dependency isolation policy before waiting for application health.
	if err = d.restoreSourcePolicy(); err != nil {
		return err
	}
	count := 0
	// Intentionally stopped capacity must remain stopped during failed-scale recovery.
	if d.old.Status == "running" {
		count = model.DesiredReplicas(d.old.Spec)
	}
	// Scaling to the previous approved count is idempotent after a partially completed recovery.
	if err = k.scale(d.ctx, d.namespace, d.old.RuntimeID, count); err != nil {
		return err
	}
	// Zero does not require pods to become ready.
	if count > 0 {
		// Restore traffic only when the original source is ready again.
		if err = k.ready(d.ctx, d.namespace, d.old.RuntimeID); err != nil {
			return err
		}
	}
	_, err = k.groupService(d.ctx, d.old.Spec, d.old.RuntimeID)
	return err
}

// completeTransition retires the Kubernetes journal only after the active version is durably committed.
func (d *k3sDeployment) completeTransition() error {
	// A cleanup failure retains evidence without rolling back already committed traffic.
	_ = d.runtime.base.finishIntent(d.spec, d.s)
	return nil
}

// removeInterruptedObject verifies the application label before deleting a journal-owned version.
func (d *k3sDeployment) removeInterruptedObject(kind string, intent *deploymentIntent) error {
	k := d.runtime
	object, err := k.get(d.ctx, kind, intent.Candidate, d.namespace)
	// API errors cannot establish absence or authorize cleanup.
	if err != nil {
		return err
	}
	// Already removed candidates make repeated recovery idempotent.
	if object == nil {
		return nil
	}
	// Require the exact application, not merely a resource owned somewhere on this server.
	if !k.ownsApplication(object, d.spec.Name) {
		return model.Err("OWNERSHIP", "Interrupted candidate application ownership mismatch")
	}
	return k.remove(d.ctx, kind, intent.Candidate, d.namespace)
}

// restoreSourcePolicy repairs the committed version's egress before restoring its Service.
func (d *k3sDeployment) restoreSourcePolicy() error {
	return d.runtime.restoreIsolationPolicy(d.ctx, d.old)
}
