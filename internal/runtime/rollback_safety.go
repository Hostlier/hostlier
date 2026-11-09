package runtime

import "context"

// releaseRollbackCandidate verifies candidate removal before an old volume writer may resume.
func (d *dockerDeployment) releaseRollbackCandidate(ctx context.Context) error {
	// Replaying a committed operation must never delete the active recorded workload.
	if d.candidate.ID == "" || d.candidate.ID == d.old.RuntimeID {
		return nil
	}
	err := d.runtime.api(ctx, "DELETE", "/containers/"+d.candidate.ID+"?force=true", nil, nil, nil)
	// A completed removal or authoritative absence both prove this candidate cannot keep writing.
	if hasCode(err, "HTTP_404") {
		return nil
	}
	return err
}

// releaseRollbackCandidate waits for all candidate pods before restoring the source PVC writer.
func (d *k3sDeployment) releaseRollbackCandidate(ctx context.Context) error {
	// An idempotent replay may reference the current committed active workload.
	if d.hasOld && d.old.RuntimeID == d.version {
		return nil
	}
	k := d.runtime
	// Deletion failure must preserve the journal and cannot authorize source reactivation.
	if err := k.remove(ctx, "deployment", d.version, d.namespace); err != nil {
		return err
	}
	// Deployment deletion can precede asynchronous pod termination on a local-path volume.
	if _, err := k.run(ctx, []string{"wait", "--for=delete", "pods", "-n", d.namespace, "-l", "io.hostlier.version=" + d.version, "--timeout=60s"}, nil); err != nil {
		return err
	}
	return k.remove(ctx, "secret", d.version, d.namespace)
}

// rollbackCandidate permits legacy group cleanup separately from the v1 writer safety boundary.
func (d *dockerDeployment) rollbackCandidate(ctx context.Context) bool {
	err := d.releaseRollbackCandidate(ctx)
	// Version two retains its separately owned workload-group rollback semantics.
	return d.spec.Version != 1 || err == nil
}

// rollbackCandidate isolates v1's verified pod deletion from version-two transaction behavior.
func (d *k3sDeployment) rollbackCandidate(ctx context.Context) bool {
	// Only version one adopts the new fail-closed single-writer rollback path here.
	if d.spec.Version == 1 {
		return d.releaseRollbackCandidate(ctx) == nil
	}
	// Preserve the independent workload-group cleanup implementation.
	if !d.hasOld || d.old.RuntimeID != d.version {
		_ = d.runtime.remove(ctx, "deployment", d.version, d.namespace)
		_ = d.runtime.remove(ctx, "secret", d.version, d.namespace)
	}
	return true
}
