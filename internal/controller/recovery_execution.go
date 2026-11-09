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

// recoveryCredentials retrieves only the credentials for the exact reserved resource.
func (c *Controller) recoveryCredentials(name string, spec model.ResourceSpec) (model.ResourceCredentials, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resourceCredentials(name, spec)
}

// recoveryOperation binds package selection and fence authority to the retained workflow.
func recoveryOperation(job *RecoveryJob, spec model.ResourceSpec, credentials model.ResourceCredentials) model.ResourceContext {
	return model.ResourceContext{OperationID: job.ID, PackageDigest: spec.PackageDigest, Credentials: credentials}
}

// runOffsiteBackup requires a native restore rehearsal before encrypting and publishing a recovery point.
func (c *Controller) runOffsiteBackup(ctx context.Context, job *RecoveryJob) error {
	// Reject this request: Scheduled authority was disabled or revoked before execution.
	if !c.recoveryPolicyEnabled(job.Policy) {
		return model.Err("RECOVERY_POLICY", "Scheduled authority was disabled or revoked before execution")
	}
	destination, err := c.recoveryDestination(job.Destination, job.DestinationFingerprint)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	credentials, err := c.recoveryCredentials(job.Source, job.SourceSpec)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.updateRecoveryPhase(job.ID, "native-backup"); err != nil {
		return err
	}
	data, err := c.opts.ResourceManager.(backupManager).Backup(ctx, job.Source, recoveryOperation(job, job.SourceSpec, credentials))
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	evidence, err := c.restoreRecoveryTarget(ctx, job, data)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	bundle := recovery.Bundle{ID: job.ID, Source: job.Source, CreatedAt: job.CreatedAt, Spec: job.SourceSpec, Data: data, Evidence: evidence}
	// Propagate failure before performing the next recovery side effect.
	if err = c.publishRecovery(ctx, job, destination, bundle); err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.updateRecoveryPhase(job.ID, "verification-cleanup"); err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.removeRecoveryTarget(ctx, job); err != nil {
		return err
	}
	return c.retainRecovery(ctx, job, destination)
}

