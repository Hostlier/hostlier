package runtime

import (
	"context"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
	"net"
	"net/url"
	goruntime "runtime"
	"strconv"
	"time"
)

// prepareGroup creates the stable Docker namespace before any member starts.
func (d *dockerDeployment) prepareGroup() error {
	// Legacy deployments retain their existing single-container networking.
	if d.spec.Version != 2 {
		return nil
	}
	d.members = map[string]string{}
	// Never mutate a deployment without its approved infrastructure image.
	if err := d.runtime.pullMember(d.ctx, d.spec.NamespaceImage); err != nil {
		return err
	}
	// Copy resolved bindings before injecting private endpoint values.
	if d.dc.Secrets == nil {
		d.dc.Secrets = map[string]string{}
	}
	providers, err := resolveWorkloadBindings(d.spec, d.s, d.dc.Secrets, d.runtime.privateAddress)
	// Private authorization must be satisfied before creating the namespace.
	if err != nil {
		return err
	}
	endpoints := map[string]any{d.network: map[string]any{}}
	// Include only controller-validated resource networks.
	for _, network := range d.dc.DependencyNetworks {
		endpoints[network] = map[string]any{}
	}
	// Provider networks are checked against their recorded consumer grants above.
	for _, provider := range providers {
		endpoints[d.runtime.applicationNetwork(provider)] = map[string]any{}
	}
	host := map[string]any{"NetworkMode": d.network, "RestartPolicy": map[string]string{"Name": "unless-stopped"}}
	// Only public primary listeners receive a host-loopback publication.
	if model.Public(d.spec) {
		bindings, err := endpointPortBindings(d.spec)
		// Fail before container creation when no private port can be reserved.
		if err != nil {
			return err
		}
		host["PortBindings"] = bindings
	}
	body := map[string]any{"Image": d.spec.NamespaceImage, "HostConfig": host, "NetworkingConfig": map[string]any{"EndpointsConfig": endpoints}}
	d.anchor, err = d.runtime.createGroupContainer(d.ctx, d.spec.Name, d.name+"-network", body)
	// Namespace creation must finish before starting member processes.
	if err != nil {
		return err
	}
	return d.runtime.groupContainerAction(d.ctx, d.spec.Name, d.anchor, "start", model.GraceSeconds(d.spec))
}

// applicationNetwork returns the owned stable network name of an application.
func (r *Runtime) applicationNetwork(name string) string { return "hostlier-" + r.owner + "-" + name }

// privateAddress resolves a provider through the stable alias on its owned network.
func (r *Runtime) privateAddress(name string, p model.Project) string {
	return "http://" + net.JoinHostPort(name, strconv.Itoa(p.Port))
}

// privatePortBinding reserves one loopback port for the public primary only.
func privatePortBinding(port int) (map[string]any, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	// Never fall back to wildcard publication when reservation fails.
	if err != nil {
		return nil, err
	}
	defer l.Close()
	return map[string]any{strconv.Itoa(port) + "/tcp": []any{map[string]string{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(l.Addr().(*net.TCPAddr).Port)}}}, nil
}

// createGroupContainer reconciles a deterministic owned container without starting code.
func (r *Runtime) createGroupContainer(ctx context.Context, app, name string, body map[string]any) (string, error) {
	existing, err := r.container(ctx, name, app)
	// Replays reuse only containers whose ownership was checked by inspection.
	if err == nil {
		return existing.ID, nil
	}
	// Unexpected inspection failures must never trigger duplicate creation.
	if !hasCode(err, "HTTP_404") {
		return "", err
	}
	body["Labels"] = r.labels(app)
	var created struct{ ID string }
	err = r.api(ctx, "POST", "/containers/create?platform=linux%2F"+goruntime.GOARCH+"&name="+url.QueryEscape(name), body, &created, nil)
	return created.ID, err
}

