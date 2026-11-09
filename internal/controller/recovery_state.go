package controller

import (
	"context"
	"hostlier/internal/model"
	"hostlier/internal/recovery"
	"time"
)

// BackupPolicy is finite, independently approved recurring authority for one pinned source and destination.
type BackupPolicy struct {
	FleetScheduled         bool               `json:"fleetScheduled,omitempty"`
	Name                   string             `json:"name"`
	Resource               string             `json:"resource"`
	Destination            string             `json:"destination"`
	DestinationFingerprint string             `json:"destinationFingerprint"`
	EverySeconds           int64              `json:"everySeconds"`
	Retain                 int                `json:"retain"`
	Spec                   model.ResourceSpec `json:"spec"`
	KeyFingerprint         string             `json:"keyFingerprint,omitempty"`
	Enabled                bool               `json:"enabled"`
	NextRun                int64              `json:"nextRun"`
}

// RecoveryJob is a durable phase record; interrupted mutations are never automatically replayed.
type RecoveryJob struct {
	CutoverStarted         bool                   `json:"cutoverStarted,omitempty"`
	ID                     string                 `json:"id"`
	Kind                   string                 `json:"kind"`
	Status                 string                 `json:"status"`
	Phase                  string                 `json:"phase"`
	Source                 string                 `json:"source"`
	SourceSpec             model.ResourceSpec     `json:"sourceSpec"`
	Target                 model.ResourceSpec     `json:"target"`
	Destination            string                 `json:"destination"`
	DestinationFingerprint string                 `json:"destinationFingerprint"`
	Policy                 string                 `json:"policy,omitempty"`
	ObjectKey              string                 `json:"objectKey,omitempty"`
	Evidence               model.DatabaseEvidence `json:"evidence,omitempty"`
	CreatedAt              int64                  `json:"createdAt"`
	Error                  string                 `json:"error,omitempty"`
}

// RemoteRecoveryPoint records only successfully restored and read-back-verified offsite objects.
type RemoteRecoveryPoint struct {
	Key                    string `json:"key"`
	Policy                 string `json:"policy"`
	Destination            string `json:"destination"`
	DestinationFingerprint string `json:"destinationFingerprint"`
	Source                 string `json:"source"`
	CreatedAt              int64  `json:"createdAt"`
	Verified               bool   `json:"verified"`
}

// initializeRecovery marks uncertain jobs for explicit inspection and preserves their resource reservations.
func (c *Controller) initializeRecovery() {
	// Advance only after the required recovery invariant holds.
	if c.state.RemoteBackupSchedules == nil {
		c.state.RemoteBackupSchedules = map[string]RemoteBackupSchedule{}
	}
	// Advance only after the required recovery invariant holds.
	if c.state.BackupPolicies == nil {
		c.state.BackupPolicies = map[string]*BackupPolicy{}
	}
	// Advance only after the required recovery invariant holds.
	if c.state.RecoveryJobs == nil {
		c.state.RecoveryJobs = map[string]*RecoveryJob{}
	}
	// Advance only after the required recovery invariant holds.
	if c.state.RecoveryPoints == nil {
		c.state.RecoveryPoints = map[string]RemoteRecoveryPoint{}
	}
	// Inspect each retained item before advancing recovery state.
	for _, job := range c.state.RecoveryJobs {
		// Advance only after the required recovery invariant holds.
		if job.Status == "running" || job.Status == "queued" {
			job.Status = "uncertain"
			job.Error = "Controller restarted; inspect the retained phase and explicitly abort or reconcile"
		}
	}
}

// recoveryReserved blocks ordinary lifecycle and dependency attachment while a workflow owns its source or target.
func (c *Controller) recoveryReserved(name string) bool {
	// Inspect each retained item before advancing recovery state.
	for _, job := range c.state.RecoveryJobs {
		// Advance only after the required recovery invariant holds.
		if job.Source != name && job.Target.Name != name {
			continue
		}
		// Advance only after the required recovery invariant holds.
		if recoveryUnresolved(job.Status) {
			return true
		}
		// Advance only after the required recovery invariant holds.
		if job.Kind == "upgrade" && job.Status == "succeeded" && job.Source == name {
			return true
		}
	}
	return false
}

// recoveryDestination rejects changed operator configuration before any scheduled or retained authority is used.
func (c *Controller) recoveryDestination(id, pin string) (*recovery.Destination, error) {
	destination := c.opts.RecoveryDestinations[id]
	// Reject this request: Destination or encryption key changed; approve a new policy.
	if destination == nil || destination.Fingerprint() != pin {
		return nil, model.Err("RECOVERY_DESTINATION", "Destination or encryption key changed; approve a new policy")
	}
	return destination, nil
}

