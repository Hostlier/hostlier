package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"hostlier/internal/model"
	"hostlier/internal/recovery"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RemoteBackupSchedule keeps recurring hardware-key authority on the central controller that can revoke it.
type RemoteBackupSchedule struct {
	Host              string `json:"host"`
	HostFingerprint   string `json:"hostFingerprint"`
	Policy            string `json:"policy"`
	PolicyFingerprint string `json:"policyFingerprint"`
	KeyFingerprint    string `json:"keyFingerprint,omitempty"`
}

// backupPolicyFingerprint excludes mutable clock and enablement state from otherwise exact recurring authority.
func backupPolicyFingerprint(policy *BackupPolicy) string {
	copy := *policy
	copy.NextRun = 0
	copy.Enabled = false
	data, _ := json.Marshal(copy)
	return recovery.Digest(data)
}

// trackRemoteBackupPolicy retains only a successfully created policy's exact host and independently approved scope.
func (c *Controller) trackRemoteBackupPolicy(r *http.Request, host HostRecord, path string, result any) error {
	// Advance only after the required recovery invariant holds.
	if r.Method != "POST" || path != "/v1/backup-policies" {
		return nil
	}
	data, err := json.Marshal(result)
	var policy BackupPolicy
	// Propagate failure before performing the next recovery side effect.
	if err != nil || json.Unmarshal(data, &policy) != nil || !policy.FleetScheduled {
		return model.Err("RECOVERY_POLICY", "Host did not retain a centrally scheduled backup policy")
	}
	schedule := RemoteBackupSchedule{Host: host.Name, HostFingerprint: host.Fingerprint, Policy: policy.Name, PolicyFingerprint: backupPolicyFingerprint(&policy)}
	// Advance only after the required recovery invariant holds.
	if approval, ok := r.Context().Value(humanExecutionKey{}).(*HumanApproval); ok {
		schedule.KeyFingerprint = approval.CredentialID
	}
	c.state.RemoteBackupSchedules[host.Name+"/"+policy.Name] = schedule
	c.state.Generation++
	return c.store.Save(c.state)
}

// scheduleRemoteBackups sends no autonomous authority to hosts; each due run requires a live central decision.
func (c *Controller) scheduleRemoteBackups() {
	schedules := []RemoteBackupSchedule{}
	// Inspect each retained item before advancing recovery state.
	for _, schedule := range c.state.RemoteBackupSchedules {
		schedules = append(schedules, schedule)
	}
	// Inspect each retained item before advancing recovery state.
	for _, schedule := range schedules {
		c.tickRemoteBackup(schedule)
	}
}

// tickRemoteBackup binds one scheduling decision to current host state and the original immutable policy.
func (c *Controller) tickRemoteBackup(schedule RemoteBackupSchedule) {
	host, ok := c.state.Hosts[schedule.Host]
	// Advance only after the required recovery invariant holds.
	if !ok || host.Fingerprint != schedule.HostFingerprint || !c.backupKeyLive(schedule.KeyFingerprint) {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	defer cancel()
	binding, err := c.recoveryHostBinding(ctx, host)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return
	}
	// Recheck authority after the host-state read released the central lock.
	current, ok := c.state.Hosts[schedule.Host]
	// Advance only after the required recovery invariant holds.
	if !ok || current.Fingerprint != host.Fingerprint || !c.backupKeyLive(schedule.KeyFingerprint) {
		return
	}
	body, _ := json.Marshal(map[string]string{"name": schedule.Policy, "fingerprint": schedule.PolicyFingerprint})
	_, _ = c.fleetRequest(ctx, host, "POST", "/v1/backup-ticks", body, "scheduled-backup", binding)
}