// candidateV2 creates the primary inside the group's network namespace.
func (d *dockerDeployment) candidateV2() error {
	id, err := d.createMember("", d.spec, d.mounts)
	// Do not inspect an unsuccessful creation as if it were a live candidate.
	if err != nil {
		return err
	}
	d.candidate, err = d.runtime.container(d.ctx, id, d.spec.Name)
	return err
}

// createMember scopes executable configuration, secrets and mounts to one member.
func (d *dockerDeployment) createMember(name string, p model.Project, mounts []any) (string, error) {
	files, err := d.configMounts(p, name)
	// Read-only managed files are complete before the process is created.
	if err != nil {
		return "", err
	}
	mounts = append(append([]any{}, mounts...), files...)
	host := map[string]any{"NetworkMode": "container:" + d.anchor, "RestartPolicy": map[string]string{"Name": "unless-stopped"}, "Mounts": mounts, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "10m", "max-file": "3"}}}
	// Per-member limits cannot be borrowed from the primary's resource allocation.
	if err := applyContainerLimits(host, p.Resources); err != nil {
		return "", err
	}
	body := map[string]any{"Image": artifact.RuntimeImage(p.Image), "HostConfig": host, "Env": memberEnvironment(p, name, d.dc.Secrets), "StopTimeout": model.GraceSeconds(p)}
	// Enforce the reviewed nonroot and kernel confinement before any container is created.
	if err := applyContainerSecurity(p, host, body); err != nil {
		return "", err
	}
	// Docker continuously executes approved in-container readiness independently of the controller.
	if p.Health != nil && len(p.Health.Exec) > 0 {
		body["Healthcheck"] = map[string]any{"Test": append([]string{"CMD"}, p.Health.Exec...), "Interval": int64(2 * time.Second), "Timeout": int64(2 * time.Second), "Retries": 1}
	}
	// Override entrypoints only through literal exec vectors.
	if len(p.Command) > 0 {
		body["Entrypoint"] = p.Command
	}
	// Arguments remain independent of entrypoint selection.
	if len(p.Args) > 0 {
		body["Cmd"] = p.Args
	}
	suffix := ""
	// Sidecar identities are distinct from the primary and infrastructure holder.
	if name != "" {
		suffix = "-sidecar-" + name
	}
	return d.runtime.createGroupContainer(d.ctx, d.spec.Name, d.name+suffix, body)
}

// startMembers provisions scoped sidecar mounts and starts each approved image.
func (d *dockerDeployment) startMembers() error {
	// Version one has no group member collection.
	if d.spec.Version != 2 {
		return nil
	}
	// Stable ordering makes failed attempts reproducible and safely recoverable.
	for _, name := range model.MemberNames(d.spec) {
		p := d.spec.Sidecars[name]
		// All member images are pinned by the controller before approval.
		if err := d.runtime.pullMember(d.ctx, p.Image); err != nil {
			return err
		}
		mounts, err := d.memberMounts(name, p)
		// Storage must exist before a process can write to it.
		if err != nil {
			return err
		}
		id, err := d.createMember(name, p, mounts)
		// Record a created member immediately so rollback can remove it.
		if id != "" {
			d.members[name] = id
		}
		// Failed creation must not be followed by a start request.
		if err != nil {
			return err
		}
		// Zero desired capacity retains concrete stopped members for later deletion or scale-up.
		if model.DesiredReplicas(d.spec) == 0 {
			continue
		}
		// Start only an owned process in this operation's namespace.
		if err = d.runtime.groupContainerAction(d.ctx, d.spec.Name, id, "start", model.GraceSeconds(p)); err != nil {
			return err
		}
	}
	return nil
}

// memberMounts creates persistent storage without implicitly sharing the primary's volumes.
func (d *dockerDeployment) memberMounts(name string, p model.Project) ([]any, error) {
	mounts := []any{}
	// Component-qualified volume keys preserve ownership and avoid sibling collisions.
	for key, v := range p.Volumes {
		volume, err := d.runtime.ensureVolume(d.ctx, d.spec.Name, "sidecar-"+name+"-"+key)
		// Failed storage preparation must not start a partial writer.
		if err != nil {
			return nil, err
		}
		d.volumes = append(d.volumes, volume)
		mounts = append(mounts, map[string]any{"Type": "volume", "Source": volume, "Target": v.Mount})
	}
	return mounts, nil
}

