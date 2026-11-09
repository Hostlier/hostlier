package runtime

import (
	"context"
	"net"
	"net/http"
	"time"

	"hostlier/internal/model"
)

// Stop/start only the committed application and its optional namespace holder.
// Inspect ownership and current state before each action so retries are idempotent.
func (r *Runtime) idleCapacity(ctx context.Context, app appRecord, awake bool) (string, error) {
	// Readiness and port discovery depend on a successful process transition.
	if err := r.setIdleContainers(ctx, app, awake); err != nil {
		return "", err
	}
	// A sleeping service retains its former address until the next start.
	if !awake {
		return app.Upstream, nil
	}
	portOwner := app.RuntimeID
	// Grouped containers share the namespace holder's port mapping.
	if app.Anchor != "" {
		portOwner = app.Anchor
	}
	port, err := r.backendPort(ctx, portOwner, app.Spec.Port)
	// A stale host port must never receive the queued request.
	if err != nil {
		return "", err
	}
	// Use the same HTTP or exec readiness policy as a normal deployment.
	if err := r.memberReady(ctx, app.Name, app.RuntimeID, portOwner, app.Spec); err != nil {
		return "", err
	}
	return net.JoinHostPort(r.options.UpstreamHost, port), nil
}

// Preserve the network namespace lifetime while starting or stopping a version-two group.
func (r *Runtime) setIdleContainers(ctx context.Context, app appRecord, awake bool) error {
	action := "stop"
	// Only a wake may start retained processes.
	if awake {
		action = "start"
	}
	// The public port belongs to the namespace holder on version-two Docker services.
	if awake && app.Anchor != "" {
		// The namespace holder must exist before the primary starts.
		if err := r.groupContainerAction(ctx, app.Name, app.Anchor, action, model.GraceSeconds(app.Spec)); err != nil {
			return err
		}
	}
	// All process transitions go through ownership-checked, idempotent Docker actions.
	if err := r.groupContainerAction(ctx, app.Name, app.RuntimeID, action, model.GraceSeconds(app.Spec)); err != nil {
		return err
	}
	// Stop the namespace only after the primary process exits.
	if !awake && app.Anchor != "" {
		// A failed namespace stop leaves sleep intent pending for recovery.
		if err := r.groupContainerAction(ctx, app.Name, app.Anchor, action, model.GraceSeconds(app.Spec)); err != nil {
			return err
		}
	}
	return nil
}

// Scale the retained owned Kubernetes deployment without rebuilding its image or creating a revision.
func (k *K3s) idleCapacity(ctx context.Context, app appRecord, awake bool) (string, error) {
	ns := k.namespace(app.Name)
	obj, err := k.get(ctx, "deployment", app.RuntimeID, ns)
	// A failed lookup cannot authorize any scaling.
	if err != nil {
		return "", err
	}
	// Reject a same-named object created by a different owner.
	if !k.ownsApplication(obj, app.Name) {
		return "", model.Err("OWNERSHIP", "Idle scaling requires the committed owned deployment")
	}
	count := 0
	// A wake restores exactly one already approved replica.
	if awake {
		count = 1
	}
	// Do not claim the requested transition if Kubernetes rejects the write.
	if err := k.scale(ctx, ns, app.RuntimeID, count); err != nil {
		return "", err
	}
	// A stop needs no running endpoint. A wake must establish application readiness.
	if awake {
		// Wait for the Deployment rollout before testing its Service.
		if err := k.ready(ctx, ns, app.RuntimeID); err != nil {
			return "", err
		}
		// Pod readiness can precede propagation to the private Service.
		if err := k.waitIdleService(ctx, app); err != nil {
			return "", err
		}
	}
	return app.Upstream, nil
}

// The Service may lag rollout readiness. Probe its private endpoint before
// forwarding a customer request, which could be a non-idempotent POST.
func (k *K3s) waitIdleService(ctx context.Context, app appRecord) error {
	// Retry only health probes, never the original public request.
	for {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		req, err := http.NewRequestWithContext(probe, "GET", "http://"+app.Upstream+healthPath(app.Spec), nil)
		// Malformed health paths cannot identify a ready endpoint.
		if err != nil {
			cancel()
			return err
		}
		res, err := k.base.web.Do(req)
		// A completed probe always releases its response body.
		if res != nil {
			res.Body.Close()
		}
		cancel()
		// Forward only after the service satisfies the HTTP readiness check.
		if err == nil && res.StatusCode >= 200 && res.StatusCode < 300 {
			return nil
		}
		// Respect the shared cold start deadline while waiting for endpoints.
		if err := pause(ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
}

// addIdleStatus distinguishes an automatically sleeping workload from a manual stop.
func (a appRecord) addIdleStatus(out map[string]any) {
	// Ordinary deployments retain their existing status response shape.
	if a.Spec.SleepAfter != "" {
		out["idleSleeping"] = a.IdleSleeping
		// Distinguish reversible idle sleep from administrative stop.
		if a.IdleSleeping {
			out["status"] = "sleeping"
		}
	}
}
