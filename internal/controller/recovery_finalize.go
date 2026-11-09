package controller

import (
	"context"
	"errors"
	"hostlier/internal/model"
	"hostlier/internal/recovery"
	"hostlier/internal/store"
	"net/http"
	"time"
)

// recoveryJobAPI keeps cutover and rollback-to-source outside the initial upgrade approval.
func (c *Controller) recoveryJobAPI(r *http.Request, p []string) (any, error) {
	// Advance only after the required recovery invariant holds.
	if r.Method == "GET" && len(p) == 2 {
		jobs, err := collectionMap[*RecoveryJob](r.Context(), c.store, "recoveryJobs", store.CollectionQuery{})
		return map[string]any{"jobs": jobs}, err
	}
	// Advance only after the required recovery invariant holds.
	if len(p) < 3 {
		return endpointNotFound()
	}
	job := c.state.RecoveryJobs[p[2]]
	// Advance only after the required recovery invariant holds.
	if job == nil {
		return endpointNotFound()
	}
	// Advance only after the required recovery invariant holds.
	if r.Method == "GET" && len(p) == 3 {
		return job, nil
	}
	// Advance only after the required recovery invariant holds.
	if r.Method != "POST" || len(p) != 4 {
		return endpointNotFound()
	}
	return c.finalizeRecoveryRequest(r, job, p[3])
}

// validateFinalization refuses rollback after any cutover attempt could have exposed the target to writes.
func validateFinalization(job *RecoveryJob, action string) error {
	// Reject this request: Only an upgrade can be cut over.
	if action == "cutover" {
		// Reject this request: Only an upgrade can be cut over.
		if job.Kind != "upgrade" {
			return model.Err("UPGRADE_STATE", "Only an upgrade can be cut over")
		}
		// Reject this request: Cutover requires a verified upgrade awaiting independent approval.
		if job.Status == "awaiting-cutover" || (job.CutoverStarted && job.Status == "uncertain") {
			return nil
		}
		return model.Err("UPGRADE_STATE", "Cutover requires a verified upgrade awaiting independent approval")
	}
	// Reject this request: Recovery action not found.
	if action != "abort" {
		return model.Err("NOT_FOUND", "Recovery action not found")
	}
	return validateRecoveryAbort(job)
}

// validateRecoveryAbort permits source recovery only while the new target has never been exposed.
func validateRecoveryAbort(job *RecoveryJob) error {
	// Reject this request: A cutover attempt may have exposed the target to writes; abort is no longer safe.
	if job.CutoverStarted {
		return model.Err("UPGRADE_STATE", "A cutover attempt may have exposed the target to writes; abort is no longer safe")
	}
	// Select the explicit recovery operation or native engine behavior.
	switch job.Status {
	// Handle only this supported recovery operation or retained state.
	case "awaiting-cutover", "failed", "uncertain":
		return nil
	}
	return model.Err("UPGRADE_STATE", "Only unresolved recovery work can be aborted")
}

// finalizeRecoveryRequest persists the decision boundary before starting any network or storage mutation.
func (c *Controller) finalizeRecoveryRequest(r *http.Request, job *RecoveryJob, action string) (any, error) {
	// Propagate failure before performing the next recovery side effect.
	if err := emptyBody(r); err != nil {
		return nil, err
	}
	// Propagate failure before performing the next recovery side effect.
	if err := validateFinalization(job, action); err != nil {
		return nil, err
	}
	previous := *job
	commit := action == "cutover"
	job.Status = "running"
	job.Phase = action
	// Advance only after the required recovery invariant holds.
	if commit {
		job.CutoverStarted = true
	}
	// Propagate failure before performing the next recovery side effect.
	if err := c.store.Save(c.state); err != nil {
		*job = previous
		return nil, err
	}
	copy := *job
	c.wg.Add(1)
	go c.runRecoveryFinalization(&copy, commit)
	return job, nil
}

// runRecoveryFinalization keeps failed or unpersisted outcomes reserved for explicit inspection.
func (c *Controller) runRecoveryFinalization(copy *RecoveryJob, commit bool) {
	defer c.wg.Done()
	c.execMu.Lock()
	defer c.execMu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Minute)
	defer cancel()
	err := c.finalizeRecovery(ctx, copy, commit)
	c.mu.Lock()
	defer c.mu.Unlock()
	job := c.state.RecoveryJobs[copy.ID]
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		job.Status = "uncertain"
		job.Error = "Finalization failed; both identities remain reserved for inspection"
	} else {
		job.Status = "aborted"
		job.Error = ""
		// Advance only after the required recovery invariant holds.
		if commit {
			job.Status = "succeeded"
		}
	}
	c.state.Generation++
	c.persistRecoveryOutcome(job)
}

// persistRecoveryOutcome prevents a public success from releasing reservations when the durable write failed.
func (c *Controller) persistRecoveryOutcome(job *RecoveryJob) {
	// Propagate failure before performing the next recovery side effect.
	if err := c.store.Save(c.state); err != nil {
		job.Status = "uncertain"
		job.Error = "Recovery outcome could not be persisted; inspect retained state before proceeding"
	}
}

// finalizeRecovery never reconnects the source until the isolated target is removed.
func (c *Controller) finalizeRecovery(ctx context.Context, job *RecoveryJob, commit bool) error {
	// Advance only after the required recovery invariant holds.
	if commit {
		return c.cutoverRecovery(ctx, job)
	}
	// Propagate failure before performing the next recovery side effect.
	if err := c.removeRecoveryTarget(ctx, job); err != nil {
		return err
	}
	// Advance only after the required recovery invariant holds.
	if job.Kind != "upgrade" {
		return nil
	}
	credentials, err := c.recoveryCredentials(job.Source, job.SourceSpec)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	operation := recoveryOperation(job, job.SourceSpec, credentials)
	manager := c.opts.ResourceManager.(model.DatabaseRecoveryManager)
	// Re-establish the owned fence before releasing it, including interrupted preflight attempts.
	if err = manager.Fence(ctx, job.Source, true, operation); err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = manager.Fence(ctx, job.Source, false, operation); err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.opts.ResourceManager.Action(ctx, job.Source, "start", operation); err != nil {
		return err
	}
	c.mu.Lock()
	c.state.Resources[job.Source].Status = "running"
	c.mu.Unlock()
	return nil
}

// cutoverRecovery rechecks native content and the remote recovery copy immediately before exposing the new target.
func (c *Controller) cutoverRecovery(ctx context.Context, job *RecoveryJob) error {
	destination, err := c.recoveryDestination(job.Destination, job.DestinationFingerprint)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	data, err := destination.Objects.Get(ctx, job.ObjectKey)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	bundle, err := recovery.Open(destination.Key, data)
	// Propagate failure before performing the next recovery side effect.
	if err != nil || bundle.ID != job.ID {
		return errors.New("upgrade recovery copy unavailable")
	}
	source, err := c.opts.ResourceManager.Status(ctx, job.Source)
	// Propagate failure before performing the next recovery side effect.
	if err != nil || source.Status != "stopped" {
		return errors.New("source is not fenced and stopped")
	}
	credentials, err := c.recoveryCredentials(job.Target.Name, job.Target)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	operation := recoveryOperation(job, job.Target, credentials)
	manager := c.opts.ResourceManager.(model.DatabaseRecoveryManager)
	evidence, err := manager.Evidence(ctx, job.Target.Name, operation)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = recovery.Match(bundle.Evidence, evidence, time.Now().UnixMilli()); err != nil {
		return err
	}
	return manager.Fence(ctx, job.Target.Name, false, operation)
}