// groupContainerAction verifies ownership and keeps retries idempotent.
func (r *Runtime) groupContainerAction(ctx context.Context, app, id, action string, grace int) error {
	c, err := r.container(ctx, id, app)
	// Missing containers are already removed only for explicit cleanup.
	if hasCode(err, "HTTP_404") && action == "delete" {
		return nil
	}
	// Refuse to touch containers with unknown ownership or unreadable state.
	if err != nil {
		return err
	}
	// Replayed starts and stops do not restart running or stopped workloads.
	if action == "start" && c.State.Running || action == "stop" && !c.State.Running {
		return nil
	}
	// Removal happens only after a caller selected owned cleanup.
	if action == "delete" {
		return r.api(ctx, "DELETE", "/containers/"+id+"?force=true", nil, nil, nil)
	}
	return r.api(ctx, "POST", "/containers/"+id+"/"+action+"?t="+strconv.Itoa(grace), nil, nil, nil)
}

// memberAction applies lifecycle actions only to recorded members of this group.
func (r *Runtime) memberAction(ctx context.Context, app appRecord, action string) error {
	// Each component uses its own approved termination window.
	for _, name := range model.MemberNames(app.Spec) {
		id := app.Members[name]
		// Legacy records have no sidecar runtime identity.
		if id == "" {
			continue
		}
		// Keep failures observable instead of claiming the group transitioned completely.
		if err := r.groupContainerAction(ctx, app.Name, id, action, model.GraceSeconds(app.Spec.Sidecars[name])); err != nil {
			return err
		}
	}
	return nil
}

// groupReadiness probes all required members before publishing the primary.
func (d *dockerDeployment) groupReadiness() error {
	// Validate the primary even when it has no public listener.
	if err := d.runtime.memberReady(d.ctx, d.spec.Name, d.candidate.ID, d.anchor, d.spec); err != nil {
		return err
	}
	// Required dependencies remain healthy through primary startup.
	if err := d.membersReady(); err != nil {
		return err
	}
	// Only a public primary has a host-loopback upstream.
	if model.Public(d.spec) {
		port, err := d.runtime.backendPort(d.ctx, d.anchor, d.spec.Port)
		d.port = port
		return err
	}
	return nil
}

// memberReady executes private probes in their container and preserves public HTTP support.
func (r *Runtime) memberReady(ctx context.Context, app, id, anchor string, p model.Project) error {
	// Exec probes can reach loopback without publishing any sidecar or worker port.
	if p.Health != nil && len(p.Health.Exec) > 0 {
		return r.execReady(ctx, app, id, p.Health)
	}
	port, err := r.backendPort(ctx, anchor, p.Port)
	// A failed port lookup cannot establish readiness.
	if err != nil {
		return err
	}
	timeout := r.options.HealthTimeout
	// Every public primary honors the same approved startup budget as exec-probed members.
	if p.Health != nil && p.Health.TimeoutSeconds > 0 {
		timeout = time.Duration(p.Health.TimeoutSeconds) * time.Second
	}
	return r.healthy(ctx, id, port, healthPath(p), timeout)
}