// startRecoveryScheduler runs one bounded scheduling pass per minute without reissuing missed intervals in a burst.
func (c *Controller) startRecoveryScheduler() {
	c.wg.Add(1)
	// Keep asynchronous work bound to the controller lifetime.
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		// Inspect each retained item before advancing recovery state.
		for {
			// Stop scheduling when the controller shuts down.
			select {
			// Handle only this supported recovery operation or retained state.
			case <-c.ctx.Done():
				return
			// Handle only this supported recovery operation or retained state.
			case <-ticker.C:
				c.scheduleBackups()
			}
		}
	}()
}

// scheduleBackups persists the next eligible time before accepting a background job.
func (c *Controller) scheduleBackups() {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Advance only after the required recovery invariant holds.
	if c.closed {
		return
	}
	// Inspect each retained item before advancing recovery state.
	for _, policy := range c.state.BackupPolicies {
		// Advance only after the required recovery invariant holds.
		if policy.FleetScheduled || !policy.Enabled || policy.NextRun > c.opts.Now().UnixMilli() || !c.backupPolicyLive(policy) {
			continue
		}
		// Advance only after the required recovery invariant holds.
		if c.resourceBusy(policy.Resource) {
			continue
		}
		previous := policy.NextRun
		policy.NextRun = c.opts.Now().Add(time.Duration(policy.EverySeconds) * time.Second).UnixMilli()
		// A rejected job must remain due rather than silently consuming its scheduled interval.
		if _, err := c.queueBackup(policy); err != nil {
			policy.NextRun = previous
		}
	}
	c.scheduleRemoteBackups()
}

// backupPolicyLive rechecks key revocation, resource identity and configured storage authority on each scheduled run.
func (c *Controller) backupPolicyLive(p *BackupPolicy) bool {
	// Propagate failure before performing the next recovery side effect.
	if _, err := c.recoveryDestination(p.Destination, p.DestinationFingerprint); err != nil {
		return false
	}
	source := c.state.Resources[p.Resource]
	// Advance only after the required recovery invariant holds.
	if source == nil || source.Status != "running" || !sameResourceSettings(source.Spec, p.Spec) || source.Spec.PackageDigest != p.Spec.PackageDigest {
		return false
	}
	// Advance only after the required recovery invariant holds.
	if p.KeyFingerprint != "" {
		// Advance only after the required recovery invariant holds.
		if c.state.Human == nil {
			return false
		}
		key, ok := c.state.Human.Keys[p.KeyFingerprint]
		// Advance only after the required recovery invariant holds.
		if !ok || key.Revoked {
			return false
		}
	}
	return true
}

// updateRecoveryPhase commits progress before the following external side effect.
func (c *Controller) updateRecoveryPhase(id, phase string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	job := c.state.RecoveryJobs[id]
	c.signalJobChange()
	job.Phase = phase
	return c.store.Save(c.state)
}

// startRecoveryJob owns the controller worker lifetime and serializes runtime mutations with normal operations.
func (c *Controller) startRecoveryJob(job *RecoveryJob) {
	c.wg.Add(1)
	// Keep asynchronous work bound to the controller lifetime.
	go func() {
		defer c.wg.Done()
		c.execMu.Lock()
		defer c.execMu.Unlock()
		ctx, cancel := context.WithTimeout(c.ctx, 30*time.Minute)
		defer cancel()
		c.mu.Lock()
		c.signalJobChange()
		job.Status = "running"
		err := c.store.Save(c.state)
		copy := *job
		c.mu.Unlock()
		// Propagate failure before performing the next recovery side effect.
		if err == nil {
			// Advance only after the required recovery invariant holds.
			if copy.Kind == "upgrade" {
				err = c.runUpgrade(ctx, &copy)
			} else {
				err = c.runOffsiteBackup(ctx, &copy)
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		// Propagate failure before performing the next recovery side effect.
		if err != nil {
			job.Status = "failed"
			job.Error = "Recovery failed in " + job.Phase + "; inspect and explicitly abort before retrying"
		} else {
			job.Status = "succeeded"
			// Advance only after the required recovery invariant holds.
			if job.Kind == "upgrade" {
				job.Status = "awaiting-cutover"
			}
		}
		c.state.Generation++
		c.persistRecoveryOutcome(job)
	}()
}

// recoveryUnresolved keeps every incomplete or failed phase reserved until an explicit finalization.
func recoveryUnresolved(status string) bool {
	return status == "queued" || status == "running" || status == "uncertain" || status == "awaiting-cutover" || status == "failed"
}

// backupPolicyDue combines the clock, key and resource checks used by local and central scheduling.
func (c *Controller) backupPolicyDue(policy *BackupPolicy) bool {
	return policy.Enabled && policy.NextRun <= c.opts.Now().UnixMilli() && c.backupPolicyLive(policy) && !c.resourceBusy(policy.Resource)
}
