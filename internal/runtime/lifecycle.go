package runtime

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"

	"hostlier/internal/model"
)

// Action serializes owned Docker lifecycle changes with private edge updates.
func (r *Runtime) Action(ctx context.Context, name, action string, ac model.ActionContext) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, e := r.read()
	// Require readable runtime ownership state before selecting a workload.
	if e != nil {
		return e
	}
	app, ok := s.Apps[name]
	// Deletion may target retained data after its application container is gone.
	if !ok && action == "delete" {
		app, ok = s.Retained[name]
	}
	// Reject applications absent from the relevant tracked collection.
	if !ok {
		return model.Err("APP_NOT_FOUND", "Application not found")
	}
	app.IdleSleeping = false
	// Group lifecycle keeps sidecars and their network namespace together.
	if app.Spec.Version == 2 {
		return r.groupLifecycle(ctx, name, action, app, s, ac)
	}
	// Verify live container ownership when the application still has a runtime ID.
	if app.RuntimeID != "" {
		// Do not mutate a same-named container whose labels or lookup fail.
		if _, e = r.container(ctx, app.RuntimeID, name); e != nil {
			return e
		}
	}
	// Deletion has a separate retention and optional purge workflow.
	if action == "delete" {
		return r.deleteApplication(ctx, name, app, s, ac)
	}
	return r.changeApplicationState(ctx, name, action, app, s)
}

// Logs returns a bounded decoded tail from an owned application container.
func (r *Runtime) Logs(ctx context.Context, name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, e := r.read()
	// Do not select a workload from unreadable runtime metadata.
	if e != nil {
		return "", e
	}
	app, ok := s.Apps[name]
	// Reject log requests for untracked applications.
	if !ok {
		return "", model.Err("APP_NOT_FOUND", "Application not found")
	}
	// Recheck container ownership before reading workload logs.
	if _, e = r.container(ctx, app.RuntimeID, name); e != nil {
		return "", e
	}
	var data []byte
	// Propagate Docker log-read failures before decoding stream frames.
	if e = r.api(ctx, "GET", "/containers/"+app.RuntimeID+"/logs?stdout=true&stderr=true&tail="+strconv.Itoa(model.LogTail(ctx)), nil, &data, nil); e != nil {
		return "", e
	}
	return decodeDockerLogs(data), nil
}

// Status reports observed Docker state without exposing environment or labels.
func (r *Runtime) Status(ctx context.Context, name string) (map[string]any, error) {
	s, e := r.read()
	// Require durable application state before reporting observed container status.
	if e != nil {
		return nil, e
	}
	app, ok := s.Apps[name]
	// Reject status requests for untracked applications.
	if !ok {
		return nil, model.Err("APP_NOT_FOUND", "Application not found")
	}
	container, e := r.container(ctx, app.RuntimeID, name)
	// Do not report fabricated running state after a failed container lookup.
	if e != nil {
		return nil, e
	}
	out := map[string]any{"runtimeId": container.ID, "running": container.State.Running, "oomKilled": container.State.OOMKilled, "exitCode": container.State.ExitCode, "components": r.dockerComponents(ctx, app), "status": app.Status}
	app.addIdleStatus(out)
	return out, nil
}

// decodeDockerLogs unwraps Docker stream frames and bounds the displayed tail.
func decodeDockerLogs(data []byte) string {
	out := []byte{}
	offset := 0
	// Unwrap only complete Docker multiplexed stream headers.
	for offset+8 <= len(data) && data[offset] <= 2 && data[offset+1] == 0 {
		size := int(binary.BigEndian.Uint32(data[offset+4 : offset+8]))
		// Stop decoding before a truncated frame could read beyond the response.
		if offset+8+size > len(data) {
			break
		}
		out = append(out, data[offset+8:offset+8+size]...)
		offset += 8 + size
	}
	// Preserve raw log data when the response is not multiplexed.
	if offset == 0 {
		out = data
	}
	// Return only the most recent 64 KiB of decoded output.
	if len(out) > 65536 {
		out = out[len(out)-65536:]
	}
	return string(out)
}

// deleteApplication removes routing before the owned workload and retains data by default.
func (r *Runtime) deleteApplication(ctx context.Context, name string, app appRecord, s state, ac model.ActionContext) error {
	var e error

	before := s.Apps[name]
	delete(s.Apps, name)
	// Withdraw routing successfully before removing the application container.
	if e = r.caddy(ctx, s); e != nil {
		return e
	}
	// Retained-only records may have no live container left to remove.
	if app.RuntimeID != "" {
		// Restore the prior edge record if container deletion fails.
		if e = r.api(ctx, "DELETE", "/containers/"+app.RuntimeID+"?force=true", nil, nil, nil); e != nil {
			s.Apps[name] = before
			_ = r.caddy(ctx, s)
			return e
		}
	}
	app.RuntimeID = ""
	app.Status = "deleted"
	s.Retained[name] = app
	// Persist deletion and retention before optionally removing data.
	if e = r.save(s); e != nil {
		return e
	}
	// Earlier revisions can leave stopped containers behind after a cutover.
	// Delete every container owned by this app, while retaining its data volumes.
	if err := r.removeAppContainers(ctx, name); err != nil {
		return err
	}
	// Purge only after the caller's explicit data-deletion approval.
	if ac.Purge {
		// Keep retention metadata if an owned volume cannot be purged.
		if err := r.purgeApplicationVolumes(ctx, name); err != nil {
			return err
		}
		delete(s.Retained, name)
	}
	return r.save(s)
}