// restoreRecoveryTarget creates an independent database whose data can be checked without touching the source.
func (c *Controller) restoreRecoveryTarget(ctx context.Context, job *RecoveryJob, data []byte) (model.DatabaseEvidence, error) {
	// Propagate failure before performing the next recovery side effect.
	if err := c.updateRecoveryPhase(job.ID, "target-provision"); err != nil {
		return model.DatabaseEvidence{}, err
	}
	_, credentials, err := c.provisionResource(ctx, job.Target, job.ID, nil)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return model.DatabaseEvidence{}, err
	}
	operation := recoveryOperation(job, job.Target, credentials)
	// Propagate failure before performing the next recovery side effect.
	if err = c.updateRecoveryPhase(job.ID, "target-restore"); err != nil {
		return model.DatabaseEvidence{}, err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.opts.ResourceManager.(backupManager).Restore(ctx, job.Target.Name, data, operation); err != nil {
		return model.DatabaseEvidence{}, err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.updateRecoveryPhase(job.ID, "native-verification"); err != nil {
		return model.DatabaseEvidence{}, err
	}
	return c.opts.ResourceManager.(model.DatabaseRecoveryManager).Evidence(ctx, job.Target.Name, operation)
}

// publishRecovery verifies the exact encrypted object read back from S3 before publishing a successful receipt.
func (c *Controller) publishRecovery(ctx context.Context, job *RecoveryJob, d *recovery.Destination, bundle recovery.Bundle) error {
	sealed, err := recovery.Seal(d.Key, bundle)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.updateRecoveryPhase(job.ID, "s3-upload"); err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = d.Objects.Put(ctx, job.ID, sealed); err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.updateRecoveryPhase(job.ID, "s3-readback"); err != nil {
		return err
	}
	received, err := d.Objects.Get(ctx, job.ID)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	restored, err := recovery.Open(d.Key, received)
	// Propagate failure before performing the next recovery side effect.
	if err != nil || restored.ID != job.ID || restored.SHA256 != recovery.Digest(bundle.Data) || recovery.Digest(received) != recovery.Digest(sealed) {
		return errors.New("S3 read-back verification failed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	retained := c.state.RecoveryJobs[job.ID]
	retained.ObjectKey = job.ID
	retained.Evidence = bundle.Evidence
	c.state.RecoveryPoints[job.ID] = RemoteRecoveryPoint{Key: job.ID, Policy: job.Policy, Source: job.Source, Destination: job.Destination, DestinationFingerprint: job.DestinationFingerprint, CreatedAt: job.CreatedAt, Verified: true}
	return c.store.Save(c.state)
}

// removeRecoveryTarget purges only the temporary database explicitly named in the approved workflow.
func (c *Controller) removeRecoveryTarget(ctx context.Context, job *RecoveryJob) error {
	c.mu.Lock()
	target := c.state.Resources[job.Target.Name]
	c.mu.Unlock()
	// Advance only after the required recovery invariant holds.
	if target == nil {
		return nil
	}
	credentials, err := c.recoveryCredentials(job.Target.Name, job.Target)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	op := recoveryOperation(job, job.Target, credentials)
	op.Purge = true
	// Propagate failure before performing the next recovery side effect.
	if err = c.opts.ResourceManager.Action(ctx, job.Target.Name, "delete", op); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.state.Resources, job.Target.Name)
	delete(c.state.ResourceCredentials, job.Target.Name)
	return c.store.Save(c.state)
}

// retainRecovery only deletes known verified copies after reauthenticating a surviving remote recovery point.
func (c *Controller) retainRecovery(ctx context.Context, job *RecoveryJob, d *recovery.Destination) error {
	// Propagate failure before performing the next recovery side effect.
	if err := c.updateRecoveryPhase(job.ID, "retention"); err != nil {
		return err
	}
	points, keep, err := c.retentionCandidates(ctx, job)
	if err != nil {
		return err
	}
	// Advance only after the required recovery invariant holds.
	if len(points) <= keep {
		return nil
	}
	survivor, err := d.Objects.Get(ctx, points[0].Key)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	checked, err := recovery.Open(d.Key, survivor)
	// Propagate failure before performing the next recovery side effect.
	if err != nil || checked.ID != points[0].Key {
		return errors.New("retention survivor unavailable")
	}
	// Inspect each retained item before advancing recovery state.
	for _, point := range points[keep:] {
		// Propagate failure before performing the next recovery side effect.
		if err = d.Objects.Delete(ctx, point.Key); err != nil {
			return err
		}
		c.mu.Lock()
		delete(c.state.RecoveryPoints, point.Key)
		err = c.store.Save(c.state)
		c.mu.Unlock()
		// Propagate failure before performing the next recovery side effect.
		if err != nil {
			return err
		}
	}
	return nil
}

// upgradeOperations carries the two independently provisioned resource contexts through a fenced transition.
type upgradeOperations struct{ Source, Target model.ResourceContext }

// runUpgrade verifies actual versions before fencing and retains the original source after publishing recovery evidence.
func (c *Controller) runUpgrade(ctx context.Context, job *RecoveryJob) error {
	d, err := c.recoveryDestination(job.Destination, job.DestinationFingerprint)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	operations, err := c.preflightUpgrade(ctx, job)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.fenceUpgrade(ctx, job, operations); err != nil {
		return err
	}
	bundle, err := c.restoreUpgrade(ctx, job, operations)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.publishRecovery(ctx, job, d, bundle); err != nil {
		return err
	}
	c.mu.Lock()
	c.state.Resources[job.Source].Status = "stopped"
	c.mu.Unlock()
	return c.updateRecoveryPhase(job.ID, "awaiting-independent-cutover")
}

// preflightUpgrade observes both actual product versions before any source network or data changes.
func (c *Controller) preflightUpgrade(ctx context.Context, job *RecoveryJob) (upgradeOperations, error) {
	var operations upgradeOperations
	sourceCredentials, err := c.recoveryCredentials(job.Source, job.SourceSpec)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return operations, err
	}
	operations.Source = recoveryOperation(job, job.SourceSpec, sourceCredentials)
	// Propagate failure before performing the next recovery side effect.
	if err = c.updateRecoveryPhase(job.ID, "preflight-target"); err != nil {
		return operations, err
	}
	_, targetCredentials, err := c.provisionResource(ctx, job.Target, job.ID, nil)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return operations, err
	}
	operations.Target = recoveryOperation(job, job.Target, targetCredentials)
	manager := c.opts.ResourceManager.(model.DatabaseRecoveryManager)
	source, err := manager.Evidence(ctx, job.Source, operations.Source)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return operations, err
	}
	target, err := manager.Evidence(ctx, job.Target.Name, operations.Target)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return operations, err
	}
	// Advance only after the required recovery invariant holds.
	if !model.SupportedTransition(job.SourceSpec.Type, source.Version, target.Version) {
		return operations, errors.New("unsupported observed database transition")
	}
	return operations, nil
}

