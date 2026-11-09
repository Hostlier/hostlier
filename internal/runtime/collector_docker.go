package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"path/filepath"
	goruntime "runtime"
	"time"

	"hostlier/internal/model"
)

// collectorContainer contains only lifecycle and identity data needed for system supervision.
type collectorContainer struct {
	ID           string `json:"Id"`
	RestartCount int
	Config       struct {
		Image  string
		Labels map[string]string
	}
	State struct {
		Running, Restarting, OOMKilled bool
		Health                         struct{ Status string }
	}
}

// EnsureCollector reconciles the deterministic Docker singleton without acquiring the application lock.
func (r *Runtime) EnsureCollector(ctx context.Context, spec model.CollectorDeployment) error {
	r.collectorMu.Lock()
	defer r.collectorMu.Unlock()
	p, err := prepareCollector(spec)
	// Invalid specifications cannot replace a currently working system collector.
	if err != nil {
		return err
	}
	current, err := r.inspectCollector(ctx)
	// A foreign name collision is an operator-visible error, never an invitation to adopt.
	if err != nil {
		return err
	}
	// Registry trust is repaired even when the running collector revision is unchanged.
	if err = r.installCollectorTrust(p); err != nil {
		return err
	}
	// Matching revisions reuse the existing singleton and repair a stopped process in place.
	if current.ID != "" && current.Config.Image == spec.Image && current.Config.Labels[collectorHashLabel] == p.revision {
		return r.startCollector(ctx, current)
	}
	return r.replaceCollector(ctx, p, current)
}

// inspectCollector distinguishes absence from an explicitly owned system collector.
func (r *Runtime) inspectCollector(ctx context.Context) (collectorContainer, error) {
	var current collectorContainer
	err := r.api(ctx, "GET", "/containers/"+collectorName+"/json", nil, &current, nil)
	// A missing singleton is repaired through creation at the same deterministic name.
	if hasCode(err, "HTTP_404") {
		return collectorContainer{}, nil
	}
	// Runtime transport failures must not be interpreted as permission to create a duplicate.
	if err != nil {
		return current, err
	}
	// Collector-specific labels avoid selecting the system process as an application log source.
	if current.Config.Labels["io.hostlier.owner"] != r.owner || current.Config.Labels["io.hostlier.collector"] != "true" {
		return current, model.Err("OWNERSHIP", "Refusing to replace an unowned collector container")
	}
	return current, nil
}

// replaceCollector prepares a pinned image and protected files before disturbing the current process.
func (r *Runtime) replaceCollector(ctx context.Context, p preparedCollector, current collectorContainer) error {
	// Registry failure must leave the old collector running with its existing credential.
	if err := r.pullCollector(ctx, p); err != nil {
		return err
	}
	dir, err := r.collectorFiles(p)
	// Missing configuration or trust files must not terminate the previous healthy revision.
	if err != nil {
		return err
	}
	// Exactly one deterministic process may own the daemon socket/log mounts at a time.
	if current.ID != "" {
		if err = r.removeCollector(ctx, current); err != nil { // Removal is restricted to the inspected owned container ID.
			return err
		}
	}
	var created struct{ ID string }
	// The daemon resolves a racing name collision; this path never force-adopts it.
	if err = r.api(ctx, "POST", "/containers/create?platform=linux%2F"+goruntime.GOARCH+"&name="+collectorName, r.collectorContainerBody(p, dir), &created, nil); err != nil {
		return err
	}
	current.ID = created.ID
	current.State.Running = false
	return r.startCollector(ctx, current)
}

// pullCollector sends the ingest-only credential exclusively to the matching controller registry.
func (r *Runtime) pullCollector(ctx context.Context, p preparedCollector) error {
	auth, _ := json.Marshal(RegistryCredential{Username: "collector", Password: p.spec.Token, ServerAddress: p.registry})
	var progress []byte
	headers := map[string]string{"X-Registry-Auth": base64.URLEncoding.EncodeToString(auth)}
	// Docker uses host registry trust; this adapter never disables TLS certificate verification.
	if err := r.api(ctx, "POST", "/images/create?platform=linux%2F"+goruntime.GOARCH+"&fromImage="+url.QueryEscape(p.spec.Image), nil, &progress, headers); err != nil {
		return err
	}
	// Layer errors may be embedded in a successful HTTP streaming response.
	for _, line := range bytes.Split(progress, []byte{'\n'}) {
		var event map[string]any
		if json.Unmarshal(line, &event) == nil && event["error"] != nil { // Do not surface registry responses containing credentials.
			return model.Err("COLLECTOR_PULL", "Collector image pull failed")
		}
	}
	return nil
}

