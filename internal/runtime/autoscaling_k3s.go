package runtime

import (
	"context"
	"errors"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"time"
)

// autoscalingRecord journals native HPA lifecycle intent before external changes.
type autoscalingRecord struct {
	Phase                   string             `json:"phase"`
	Target                  string             `json:"target"`
	UID                     string             `json:"uid"`
	Template                string             `json:"template"`
	Policy                  string             `json:"policy"`
	Reason                  string             `json:"reason,omitempty"`
	MetricsUnavailableSince int64              `json:"metricsUnavailableSince,omitempty"`
	ApprovalOperation       string             `json:"approvalOperation"`
	Events                  []autoscalingEvent `json:"events,omitempty"`
}

// pauseAutoscaling durably revokes delegation before deleting the owned HPA.
func (k *K3s) pauseAutoscaling(ctx context.Context, s state, name, reason string) error {
	app, ok := s.Apps[name]
	// Applications without an installed delegation require no autoscaler mutation.
	if !ok || app.Autoscaling == nil {
		return nil
	}
	app.Autoscaling.transition("paused", reason)
	s.Apps[name] = app
	// A restart retries deletion from this persisted fence before any capacity mutation.
	if err := k.base.save(s); err != nil {
		return err
	}
	return k.removeAutoscaler(ctx, app)
}

// removeAutoscaler checks exact application and target identity before revocation.
func (k *K3s) removeAutoscaler(ctx context.Context, app appRecord) error {
	obj, err := k.get(ctx, "horizontalpodautoscaler", "autoscaler", k.namespace(app.Name))
	// Lookup failure must not authorize a conflicting manual operation.
	if err != nil {
		return err
	}
	// Repeated deletion after a crash is safe when absence is authoritative.
	if obj == nil {
		return nil
	}
	// Foreign HPA ownership never permits deletion or adoption.
	if !k.ownsApplication(obj, app.Name) {
		return model.Err("OWNERSHIP", "Autoscaler application ownership mismatch")
	}
	// Revoke our owned HPA even when its target drifted; leave foreign objects untouched.
	return k.remove(ctx, "horizontalpodautoscaler", "autoscaler", k.namespace(app.Name))
}

// prepareAutoscaling verifies metrics availability before pausing the serving revision.
func (d *k3sDeployment) prepareAutoscaling() error {
	k := d.runtime
	// No CPU controller is installed unless this exact deployment approved delegation.
	if d.spec.Autoscaling != nil {
		// Validate all reviewed quantities before granting replica authority.
		if _, err := config.ValidateProject(d.spec); err != nil {
			return err
		}
		// Require an available cluster metrics API; HPA itself consumes per-pod metrics.
		if err := k.autoscalingMetricsReady(d.ctx); err != nil {
			return err
		}
	}
	// Idempotent replay must preserve live HPA replica ownership for its committed target.
	if d.hasOld && d.old.OperationID == d.dc.OperationID {
		return nil
	}
	return k.pauseAutoscaling(d.ctx, d.s, d.spec.Name, "approved replacement")
}

// activateAutoscaling journals an approved target only after readiness and HTTPS cutover.
func (d *k3sDeployment) activateAutoscaling() error {
	// Zero and manually operated revisions never install an HPA.
	if d.spec.Autoscaling == nil {
		return nil
	}
	app := d.s.Apps[d.spec.Name]
	// A replay of an already delegated revision preserves the existing journal.
	if app.Autoscaling != nil {
		return d.runtime.reconcileAutoscaler(d.ctx, d.s, app)
	}
	object, err := d.runtime.get(d.ctx, "deployment", d.version, d.namespace)
	// An unreadable authoritative observation must not authorize mutation.
	if err != nil {
		return err
	}
	// Bind the newly installed HPA to the application already proven ready.
	if !d.runtime.ownsApplication(object, d.spec.Name) {
		return model.Err("OWNERSHIP", "Autoscaler target is not an owned application")
	}
	metadata, _ := object["metadata"].(map[string]any)
	uid, _ := metadata["uid"].(string)
	// UID binds delegation to the exact Deployment instance rather than a reused name.
	if uid == "" {
		return model.Err("OWNERSHIP", "Autoscaler target UID unavailable")
	}
	targetSpec, _ := object["spec"].(map[string]any)
	app.Autoscaling = &autoscalingRecord{ApprovalOperation: d.dc.OperationID, Phase: "pending", Target: d.version, UID: uid, Template: config.Fingerprint(targetSpec["template"]), Policy: config.Fingerprint(d.spec.Autoscaling)}
	// Carry bounded lifecycle evidence across revisions without recording secret values.
	if d.old.Autoscaling != nil {
		app.Autoscaling.Events = append([]autoscalingEvent(nil), d.old.Autoscaling.Events...)
	}
	app.Autoscaling.transition("pending", "approved readiness handoff")
	d.s.Apps[d.spec.Name] = app
	// Persist transfer intent before installing the external replica writer.
	if err = d.runtime.base.save(d.s); err != nil {
		return err
	}
	return d.runtime.reconcileAutoscaler(d.ctx, d.s, app)
}