// purgeApplicationVolumes checks every volume ownership label before deletion.
func (r *Runtime) purgeApplicationVolumes(ctx context.Context, name string) error {
	// Explicit purge also removes retained replacement containers holding volume references.
	if err := r.removeAppContainers(ctx, name); err != nil {
		return err
	}
	var e error

	filters, _ := json.Marshal(map[string][]string{"label": {"io.hostlier.managed=true", "io.hostlier.owner=" + r.owner, "io.hostlier.app=" + name}})
	var inventory struct {
		Volumes []struct {
			Name   string
			Labels map[string]string
		}
	}
	// Require a successful owned-volume inventory before deleting persistent data.
	if e = r.api(ctx, "GET", "/volumes?filters="+url.QueryEscape(string(filters)), nil, &inventory, nil); e != nil {
		return e
	}
	// Verify each discovered volume independently before removing it.
	for _, v := range inventory.Volumes {
		// A label-filtered inventory still requires an explicit ownership check.
		if !r.owned(v.Labels, name) {
			return model.Err("OWNERSHIP", "Refusing to purge unowned data")
		}
		// Stop purging if Docker rejects any owned volume removal.
		if e = r.api(ctx, "DELETE", "/volumes/"+url.PathEscape(v.Name), nil, nil, nil); e != nil {
			return e
		}
	}

	return nil
}

// changeApplicationState reconciles workload readiness and routes after lifecycle actions.
func (r *Runtime) changeApplicationState(ctx context.Context, name, action string, app appRecord, s state) error {
	var e error

	// Reject unsupported lifecycle action names before contacting Docker.
	if !validLifecycleAction(action) {
		return model.Err("ACTION", "Unknown lifecycle action")
	}
	// Approved desired zero must survive lifecycle restarts.
	if zeroLifecycle(app.Spec, action) {
		return model.Err("SCALE_APPROVAL_REQUIRED", "Scale replicas above zero with an approved scaling plan")
	}
	suffix := "?t=20"
	// Starting needs no stop timeout suffix.
	if action == "start" {
		suffix = ""
	}
	// Do not update metadata after a rejected Docker lifecycle request.
	if e = r.api(ctx, "POST", "/containers/"+app.RuntimeID+"/"+action+suffix, nil, nil, nil); e != nil {
		return e
	}
	// Starting or restarting must restore a healthy private backend before edge activation.
	if action != "stop" {

		port, e := r.backendPort(ctx, app.RuntimeID, app.Spec.Port)
		// Require an allocated owned loopback port before readiness probes.
		if e != nil {
			return e
		}
		app.Upstream = net.JoinHostPort(r.options.UpstreamHost, port)
		path := healthPath(app.Spec)
		// Do not publish running status for an unhealthy application.
		if e = r.healthy(ctx, app.RuntimeID, port, path, r.options.HealthTimeout); e != nil {
			return e
		}
	}
	app.Status = "running"
	// A stopped application must be omitted from live edge routes.
	if action == "stop" {
		app.Status = "stopped"
	}
	s.Apps[name] = app
	// Load the resulting live route set before persisting the lifecycle change.
	if e = r.caddy(ctx, s); e != nil {
		return e
	}
	return r.save(s)
}

// healthPath supplies the root readiness probe when a project has no explicit path.
func healthPath(spec model.Project) string {
	// Use the explicit approved probe path when one was declared.
	if spec.Health != nil && spec.Health.Path != "" {
		return spec.Health.Path
	}
	return "/"
}

// validLifecycleAction bounds runtime mutations to the supported lifecycle operations.
func validLifecycleAction(action string) bool {
	return action == "start" || action == "stop" || action == "restart"
}

// removeAppContainers removes only app-owned containers, including superseded revisions.
func (r *Runtime) removeAppContainers(ctx context.Context, name string) error {
	filters, _ := json.Marshal(map[string][]string{"label": {"io.hostlier.managed=true", "io.hostlier.owner=" + r.owner, "io.hostlier.app=" + name}})
	var containers []struct {
		ID     string
		Labels map[string]string
		Names  []string
	}
	// A bounded owned inventory includes stopped revisions absent from runtime state.
	if err := r.api(ctx, "GET", "/containers/json?all=true&filters="+url.QueryEscape(string(filters)), nil, &containers, nil); err != nil {
		return err
	}
	// Recheck the whole inventory before any irreversible deletion.
	for _, container := range containers {
		// Label-filtered daemon results are still independently ownership checked.
		if !r.owned(container.Labels, name) {
			return model.Err("OWNERSHIP", "Refusing to remove an unowned application container")
		}
	}
	// Namespace holders must be removed after all containers that share their network.
	for _, networkHolder := range []bool{false, true} {
		for _, container := range containers {
			holder := false
			for _, containerName := range container.Names {
				if strings.HasSuffix(containerName, "-network") {
					holder = true
				}
			}
			if holder != networkHolder {
				continue
			}
			if err := r.api(ctx, "DELETE", "/containers/"+url.PathEscape(container.ID)+"?force=true", nil, nil, nil); err != nil && !hasCode(err, "HTTP_404") {
				return err
			}
		}
	}
	return nil
}
