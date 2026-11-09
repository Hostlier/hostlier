package controller

import (
	"hostlier/internal/model"
	"hostlier/internal/store"
	"net/http"
	"regexp"
	"time"
)

// recoveryInput contains only nonsecret, independently reviewed recovery intent.
type recoveryInput struct {
	Name                   string `json:"name"`
	Resource               string `json:"resource"`
	Target                 string `json:"target"`
	Destination            string `json:"destination"`
	DestinationFingerprint string `json:"destinationFingerprint"`
	EverySeconds           int64  `json:"everySeconds"`
	Retain                 int    `json:"retain"`
	To                     string `json:"to"`
	ObjectKey              string `json:"objectKey"`
}

// apiRecovery exposes public metadata separately from protected policy, import and upgrade mutations.
func (c *Controller) apiRecovery(r *http.Request, p []string, actor string) (any, error) {
	// Select the explicit recovery operation or native engine behavior.
	switch p[1] {
	// Handle only this supported recovery operation or retained state.
	case "backup-ticks":
		return c.recoveryTick(r)
	// Handle only this supported recovery operation or retained state.
	case "recovery-review":
		return c.recoveryReviewAPI(r)
	// Handle only this supported recovery operation or retained state.
	case "backup-destinations":
		return c.recoveryDestinations(r, p)
	// Handle only this supported recovery operation or retained state.
	case "backup-policies":
		return c.backupPolicyAPI(r, p)
	// Handle only this supported recovery operation or retained state.
	case "recovery-jobs":
		return c.recoveryJobAPI(r, p)
	// Handle only this supported recovery operation or retained state.
	case "upgrades", "backup-imports":
		return c.recoveryCreateAPI(r, p)

	}
	return endpointNotFound()
}

// recoveryDestinations exposes only destination identity, never credentials or encryption keys.
func (c *Controller) recoveryDestinations(r *http.Request, p []string) (any, error) {
	// Advance only after the required recovery invariant holds.
	if r.Method != "GET" {
		return endpointNotFound()
	}
	// Advance only after the required recovery invariant holds.
	if len(p) == 4 && p[3] == "objects" {
		d := c.opts.RecoveryDestinations[p[2]]
		// Advance only after the required recovery invariant holds.
		if d == nil {
			return endpointNotFound()
		}
		c.wg.Add(1)
		defer c.wg.Done()
		c.mu.Unlock()
		defer c.mu.Lock()
		keys, err := d.Objects.List(r.Context())
		// Propagate failure before performing the next recovery side effect.
		if err != nil {
			return nil, model.Err("RECOVERY_STORAGE", "S3 inventory unavailable")
		}
		return map[string]any{"objects": keys}, nil
	}
	out := []map[string]string{}
	// Inspect each retained item before advancing recovery state.
	for id, d := range c.opts.RecoveryDestinations {
		out = append(out, map[string]string{"id": id, "endpoint": d.Endpoint, "region": d.Region, "bucket": d.Bucket, "prefix": d.Prefix, "fingerprint": d.Fingerprint()})
	}
	return map[string]any{"destinations": out}, nil
}

// backupPolicyAPI binds recurring authority to a source, storage identity and finite retention policy.
func (c *Controller) backupPolicyAPI(r *http.Request, p []string) (any, error) {
	// Advance only after the required recovery invariant holds.
	if r.Method == "GET" && len(p) == 2 {
		policies, err := collectionMap[*BackupPolicy](r.Context(), c.store, "backupPolicies", store.CollectionQuery{})
		if err != nil {
			return nil, err
		}
		points, err := collectionMap[RemoteRecoveryPoint](r.Context(), c.store, "recoveryPoints", store.CollectionQuery{})
		return map[string]any{"policies": policies, "points": points}, err
	}
	// Advance only after the required recovery invariant holds.
	if r.Method == "POST" && len(p) == 2 {
		var input recoveryInput
		// Propagate failure before performing the next recovery side effect.
		if err := decode(r, &input); err != nil {
			return nil, err
		}
		return c.createBackupPolicy(r, input)
	}
	// Advance only after the required recovery invariant holds.
	if len(p) != 4 || r.Method != "POST" {
		return endpointNotFound()
	}
	return c.backupPolicyAction(r, p)
}