// reconcileAutoscaler completes pending installation or persistent revocation under the runtime lock.
func (k *K3s) reconcileAutoscaler(ctx context.Context, s state, app appRecord) error {
	record := app.Autoscaling
	// A paused journal always wins over a retained declarative policy.
	if record.Phase == "paused" {
		return k.removeAutoscaler(ctx, app)
	}
	// Target or template drift permanently fences this delegation.
	if err := k.verifyAutoscalerTarget(ctx, app); err != nil {
		return k.autoscalingTargetFailure(ctx, s, app, err)
	}
	obj, err := k.get(ctx, "horizontalpodautoscaler", "autoscaler", k.namespace(app.Name))
	// An unreadable authoritative observation must not authorize mutation.
	if err != nil {
		return err
	}
	expected := k.autoscalerManifest(app)
	// Existing active controllers are observed, never repeatedly applied over Kubernetes state.
	if obj != nil {
		// An HPA with edited bounds must be revoked instead of repaired silently.
		if !k.ownsApplication(obj, app.Name) || config.Fingerprint(obj["spec"]) != config.Fingerprint(expected["spec"]) {
			return k.pauseAutoscaling(ctx, s, app.Name, "autoscaler policy drift")
		}
	} else {
		// An unexpectedly deleted active HPA requires renewed deployment approval.
		if record.Phase == "active" {
			return k.pauseAutoscaling(ctx, s, app.Name, "autoscaler removed externally")
		}
		// An interrupted apply retains pending intent for recovery.
		if err = k.apply(ctx, expected); err != nil {
			return err
		}
	}
	// Stable active delegation never rewrites desired state or replica ownership.
	if record.Phase == "active" {
		return k.observeAutoscalingOwnership(ctx, s, app, obj)
	}
	record.transition("active", "native HPA installed")
	app.Autoscaling = record
	s.Apps[app.Name] = app
	return k.base.save(s)
}

// verifyAutoscalerTarget prevents old revisions, replaced objects and zero from obtaining authority.
func (k *K3s) verifyAutoscalerTarget(ctx context.Context, app appRecord) error {
	a := app.Autoscaling
	// Paused capacity and old revision inputs cannot obtain replica authority.
	if !autoscalingRevisionEligible(app) {
		return model.Err("AUTOSCALING_FENCE", "Autoscaling revision is fenced")
	}
	object, err := k.get(ctx, "deployment", a.Target, k.namespace(app.Name))
	// An unreadable authoritative observation must not authorize mutation.
	if err != nil {
		return err
	}
	metadata, _ := object["metadata"].(map[string]any)
	targetSpec, _ := object["spec"].(map[string]any)
	// UID, pod template and replica-manager changes all invalidate approval.
	if !k.ownsApplication(object, app.Name) || metadata["uid"] != a.UID || foreignReplicaWriter(object) || config.Fingerprint(targetSpec["template"]) != a.Template {
		return model.Err("OWNERSHIP", "Autoscaler target UID changed")
	}
	return nil
}

// autoscalerManifest delegates conservative rate and stabilization behavior to autoscaling/v2.
func (k *K3s) autoscalerManifest(app appRecord) map[string]any {
	a := app.Spec.Autoscaling
	metadata := k.metadata(app.Name, "autoscaler")
	// Kubernetes garbage collection also binds lifecycle to the precise immutable target.
	metadata["ownerReferences"] = []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": app.RuntimeID, "uid": app.Autoscaling.UID}}
	return map[string]any{"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler", "metadata": metadata, "spec": map[string]any{
		"scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": app.RuntimeID},
		"minReplicas":    a.MinReplicas, "maxReplicas": a.MaxReplicas,
		"metrics":  []any{map[string]any{"type": "Resource", "resource": map[string]any{"name": "cpu", "target": map[string]any{"type": "Utilization", "averageUtilization": a.CPUTargetPercent}}}},
		"behavior": map[string]any{"scaleUp": hpaDirection(0, 60), "scaleDown": hpaDirection(300, 300)},
	}}
}

// hpaDirection fixes a single-replica rate rather than exposing unreviewed algorithm parameters.
func hpaDirection(stabilization, period int) map[string]any {
	return map[string]any{"stabilizationWindowSeconds": stabilization, "selectPolicy": "Min", "policies": []any{map[string]any{"type": "Pods", "value": 1, "periodSeconds": period}}}
}

