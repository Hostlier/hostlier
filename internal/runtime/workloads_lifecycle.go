package runtime

import (
	"context"
	"hostlier/internal/model"
	"net"
	"net/url"
	"strconv"
)

// groupLifecycle transitions primary and sidecars without losing their namespace.
func (r *Runtime) groupLifecycle(ctx context.Context, name, action string, app appRecord, s state, ac model.ActionContext) error {
	// Validate desired capacity and the action before mutating running processes.
	if err := validateGroupAction(app.Spec, action); err != nil {
		return err
	}
	// A retained record has already withdrawn and removed its running processes.
	if action == "delete" && app.RuntimeID == "" {
		return r.deleteGroup(ctx, name, app, s, ac)
	}
	app, err := r.stopGroupForAction(ctx, name, action, app, s)
	// Failed drain must not proceed to deletion or restart.
	if err != nil {
		return err
	}
	// Deletion retains volumes but removes all group processes and the namespace holder.
	if action == "delete" {
		return r.deleteGroup(ctx, name, app, s, ac)
	}
	// Starting and restarting restore sidecars before running readiness checks.
	if action == "start" || action == "restart" {
		var err error
		app, err = r.startGroup(ctx, app)
		// Failed readiness leaves the group out of public routing.
		if err != nil {
			return err
		}

	}
	s.Apps[name] = app
	// Commit route state before reporting a completed lifecycle transition.
	if err := r.caddy(ctx, s); err != nil {
		return err
	}
	return r.save(s)
}

// deleteGroup removes only recorded workloads and retains owned data unless purged.
func (r *Runtime) deleteGroup(ctx context.Context, name string, app appRecord, s state, ac model.ActionContext) error {
	// Remove sidecars before their primary and namespace owner.
	if err := r.memberAction(ctx, app, "delete"); err != nil {
		return err
	}
	// Clear the primary container before removing network infrastructure.
	if app.RuntimeID != "" {
		// Idempotent ownership checks allow retry after partial cleanup.
		if err := r.groupContainerAction(ctx, name, app.RuntimeID, "delete", 1); err != nil {
			return err
		}
	}
	// A retained deleted record may no longer have an anchor.
	if app.Anchor != "" {
		// Namespace removal must never affect another application.
		if err := r.groupContainerAction(ctx, name, app.Anchor, "delete", 1); err != nil {
			return err
		}
	}
	app.RuntimeID, app.Anchor = "", ""
	app.Members = nil
	app.Status = "deleted"
	delete(s.Apps, name)
	s.Retained[name] = app
	// Persist retention before destructive data cleanup.
	if err := r.save(s); err != nil {
		return err
	}
	// Superseded revisions are no longer needed after the app is deleted.
	if err := r.removeAppContainers(ctx, name); err != nil {
		return err
	}
	// Purge follows the same explicit approval boundary as v1 applications.
	if ac.Purge {
		// Existing purge ownership filters cover primary and member volumes.
		if err := r.purgeApplicationVolumes(ctx, name); err != nil {
			return err
		}
		delete(s.Retained, name)
	}
	return r.save(s)
}

// privateAlias switches stable private discovery only after candidate readiness.
func (d *dockerDeployment) privateAlias() error {
	// Public and listenerless groups need no application DNS alias.
	if d.spec.Endpoint == nil || d.spec.Endpoint.Exposure != "private" {
		return nil
	}
	// Remove the old provider alias before publishing the replacement.
	if d.old.Anchor != "" && d.old.Anchor != d.anchor {
		// A failed network transition must not be reported as an activated provider.
		if err := d.runtime.disconnectGroupNetwork(d.ctx, d.old.Anchor, d.network); err != nil {
			return err
		}
	}
	return d.runtime.aliasGroupNetwork(d.ctx, d.anchor, d.network, d.spec.Name)
}

// disconnectGroupNetwork removes an owned namespace's attachment idempotently.
func (r *Runtime) disconnectGroupNetwork(ctx context.Context, id, network string) error {
	err := r.api(ctx, "POST", "/networks/"+url.PathEscape(network)+"/disconnect", map[string]any{"Container": id, "Force": true}, nil, nil)
	// Docker reports an already disconnected container as a missing endpoint.
	if hasCode(err, "HTTP_404") {
		return nil
	}
	return err
}

// aliasGroupNetwork installs the stable provider alias on the active namespace.
func (r *Runtime) aliasGroupNetwork(ctx context.Context, id, network, name string) error {
	// Reconnecting updates aliases without changing the group's loopback identity.
	if err := r.disconnectGroupNetwork(ctx, id, network); err != nil {
		return err
	}
	return r.api(ctx, "POST", "/networks/"+url.PathEscape(network)+"/connect", map[string]any{"Container": id, "EndpointConfig": map[string]any{"Aliases": []string{name}}}, nil, nil)
}