// backupPolicyAction executes only a separately approved run or disable operation.
func (c *Controller) backupPolicyAction(r *http.Request, p []string) (any, error) {
	policy := c.state.BackupPolicies[p[2]]
	// Advance only after the required recovery invariant holds.
	if policy == nil {
		return endpointNotFound()
	}
	// Propagate failure before performing the next recovery side effect.
	if err := emptyBody(r); err != nil {
		return nil, err
	}
	// Advance only after the required recovery invariant holds.
	if p[3] == "disable" {
		policy.Enabled = false
		c.state.Generation++
		return policy, nil
	}
	// Advance only after the required recovery invariant holds.
	if p[3] == "run" {
		return c.queueBackup(policy)
	}
	return endpointNotFound()
}

// createBackupPolicy requires explicit restore-test capacity as part of every scheduled run.
func (c *Controller) createBackupPolicy(r *http.Request, input recoveryInput) (any, error) {
	// Reject this request: Policy requires a name, interval of 1 hour–31 days and 1–100 retained copies.
	if !validPolicyInput(input) {
		return nil, model.Err("RECOVERY_POLICY", "Policy requires a name, interval of 1 hour–31 days and 1–100 retained copies")
	}
	// Reject this request: Policy names are immutable; disable the old policy and approve a new name.
	if c.state.BackupPolicies[input.Name] != nil && input.Name != "" {
		return nil, model.Err("RECOVERY_POLICY", "Policy names are immutable; disable the old policy and approve a new name")
	}
	source, err := c.validateRecoverySource(input.Resource)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, err
	}
	// Propagate failure before performing the next recovery side effect.
	if _, err = c.recoveryDestination(input.Destination, input.DestinationFingerprint); err != nil {
		return nil, err
	}
	policy := &BackupPolicy{Name: input.Name, Resource: input.Resource, Destination: input.Destination, DestinationFingerprint: input.DestinationFingerprint, EverySeconds: input.EverySeconds, Retain: input.Retain, Spec: source.Spec, Enabled: true, NextRun: c.opts.Now().Add(time.Duration(input.EverySeconds) * time.Second).UnixMilli()}
	// Advance only after the required recovery invariant holds.
	if approval, ok := r.Context().Value(humanExecutionKey{}).(*HumanApproval); ok {
		policy.KeyFingerprint = approval.CredentialID
	}
	_, policy.FleetScheduled = r.Context().Value(fleetContext{}).(hostAuthority)
	c.state.BackupPolicies[input.Name] = policy
	c.state.Generation++
	return policy, nil
}

// validateRecoverySource refuses conflicting work and offline Neo4j maintenance with active dependents.
func (c *Controller) validateRecoverySource(name string) (*ResourceRecord, error) {
	source := c.state.Resources[name]
	// Reject this request: A running managed database is required.
	if source == nil || source.Status != "running" {
		return nil, model.Err("RECOVERY_SOURCE", "A running managed database is required")
	}
	// Reject this request: Database has retained work or a recovery fence.
	if c.resourceBusy(name) {
		return nil, model.Err("RESOURCE_BUSY", "Database has retained work or a recovery fence")
	}
	// Reject this request: Resource package requires native verification support.
	if _, ok := c.opts.ResourceManager.(model.DatabaseRecoveryManager); !ok {
		return nil, model.Err("RECOVERY_UNSUPPORTED", "Resource package requires native verification support")
	}
	// Reject this request: Resource package requires native backup support.
	if _, ok := c.opts.ResourceManager.(backupManager); !ok {
		return nil, model.Err("RECOVERY_UNSUPPORTED", "Resource package requires native backup support")
	}
	// Reject this request: Detach Neo4j dependencies before offline backup maintenance.
	if source.Type == "neo4j" && len(c.resourceDependents(name)) > 0 {
		return nil, model.Err("RESOURCE_IN_USE", "Detach Neo4j dependencies before offline backup maintenance")
	}
	return source, nil
}