// ReconcileHealth retries durable HPA lifecycle intents after a controller restart.
func (k *K3s) ReconcileHealth(ctx context.Context) (map[string]string, error) {
	k.base.mu.Lock()
	defer k.base.mu.Unlock()
	s, err := k.base.read()
	// An unreadable authoritative observation must not authorize mutation.
	if err != nil {
		return nil, err
	}
	// Native Kubernetes owns replica decisions; this loop only repairs lifecycle intent.
	for _, app := range s.Apps {
		// Ordinary manually scaled applications require no HPA lifecycle work.
		if app.Autoscaling != nil {
			// Stop on uncertain ownership instead of mutating another application.
			if err = k.reconcileAutoscaler(ctx, s, app); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

// autoscalingRevisionEligible rejects paused capacity, old revisions and modified policy inputs.
func autoscalingRevisionEligible(app appRecord) bool {
	return (app.Autoscaling.Phase == "pending" || app.Autoscaling.Phase == "active") && app.Status == "running" && model.DesiredReplicas(app.Spec) > 0 && app.Spec.Autoscaling != nil && app.Autoscaling.Target == app.RuntimeID && app.Autoscaling.Policy == config.Fingerprint(app.Spec.Autoscaling)
}

// rollbackAutoscaling fences a partially installed candidate before restoring its prior revision.
func (d *k3sDeployment) rollbackAutoscaling(ctx context.Context) bool {
	app, ok := d.s.Apps[d.spec.Name]
	// Failures before candidate commit have no candidate replica writer to revoke.
	if !ok || app.Autoscaling == nil || app.Autoscaling.Target != d.version {
		return true
	}
	return d.runtime.pauseAutoscaling(ctx, d.s, d.spec.Name, "candidate rollback") == nil
}

// prepareOwnedTransition fences replica ownership before ordinary transition recovery.
func (d *k3sDeployment) prepareOwnedTransition() error {
	// A failed pause must leave the serving workload and transition journal untouched.
	if err := d.prepareAutoscaling(); err != nil {
		return err
	}
	return d.prepareRecovery()
}

// fencedLifecycleAction removes native replica authority before a manual lifecycle change.
func (k *K3s) fencedLifecycleAction(ctx context.Context, name, namespace, action string, app appRecord, s state, ac model.ActionContext) error {
	// Persist revocation before the external HPA deletion and before all capacity mutations.
	if err := k.pauseAutoscaling(ctx, s, name, "manual "+action); err != nil {
		return err
	}
	return k.applyLifecycleAction(ctx, name, namespace, action, app, s, ac)
}

// restoreRollbackState commits repaired traffic and preserves revoked source delegation.
func (d *k3sDeployment) restoreRollbackState(ctx context.Context) {
	// Caddy needs repair only if this attempt changed routing or stopped its source.
	if d.activated || d.oldStopped {
		_ = d.runtime.base.caddy(ctx, d.s)
	}
	// A pending HPA record may have been committed before its installation failed.
	if d.spec.Autoscaling != nil || d.old.Autoscaling != nil {
		_ = d.runtime.base.save(d.s)
	}
}

// autoscalingEvent records bounded lifecycle evidence linked to the existing approved operation audit.
type autoscalingEvent struct {
	Timestamp int64  `json:"timestamp"`
	Phase     string `json:"phase"`
	Reason    string `json:"reason"`
	Operation string `json:"operation"`
	Target    string `json:"target"`
}

// transition records lifecycle intent, never native HPA internal recommendations or secret inputs.
func (a *autoscalingRecord) transition(phase, reason string) {
	a.Phase = phase
	a.Reason = reason
	a.Events = append(a.Events, autoscalingEvent{Timestamp: time.Now().UnixMilli(), Phase: phase, Reason: reason, Operation: a.ApprovalOperation, Target: a.Target})
	// Bound private journal growth while retaining the latest approved lifecycle transitions.
	if len(a.Events) > 64 {
		a.Events = a.Events[len(a.Events)-64:]
	}
}

// observeAutoscalingOwnership revokes visible external desired-count drift before observing metric health.
func (k *K3s) observeAutoscalingOwnership(ctx context.Context, s state, app appRecord, hpa map[string]any) error {
	agreement, err := k.autoscalerReplicaAgreement(ctx, app, hpa)
	// API failures retain the last known bounded authority without guessing manual intent.
	if err != nil {
		return err
	}
	// Visible external replica changes require a new approval instead of fighting the writer.
	if !agreement {
		return k.pauseAutoscaling(ctx, s, app.Name, "external desired replica drift")
	}
	return k.observeAutoscalingMetrics(ctx, s, app)
}

// autoscalingTargetFailure distinguishes proven drift from a temporarily unavailable cluster API.
func (k *K3s) autoscalingTargetFailure(ctx context.Context, s state, app appRecord, err error) error {
	var failure *model.Error
	// Unknown target state during cluster startup is not evidence of an unauthorized replacement.
	if !errors.As(err, &failure) {
		return err
	}
	// Only authoritative identity or revision failures revoke the installed bounded controller.
	if failure.Code != "OWNERSHIP" && failure.Code != "AUTOSCALING_FENCE" {
		return err
	}
	return k.pauseAutoscaling(ctx, s, app.Name, "target drift: "+err.Error())
}