// ComponentLogs returns only the selected owned member's output.
func (r *Runtime) ComponentLogs(ctx context.Context, name, component string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := r.read()
	// Never infer container identities from an unverified caller string.
	if err != nil {
		return "", err
	}
	app, ok := s.Apps[name]
	// Unknown components cannot select arbitrary Docker containers.
	if !ok || app.Members[component] == "" {
		return "", model.Err("NOT_FOUND", "Component not found")
	}
	id := app.Members[component]
	// Ownership is rechecked before reading any log bytes.
	if _, err = r.container(ctx, id, name); err != nil {
		return "", err
	}
	var data []byte
	err = r.api(ctx, "GET", "/containers/"+id+"/logs?stdout=true&stderr=true&tail="+strconv.Itoa(model.LogTail(ctx)), nil, &data, nil)
	return decodeDockerLogs(data), err
}

// ComponentLogs selects a container only from the retained group specification.
func (k *K3s) ComponentLogs(ctx context.Context, name, component string) (string, error) {
	s, err := k.base.read()
	// Missing metadata cannot authorize a Kubernetes log read.
	if err != nil {
		return "", err
	}
	app, ok := s.Apps[name]
	// Validate component membership before constructing kubectl arguments.
	if !ok || app.Spec.Sidecars[component].Name == "" {
		return "", model.Err("NOT_FOUND", "Component not found")
	}
	raw, err := k.run(ctx, []string{"logs", "deployment/" + app.RuntimeID, "-n", k.namespace(name), "-c", component, "--tail=" + strconv.Itoa(model.LogTail(ctx)), "--limit-bytes=" + strconv.Itoa(65536)}, nil)
	return string(raw), err
}

// startGroup restores all members and verifies readiness before traffic activation.
func (r *Runtime) startGroup(ctx context.Context, app appRecord) (appRecord, error) {
	name := app.Name
	// Restore a stopped namespace holder before its members.
	if err := r.groupContainerAction(ctx, name, app.Anchor, "start", 20); err != nil {
		return app, err
	}
	// Dependency availability precedes primary readiness.
	if err := r.memberAction(ctx, app, "start"); err != nil {
		return app, err
	}
	d := dockerDeployment{runtime: r, ctx: ctx, spec: app.Spec, anchor: app.Anchor, members: app.Members, candidate: dockerContainer{ID: app.RuntimeID}}
	// Restart preserves the same dependency-ready boundary as a fresh deployment.
	if err := d.membersReady(); err != nil {
		return app, err
	}
	// Primary startup uses the same namespace as before shutdown.
	if err := r.groupContainerAction(ctx, name, app.RuntimeID, "start", model.GraceSeconds(app.Spec)); err != nil {
		return app, err
	}
	// Do not publish a group that fails its required probes.
	if err := d.groupReadiness(); err != nil {
		return app, err
	}
	app.Status = "running"
	// Public upstream belongs to the namespace holder rather than the primary container.
	if model.Public(app.Spec) {
		app.Upstream = net.JoinHostPort(r.options.UpstreamHost, d.port)
	}
	return app, nil
}

// stopGroupForAction drains primary before its dependencies and withdraws ingress first.
func (r *Runtime) stopGroupForAction(ctx context.Context, name, action string, app appRecord, s state) (appRecord, error) {
	// Withdraw ingress before draining the primary and its dependencies.
	if action != "start" {
		app.Status = "stopped"
		s.Apps[name] = app
		// A failed route withdrawal must leave running capacity untouched.
		if err := r.caddy(ctx, s); err != nil {
			return app, err
		}
		// The primary drains while its sidecars remain available.
		if err := r.groupContainerAction(ctx, name, app.RuntimeID, "stop", model.GraceSeconds(app.Spec)); err != nil {
			return app, err
		}
		// Stop dependencies only after the primary has terminated.
		if err := r.memberAction(ctx, app, "stop"); err != nil {
			return app, err
		}
		// An explicit stop must also release the namespace holder's CPU and port.
		if action == "stop" && app.Anchor != "" {
			if err := r.groupContainerAction(ctx, name, app.Anchor, "stop", model.GraceSeconds(app.Spec)); err != nil {
				return app, err
			}
		}
	}
	return app, nil
}

// stopZeroAnchor leaves no runnable namespace infrastructure at zero desired capacity.
func (d *dockerDeployment) stopZeroAnchor() error {
	// Legacy zero deployments have no separate namespace holder.
	if d.anchor == "" {
		return nil
	}
	return d.runtime.groupContainerAction(d.ctx, d.spec.Name, d.anchor, "stop", 1)
}

// validateGroupAction keeps lifecycle verbs inside the approved desired capacity.
func validateGroupAction(p model.Project, action string) error {
	// Lifecycle commands cannot override an explicitly approved zero replica count.
	if zeroLifecycle(p, action) {
		return model.Err("SCALE_REQUIRED", "Scale above zero before starting this group")
	}
	// Only explicit lifecycle verbs may reach Docker.
	if !validLifecycleAction(action) && action != "delete" {
		return model.Err("ACTION", "Unknown lifecycle action")
	}
	return nil
}
