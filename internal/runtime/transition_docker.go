package runtime

import (
	"hostlier/internal/model"
	"net"
)

// prepareRecovery repairs interrupted v1 work under the next explicit deployment approval.
func (d *dockerDeployment) prepareRecovery() error {
	// Workload groups remain owned by their separate version-two implementation.
	if d.spec.Version != 1 {
		return d.recoverDrain()
	}
	intent, err := d.runtime.readIntent(d.spec.Name)
	// Invalid recovery evidence cannot authorize guessing a candidate identity.
	if err != nil {
		return err
	}
	// A committed candidate is retained; only uncommitted attempts are rolled back.
	if intent != nil && d.old.OperationID != intent.Operation {
		// Restore known source state before creating any new candidate.
		if err = d.recoverUncommitted(intent); err != nil {
			return err
		}
	}
	// Complete a committed cutover's pending source drain before replacing its marker.
	if err = d.recoverDrain(); err != nil {
		return err
	}
	return d.runtime.beginIntent(d.spec, d.operation)
}

// recoverUncommitted removes only the interrupted operation's verified container and restores its source.
func (d *dockerDeployment) recoverUncommitted(intent *deploymentIntent) error {
	r := d.runtime
	// Remove only the verified interrupted writer before restarting retained capacity.
	if err := d.removeInterruptedCandidate(intent); err != nil {
		return err
	}
	var err error
	// Previously running capacity resumes; intentionally stopped capacity stays stopped.
	if d.hasOld && d.old.Status == "running" {
		// Backend inspection and health prevent routing to a failed source.
		if err = d.restoreCommittedSource(); err != nil {
			return err
		}
	}
	// The durable source record is authoritative if a crash switched only the proxy.
	if err = r.caddy(d.ctx, d.s); err != nil {
		return err
	}
	return r.save(d.s)
}

// restoreCommittedSource independently verifies ownership and restores the previous healthy Docker writer.
func (d *dockerDeployment) restoreCommittedSource() error {
	r := d.runtime
	source, err := r.container(d.ctx, d.old.RuntimeID, d.spec.Name)
	// Verified clean-server recovery may have desired state without a surviving local writer.
	if r.recoveredMissingWriter(err) {
		return nil
	}
	// Missing or foreign source capacity otherwise requires operator investigation.
	if err != nil {
		return err
	}
	// Start only a source that was approved to run before the interrupted transition.
	if !source.State.Running {
		// A failed restart leaves the journal for another explicit recovery attempt.
		if err = r.api(d.ctx, "POST", "/containers/"+source.ID+"/start", nil, nil, nil); err != nil {
			return err
		}
	}
	port, err := r.backendPort(d.ctx, source.ID, d.old.Spec.Port)
	// Resolve actual placement rather than trusting an outdated private port.
	if err != nil {
		return err
	}
	// Require the old application's readiness before exposing its route again.
	if err = r.healthy(d.ctx, source.ID, port, healthPath(d.old.Spec), r.options.HealthTimeout); err != nil {
		return err
	}
	d.old.Upstream = net.JoinHostPort(r.options.UpstreamHost, port)
	d.s.Apps[d.spec.Name] = d.old
	return nil
}

// completeTransition drops the recovery journal only after activation and any required source drain.
func (d *dockerDeployment) completeTransition() error {
	// A cleanup failure retains evidence without rolling back already committed traffic.
	_ = d.runtime.finishIntent(d.spec, d.s)
	return nil
}

// removeInterruptedCandidate confines destructive recovery to the exact uncommitted operation.
func (d *dockerDeployment) removeInterruptedCandidate(intent *deploymentIntent) error {
	r := d.runtime
	candidate, err := r.container(d.ctx, intent.Candidate, d.spec.Name)
	// A missing candidate means the crash preceded creation or cleanup already completed.
	if err != nil && !hasCode(err, "HTTP_404") {
		return err
	}
	// An existing candidate must carry the original operation label before deletion.
	if err == nil {
		// Protect the recorded active instance and any independently replaced container.
		if candidate.ID == d.old.RuntimeID || candidate.Config.Labels["io.hostlier.operation"] != intent.Operation {
			return model.Err("OWNERSHIP", "Interrupted candidate ownership mismatch")
		}
		// Stop the uncommitted writer before restoring access to shared persistent data.
		if err = r.api(d.ctx, "DELETE", "/containers/"+candidate.ID+"?force=true", nil, nil, nil); err != nil {
			return err
		}
	}
	return nil
}