// fenceUpgrade isolates both writers before collecting final source evidence and the native snapshot.
func (c *Controller) fenceUpgrade(ctx context.Context, job *RecoveryJob, operations upgradeOperations) error {
	// Propagate failure before performing the next recovery side effect.
	if err := c.updateRecoveryPhase(job.ID, "source-fence"); err != nil {
		return err
	}
	manager := c.opts.ResourceManager.(model.DatabaseRecoveryManager)
	// Propagate failure before performing the next recovery side effect.
	if err := manager.Fence(ctx, job.Source, true, operations.Source); err != nil {
		return err
	}
	return manager.Fence(ctx, job.Target.Name, true, operations.Target)
}

// restoreUpgrade stops the original writer before restoring and comparing an independent target.
func (c *Controller) restoreUpgrade(ctx context.Context, job *RecoveryJob, operations upgradeOperations) (recovery.Bundle, error) {
	bundle := recovery.Bundle{ID: job.ID, Source: job.Source, CreatedAt: job.CreatedAt, Spec: job.SourceSpec}
	manager := c.opts.ResourceManager.(model.DatabaseRecoveryManager)
	evidence, err := manager.Evidence(ctx, job.Source, operations.Source)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return bundle, err
	}
	bundle.Evidence = evidence
	// Propagate failure before performing the next recovery side effect.
	if err = c.updateRecoveryPhase(job.ID, "fenced-backup"); err != nil {
		return bundle, err
	}
	bundle.Data, err = c.opts.ResourceManager.(backupManager).Backup(ctx, job.Source, operations.Source)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return bundle, err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.opts.ResourceManager.Action(ctx, job.Source, "stop", operations.Source); err != nil {
		return bundle, err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.updateRecoveryPhase(job.ID, "upgrade-restore"); err != nil {
		return bundle, err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.opts.ResourceManager.(backupManager).Restore(ctx, job.Target.Name, bundle.Data, operations.Target); err != nil {
		return bundle, err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.updateRecoveryPhase(job.ID, "upgrade-verification"); err != nil {
		return bundle, err
	}
	target, err := manager.Evidence(ctx, job.Target.Name, operations.Target)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return bundle, err
	}
	return bundle, recovery.Match(bundle.Evidence, target, time.Now().UnixMilli())
}

// importOffsite authenticates the complete portable bundle before exposing a new local restore identity.
func (c *Controller) importOffsite(r *http.Request, input recoveryInput) (any, error) {
	d, err := c.recoveryDestination(input.Destination, input.DestinationFingerprint)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, err
	}
	c.wg.Add(1)
	defer c.wg.Done()
	c.mu.Unlock()
	data, err := d.Objects.Get(r.Context(), input.ObjectKey)
	var bundle recovery.Bundle
	// Propagate failure before performing the next recovery side effect.
	if err == nil {
		bundle, err = recovery.Open(d.Key, data)
	}
	c.mu.Lock()
	// Propagate failure before performing the next recovery side effect.
	if err != nil || bundle.ID != input.ObjectKey || bundle.Spec.Type != bundle.Evidence.Engine {
		return nil, model.Err("RECOVERY_IMPORT", "Object authentication, identity or engine validation failed")
	}
	id := random()
	encrypted, err := c.store.Encrypt(string(bundle.Data))
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = c.store.WriteBlob(id+".backup", []byte(encrypted)); err != nil {
		return nil, err
	}
	record := BackupRecord{ID: id, Name: bundle.Source, Type: bundle.Spec.Type, CreatedAt: bundle.CreatedAt, Size: len(bundle.Data), Spec: &bundle.Spec, Evidence: &bundle.Evidence}
	c.state.Backups[id] = record
	c.state.Generation++
	return record, nil
}

// recoveryPolicyEnabled rechecks retained scheduling authority immediately before native work starts.
func (c *Controller) recoveryPolicyEnabled(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	policy := c.state.BackupPolicies[name]
	return policy != nil && policy.Enabled && c.backupPolicyLive(policy)
}

// retentionCandidates snapshots only verified points covered by the exact approved policy and destination.
func (c *Controller) retentionCandidates(ctx context.Context, job *RecoveryJob) ([]RemoteRecoveryPoint, int, error) {
	c.mu.Lock()
	policy := c.state.BackupPolicies[job.Policy]
	keep := 1
	// Advance only after the required recovery invariant holds.
	if policy != nil {
		keep = policy.Retain
	}
	c.mu.Unlock()
	points, err := collectionValues[RemoteRecoveryPoint](ctx, c.store, "recoveryPoints", store.CollectionQuery{Policy: job.Policy, Destination: job.DestinationFingerprint, Verified: true, NewestFirst: true})
	return points, keep, err
}