// collectorFiles stores each immutable configuration and credential in a private revision directory.
func (r *Runtime) collectorFiles(p preparedCollector) (string, error) {
	dir, err := r.collectorDirectory(p.revision)
	// A protected state directory is required before writing credentials.
	if err != nil {
		return "", err
	}
	files := map[string][]byte{"collector.yaml": []byte(p.config), "collector.env": []byte("HOSTLIER_TELEMETRY_TOKEN=" + p.spec.Token + "\n")}
	// Only an explicitly supplied public trust bundle gets a mounted CA file.
	if len(p.ca) > 0 {
		files["ca.pem"] = p.ca
	}
	for name, data := range collectorTLSFiles(p.spec.TLS) { // Dedicated TLS material remains in the private immutable revision.
		files[name] = data
	}
	for name, data := range files { // Atomic writes cannot expose partially rotated configuration.
		if err = writeCollectorFile(filepath.Join(dir, name), data); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// collectorContainerBody gives the singleton the bounded, nonprivileged system collector contract.
func (r *Runtime) collectorContainerBody(p preparedCollector, dir string) map[string]any {
	mounts := []any{collectorBind(filepath.Join(dir, "collector.yaml"), "/etc/otelcol/collector.yaml"), collectorBind(r.options.SocketPath, "/var/run/docker.sock"), collectorBind("/var/lib/docker/containers", "/var/lib/docker/containers")}
	// Exporter trust is copied into the immutable revision rather than binding arbitrary source paths.
	if len(p.ca) > 0 {
		mounts = append(mounts, collectorBind(filepath.Join(dir, "ca.pem"), "/etc/otelcol/ca.pem"))
	}
	for name := range collectorTLSFiles(p.spec.TLS) { // TLS private keys are visible only through read-only mounts.
		mounts = append(mounts, collectorBind(filepath.Join(dir, name), "/etc/otelcol/tls/"+name))
	}
	return map[string]any{"Image": p.spec.Image, "Cmd": []string{"--config=/etc/otelcol/collector.yaml"}, "User": "0:0", "Env": []string{"HOSTLIER_TELEMETRY_TOKEN=" + p.spec.Token, "GOMEMLIMIT=96MiB"}, "Labels": map[string]string{"io.hostlier.owner": r.owner, "io.hostlier.collector": "true", collectorHashLabel: p.revision}, "HostConfig": map[string]any{"RestartPolicy": map[string]string{"Name": "unless-stopped"}, "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "Memory": int64(256 << 20), "NanoCpus": int64(500000000), "Mounts": mounts, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "1m", "max-file": "2"}}}}
}

// collectorBind expresses a read-only mount without publishing any host port.
func collectorBind(source, target string) map[string]any {
	return map[string]any{"Type": "bind", "Source": source, "Target": target, "ReadOnly": true}
}

// removeCollector stops and removes only the exact previously ownership-checked singleton.
func (r *Runtime) removeCollector(ctx context.Context, current collectorContainer) error {
	// Stopped containers need no shutdown request before replacement.
	if current.State.Running {
		err := r.api(ctx, "POST", "/containers/"+url.PathEscape(current.ID)+"/stop?t=10", nil, nil, nil)
		if err != nil && !hasCode(err, "HTTP_304") { // Already stopped is safe; other daemon failures preserve the container.
			return err
		}
	}
	return r.api(ctx, "DELETE", "/containers/"+url.PathEscape(current.ID), nil, nil, nil)
}

// startCollector repairs stopped state and waits for process readiness after each start.
func (r *Runtime) startCollector(ctx context.Context, current collectorContainer) error {
	// An unchanged already-running revision does not restart every reconciliation interval.
	if current.State.Running {
		return collectorDockerReady(current)
	}
	// Start uses the recorded daemon ID, never an unverified replacement at the same name.
	if err := r.api(ctx, "POST", "/containers/"+url.PathEscape(current.ID)+"/start", nil, nil, nil); err != nil {
		return err
	}
	return r.waitCollectorDocker(ctx, current.ID)
}

// collectorDockerReady distinguishes running healthy processes from crash loops and failed health checks.
func collectorDockerReady(current collectorContainer) error {
	// The minimal binary has no extra health extension; Docker health is honored when configured.
	if !current.State.Running || current.State.Restarting || current.State.OOMKilled || (current.State.Health.Status != "" && current.State.Health.Status != "healthy") {
		return model.Err("COLLECTOR_NOT_READY", "Collector process is not ready")
	}
	return nil
}

// waitCollectorDocker catches immediate configuration crashes before reporting initial provisioning ready.
func (r *Runtime) waitCollectorDocker(ctx context.Context, id string) error {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select { // Give a newly started collector time to parse configuration and initialize receivers.
	case <-bounded.Done():
		return model.Err("COLLECTOR_NOT_READY", "Collector readiness deadline exceeded")
	// Successful startup waits for a bounded readiness observation.
	case <-timer.C:
	}
	current, err := r.inspectCollector(bounded)
	// A replacement race or disappeared process cannot satisfy the requested start operation.
	if err != nil || current.ID != id {
		return model.Err("COLLECTOR_NOT_READY", "Collector process changed during startup")
	}
	return collectorDockerReady(current)
}