// execReady retries bounded exec probes without relying on a shell in the image.
func (r *Runtime) execReady(ctx context.Context, app, id string, h *model.Health) error {
	timeout := r.options.HealthTimeout
	// The project can extend startup time within the schema's bound.
	if h.TimeoutSeconds > 0 {
		timeout = time.Duration(h.TimeoutSeconds) * time.Second
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// A probe can fail while the process is still initializing.
	for bounded.Err() == nil {
		c, err := r.container(bounded, id, app)
		// Exited or inaccessible containers cannot pass readiness.
		if err != nil || !c.State.Running {
			return model.Err("APPLICATION_EXIT", "Group member exited before readiness")
		}
		// Only a successful process exit establishes readiness.
		if r.execProbe(bounded, id, h.Exec) == nil {
			return nil
		}
		// Cancellation bounds retries and controller shutdown latency.
		if err = pause(bounded, 300*time.Millisecond); err != nil {
			break
		}
	}
	return model.Err("HEALTH_TIMEOUT", "Group member did not pass its exec readiness probe")
}

// execProbe waits for a detached bounded probe and never returns its output or secrets.
func (r *Runtime) execProbe(ctx context.Context, id string, command []string) error {
	var execution struct{ ID string }
	// Detached execution avoids unbounded stdout and Docker stream hijacking.
	if err := r.api(ctx, "POST", "/containers/"+id+"/exec", map[string]any{"Cmd": command, "AttachStdout": false, "AttachStderr": false}, &execution, nil); err != nil {
		return err
	}
	// Docker owns the probe process; readiness waits only for its exit status.
	if err := r.api(ctx, "POST", "/exec/"+execution.ID+"/start", map[string]any{"Detach": true}, nil, nil); err != nil {
		return err
	}
	// The outer startup deadline bounds an individual stuck probe as well.
	for ctx.Err() == nil {
		var state struct {
			Running  bool
			ExitCode int
		}
		// Inspection failure cannot be interpreted as successful exit.
		if err := r.api(ctx, "GET", "/exec/"+execution.ID+"/json", nil, &state, nil); err != nil {
			return err
		}
		// Only exit code zero proves readiness.
		if !state.Running {
			// Nonzero status retries under the outer readiness deadline.
			if state.ExitCode != 0 {
				return model.Err("PROBE_FAILED", "Exec probe failed")
			}
			return nil
		}
		// Poll without blocking cancellation.
		if err := pause(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// removeCandidateMembers clears created sidecars before deleting the namespace.
func (d *dockerDeployment) removeCandidateMembers(ctx context.Context) {
	// Partial creation must clean up every member already recorded.
	for _, id := range d.members {
		_ = d.runtime.groupContainerAction(ctx, d.spec.Name, id, "delete", 1)
	}
}

// removeGroupAnchor releases only the failed attempt's namespace holder.
func (d *dockerDeployment) removeGroupAnchor(ctx context.Context) {
	// Never delete a committed namespace while replaying its operation.
	if d.anchor != "" && d.anchor != d.old.Anchor {
		_ = d.runtime.groupContainerAction(ctx, d.spec.Name, d.anchor, "delete", 1)
	}
}

// startGroupMembers fences volume writers and establishes dependencies before starting the primary.
func (d *dockerDeployment) startGroupMembers() error {
	// Legacy workloads retain their original start path.
	if d.spec.Version != 2 {
		return nil
	}
	// No new member may mount retained writable storage while the old group still writes.
	if d.old.RuntimeID != "" && d.old.RuntimeID != d.candidate.ID && model.HasVolumes(d.spec) {
		// Rollback retains the old-running flag before any new dependency starts.
		if err := d.stopPreviousWriter(); err != nil {
			return err
		}
	}
	// Create and start only the approved member images.
	if err := d.startMembers(); err != nil {
		return err
	}
	return d.membersReady()
}

// membersReady gates startup and publication on required sidecar readiness.
func (d *dockerDeployment) membersReady() error {
	// Sorted iteration makes readiness failures deterministic.
	for _, name := range model.MemberNames(d.spec) {
		p := d.spec.Sidecars[name]
		// Optional probes are observed by the daemon but do not delay primary readiness.
		if p.Required != nil && !*p.Required {
			continue
		}
		// Every required sidecar must be ready before the group can serve.
		if err := d.runtime.memberReady(d.ctx, d.spec.Name, d.members[name], d.anchor, p); err != nil {
			return err
		}
	}
	return nil
}