// queueBackup reserves a unique verification target and durably accepts one policy-bound job.
func (c *Controller) queueBackup(policy *BackupPolicy) (any, error) {
	source, err := c.validateRecoverySource(policy.Resource)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, err
	}
	// Reject this request: Policy authority expired or source/destination changed.
	if !c.backupPolicyLive(policy) {
		return nil, model.Err("RECOVERY_POLICY", "Policy authority expired or source/destination changed")
	}
	id := random()
	target := source.Spec
	target.Name = "restorecheck-" + id[:20]
	job := &RecoveryJob{ID: id, Kind: "backup", Status: "queued", Phase: "accepted", Source: source.Name, SourceSpec: source.Spec, Target: target, Destination: policy.Destination, DestinationFingerprint: policy.DestinationFingerprint, Policy: policy.Name, CreatedAt: c.opts.Now().UnixMilli()}
	return c.acceptRecoveryJob(job)
}

// queueUpgrade reserves both database names before creating any new engine or fencing the original.
func (c *Controller) queueUpgrade(input recoveryInput) (any, error) {
	source, err := c.validateRecoverySource(input.Resource)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, err
	}
	// Reject this request: Upgrade requires Docker and detached application dependencies.
	if c.opts.ResourceManager.Name() != "docker" || len(c.resourceDependents(source.Name)) > 0 {
		return nil, model.Err("UPGRADE_PREFLIGHT", "Upgrade requires Docker and detached application dependencies")
	}
	image := model.UpgradeImage(source.Type, input.To)
	// Reject this request: Choose a supported transition and an unused target name.
	if image == "" || input.Target == source.Name || c.state.Resources[input.Target] != nil || c.resourceBusy(input.Target) {
		return nil, model.Err("UPGRADE_PREFLIGHT", "Choose a supported transition and an unused target name")
	}
	// Propagate failure before performing the next recovery side effect.
	if _, err = c.recoveryDestination(input.Destination, input.DestinationFingerprint); err != nil {
		return nil, err
	}
	spec := source.Spec
	spec.Name = input.Target
	spec.Image = image
	planned, err := c.opts.ResourceManager.Plan(c.ctx, spec)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, model.Err("UPGRADE_PREFLIGHT", "Upgrade target specification cannot be planned")
	}
	job := &RecoveryJob{ID: random(), Kind: "upgrade", Status: "queued", Phase: "accepted", Source: source.Name, SourceSpec: source.Spec, Target: planned.Spec, Destination: input.Destination, DestinationFingerprint: input.DestinationFingerprint, CreatedAt: c.opts.Now().UnixMilli()}
	return c.acceptRecoveryJob(job)
}

// acceptRecoveryJob saves reservations before a worker can touch a runtime or object store.
func (c *Controller) acceptRecoveryJob(job *RecoveryJob) (any, error) {
	// Reject this request: Recovery history limit reached.
	if len(c.state.RecoveryJobs) >= 10000 {
		return nil, model.Err("RECOVERY_LIMIT", "Recovery history limit reached")
	}
	c.state.RecoveryJobs[job.ID] = job
	c.state.Generation++
	// Propagate failure before performing the next recovery side effect.
	if err := c.store.Save(c.state); err != nil {
		delete(c.state.RecoveryJobs, job.ID)
		return nil, err
	}
	c.startRecoveryJob(job)
	return job, nil
}

// recoveryCreateAPI validates complete mutation input before selecting upgrade or portable import.
func (c *Controller) recoveryCreateAPI(r *http.Request, p []string) (any, error) {
	// Advance only after the required recovery invariant holds.
	if r.Method != "POST" || len(p) != 2 {
		return endpointNotFound()
	}
	var input recoveryInput
	// Propagate failure before performing the next recovery side effect.
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	// Advance only after the required recovery invariant holds.
	if p[1] == "upgrades" {
		return c.queueUpgrade(input)
	}
	return c.importOffsite(r, input)
}

// validPolicyInput limits recurring maintenance frequency and retained-copy scope.
func validPolicyInput(input recoveryInput) bool {
	return regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`).MatchString(input.Name) && input.EverySeconds >= 3600 && input.EverySeconds <= 31*86400 && input.Retain >= 1 && input.Retain <= 100
}