// recoveryTick accepts exact centrally scheduled scope only on the pinned mutual-TLS agent listener.
func (c *Controller) recoveryTick(r *http.Request) (any, error) {
	// Reject this request: Backup scheduling requires the enrolled central controller.
	if _, ok := r.Context().Value(fleetContext{}).(hostAuthority); !ok || r.Method != "POST" {
		return nil, model.Err("HUMAN_APPROVAL_REQUIRED", "Backup scheduling requires the enrolled central controller")
	}
	var input struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
	}
	// Propagate failure before performing the next recovery side effect.
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	policy := c.state.BackupPolicies[input.Name]
	// Reject this request: Centrally approved backup scope changed.
	if policy == nil || !policy.FleetScheduled || backupPolicyFingerprint(policy) != input.Fingerprint {
		return nil, model.Err("RECOVERY_POLICY", "Centrally approved backup scope changed")
	}
	// Advance only after the required recovery invariant holds.
	if !c.backupPolicyDue(policy) {
		return map[string]bool{"queued": false}, nil
	}
	previous := policy.NextRun
	policy.NextRun = c.opts.Now().Add(time.Duration(policy.EverySeconds) * time.Second).UnixMilli()
	job, err := c.queueBackup(policy)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		policy.NextRun = previous
	}
	return job, err
}

// recoveryReviewAPI returns host-authoritative nonsecret meaning for central human review.
func (c *Controller) recoveryReviewAPI(r *http.Request) (any, error) {
	// Advance only after the required recovery invariant holds.
	if r.Method != "GET" || len(r.URL.RawQuery) > 150000 {
		return endpointNotFound()
	}
	data, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("intent"))
	var request humanRequest
	// Propagate failure before performing the next recovery side effect.
	if err != nil || json.Unmarshal(data, &request) != nil || !recoveryReviewPath(request.Path) {
		return nil, model.Err("INVALID_REQUEST", "Recovery review requires a canonical recovery request")
	}
	review := map[string]any{}
	c.addRecoveryReview(request, review)
	return map[string]any{"generation": c.state.Generation, "review": review}, nil
}

// recoveryReviewPath restricts delegated inspection to recovery metadata and never expands arbitrary API bodies.
func recoveryReviewPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// Advance only after the required recovery invariant holds.
	if len(parts) < 2 || parts[0] != "v1" || strings.Contains(path, "..") {
		return false
	}
	return parts[1] == "backup-policies" || parts[1] == "recovery-jobs" || parts[1] == "upgrades" || parts[1] == "backup-imports"
}

// reviewHostRecovery rejects metadata from a different host generation than the signed request.
func (c *Controller) reviewHostRecovery(request humanRequest, host HostRecord, inner, actor string, review map[string]any) error {
	// Advance only after the required recovery invariant holds.
	if !recoveryReviewPath(inner) {
		return nil
	}
	local := request
	local.Path = inner
	data, _ := json.Marshal(local)
	result, err := c.fleetRequest(c.ctx, host, "GET", "/v1/recovery-review?intent="+url.QueryEscape(base64.RawURLEncoding.EncodeToString(data)), nil, actor, nil)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(result)
	var value struct {
		Generation int64
		Review     map[string]any
	}
	// Reject this request: Host recovery state changed; request a new review.
	if json.Unmarshal(raw, &value) != nil || value.Generation != request.Target.Generation {
		return model.Err("STALE_PLAN", "Host recovery state changed; request a new review")
	}
	review["recovery"] = value.Review
	return nil
}

// backupKeyLive keeps revocation checks in the controller that enrolled the human key.
func (c *Controller) backupKeyLive(fingerprint string) bool {
	// Advance only after the required recovery invariant holds.
	if fingerprint == "" {
		return true
	}
	// Advance only after the required recovery invariant holds.
	if c.state.Human == nil {
		return false
	}
	key, ok := c.state.Human.Keys[fingerprint]
	return ok && !key.Revoked
}

// recoveryHostBinding bounds unavailable-host delay and refuses mutations without a current host generation.
func (c *Controller) recoveryHostBinding(ctx context.Context, host HostRecord) (*fleetBinding, error) {
	result, err := c.fleetRequest(ctx, host, "GET", "/v1/host-state", nil, "scheduled-backup", nil)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(result)
	var state struct {
		Generation *int64 `json:"generation"`
	}
	// Reject this request: Host generation unavailable.
	if json.Unmarshal(raw, &state) != nil || state.Generation == nil {
		return nil, model.Err("HOST_PROTOCOL", "Host generation unavailable")
	}
	return &fleetBinding{Fingerprint: host.Fingerprint, Generation: *state.Generation}, nil
}
