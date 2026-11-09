package runtime

import (
	"context"
	"hostlier/internal/model"
	"net/url"
)

// ReconcileHealth withdraws unhealthy required groups and restores them after recovery.
func (r *Runtime) ReconcileHealth(ctx context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := r.read()
	// Unreadable authoritative state must never invent runnable workloads.
	if err != nil {
		return nil, err
	}
	changed := map[string]string{}
	// Only actively managed v2 groups participate in continuous readiness.
	for name, app := range s.Apps {
		// Stopped and explicitly zero-scaled groups stay stopped regardless of process probes.
		if !monitoredGroup(app) {
			continue
		}
		status := "running"
		// Required process health is the group availability contract.
		if !r.groupHealthy(ctx, app) {
			status = "degraded"
		}
		// Stable readiness avoids unnecessary edge reloads and state writes.
		if status == app.Status {
			continue
		}
		// Private DNS is withdrawn alongside public routing on required member failure.
		if err = r.healthDiscovery(ctx, app, status); err != nil {
			return nil, err
		}
		app.Status = status
		s.Apps[name] = app
		changed[name] = status
	}
	// No changes means no edge or durable state mutation.
	if len(changed) == 0 {
		return changed, nil
	}
	// Edge withdrawal must succeed before the controller reports a completed health transition.
	if err = r.caddy(ctx, s); err != nil {
		return nil, err
	}
	return changed, r.save(s)
}

// monitoredGroup excludes legacy and administratively stopped deployments.
func monitoredGroup(app appRecord) bool {
	return !app.IdleSleeping && app.Spec.Version == 2 && (app.Status == "running" || app.Status == "degraded") && model.DesiredReplicas(app.Spec) > 0
}

// groupHealthy requires the primary and every required sidecar to be available.
func (r *Runtime) groupHealthy(ctx context.Context, app appRecord) bool {
	// A stopped or failed primary cannot expose a healthy group.
	if !r.componentHealthy(ctx, app.Name, app.RuntimeID) {
		return false
	}
	// Optional members remain observable but do not withdraw the primary route.
	for name, member := range app.Spec.Sidecars {
		// Explicit false is the only opt-out from group readiness.
		if member.Required != nil && !*member.Required {
			continue
		}
		// Member IDs come exclusively from the owned runtime journal.
		if !r.componentHealthy(ctx, app.Name, app.Members[name]) {
			return false
		}
	}
	return true
}

// componentHealthy reads bounded Docker state without running or exposing probe output.
func (r *Runtime) componentHealthy(ctx context.Context, app, id string) bool {
	c, err := r.container(ctx, id, app)
	return err == nil && c.State.Running && c.State.Health.Status != "unhealthy"
}

// healthDiscovery keeps private network reachability aligned with group readiness.
func (r *Runtime) healthDiscovery(ctx context.Context, app appRecord, status string) error {
	// Only standalone private listeners have a discoverable private network address.
	if app.Spec.Endpoint == nil || app.Spec.Endpoint.Exposure != "private" {
		return nil
	}
	network := r.applicationNetwork(app.Name)
	// Recovery restores the same stable DNS identity after required components become healthy.
	if status == "running" {
		return r.aliasGroupNetwork(ctx, app.Anchor, network, app.Name)
	}
	return r.api(ctx, "POST", "/networks/"+url.PathEscape(network)+"/disconnect", map[string]any{"Container": app.Anchor, "Force": true}, nil, nil)
}
