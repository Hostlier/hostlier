package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"hostlier/internal/artifact"
	"hostlier/internal/model"
)

// dockerDeployment owns one transactional attempt while the runtime lock is held.
type dockerDeployment struct {
	anchor                string
	members               map[string]string
	connections           []string
	port                  string
	runtime               *Runtime
	ctx                   context.Context
	spec                  model.Project
	dc                    model.DeployContext
	s                     state
	old                   appRecord
	hasOld                bool
	route                 model.Route
	operation, name       string
	candidate             dockerContainer
	activated, oldStopped bool
	mounts                []any
	volumes               []string
	network, portKey      string
	result                model.Deployment
}

// event forwards safe progress without exposing runtime configuration or secrets.
func (d *dockerDeployment) event(step, message string) {
	// Emit progress only when the controller supplied an event consumer.
	if d.dc.OnEvent != nil {
		d.dc.OnEvent(model.Event{Level: "info", Step: step, Message: message})
	}
}

// prepare validates ownership, routing and approved immutable inputs before mutation.
func (d *dockerDeployment) prepare() error {
	r := d.runtime
	var e error

	// Reject unowned dependency networks before reading or changing application state.
	if err := d.validateDependencies(); err != nil {
		return err
	}
	d.s, e = r.read()
	// Do not deploy against unreadable durable runtime metadata.
	if e != nil {
		return e
	}
	d.old, d.hasOld = d.s.Apps[d.spec.Name]
	d.route, e = workloadRoute(d.spec, r.options.Hostname)
	// Require a normalized route outside the reserved controller hostname.
	if e != nil {
		return e
	}
	// Edge protocol capabilities are checked before stopping or creating any workload.
	if err := r.validateEdgeFeatures(d.ctx, d.spec); err != nil {
		return err
	}
	// Reject overlap with another application's route before pulling images.
	if err := r.validateEndpointRoutes(d.ctx, d.s, d.spec); err != nil {
		return err
	}
	// Accept only the digest approved by the controller plan.
	if !digestPattern.MatchString(d.spec.Image) {
		return model.Err("UNPINNED_IMAGE", "Deployment requires its approved immutable image digest")
	}
	d.operation = d.dc.OperationID
	// Require durable operation identity for idempotent candidate naming.
	if d.operation == "" {
		return model.Err("OPERATION_REQUIRED", "A durable operation ID is required")
	}
	d.name = "hostlier-" + r.owner + "-" + d.spec.Name + "-" + shortHash(d.operation)
	return d.prepareRecovery()
}

// rollback restores the previous route and volume writer after a failed attempt.
func (d *dockerDeployment) rollback() {
	d.journalFailedGroup()
	r := d.runtime

	cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer d.rollbackGroup(cleanup)
	// Candidate members must release the namespace before primary deletion.
	if d.candidate.ID != d.old.RuntimeID {
		d.removeCandidateMembers(cleanup)
	}

	// An unresolved candidate may still own the volume; leave the journal and source stopped.
	if !d.rollbackCandidate(cleanup) {
		return
	}
	d.restoreOldWriter(cleanup)
	// Repair edge state only when the attempt changed routing or stopped the old workload.
	if d.activated || d.oldStopped {
		// Restore the recorded application or remove the newly introduced record.
		if d.hasOld {
			d.s.Apps[d.spec.Name] = d.old
		} else {
			delete(d.s.Apps, d.spec.Name)
		}
		_ = r.caddy(cleanup, d.s)
		// Persist an updated upstream when the old container was restarted.
		if d.oldStopped {
			_ = r.save(d.s)
		}
	}
}

// pull pulls the approved digest and rejects per-layer Docker errors.
func (d *dockerDeployment) pull() error {
	// Prepared artifacts are imported without contacting an image registry.
	if artifact.IsImage(d.spec.Image) {
		return d.runtime.importArtifact(d.ctx, d.spec.Image)
	}
	r := d.runtime
	ctx := d.ctx
	var e error

	d.event("pull", "Pulling approved immutable image")
	headers := map[string]string{}
	// Attach registry credentials only when the image's registry has an approved credential entry.
	auth, ok, err := r.auth(ctx, d.spec.Image)
	// A broken or locked login must not silently turn into an anonymous pull.
	if err != nil {
		return err
	}
	// Include credentials only for the approved image registry.
	if ok {
		raw, _ := json.Marshal(auth)
		headers["X-Registry-Auth"] = base64.URLEncoding.EncodeToString(raw)
	}
	var pulled []byte
	// A failed pull prevents network or candidate creation.
	if e = r.api(ctx, "POST", "/images/create?platform=linux%2F"+goruntime.GOARCH+"&fromImage="+url.QueryEscape(d.spec.Image), nil, &pulled, headers); e != nil {
		return e
	}
	// Inspect streamed Docker progress for errors hidden inside a successful HTTP response.
	for _, line := range bytes.Split(pulled, []byte{'\n'}) {
		var p map[string]any
		// Treat any layer-level pull error as deployment failure.
		if json.Unmarshal(line, &p) == nil && p["error"] != nil {
			return model.Err("IMAGE_PULL", "Image pull failed; check registry and digest")
		}
	}
	return nil
}

// prepareVolumes prepares the owned application network and persistent mounts.
func (d *dockerDeployment) prepareVolumes() error {
	r := d.runtime
	ctx := d.ctx
	var e error

	d.network, e = r.ensureNetwork(ctx, d.spec.Name)
	// Require the owned application network before creating volume mounts.
	if e != nil {
		return e
	}
	d.mounts = []any{}
	d.volumes = []string{}
	// Create or verify each named owned persistent volume.
	for key, v := range d.spec.Volumes {
		volumeName, volumeErr := r.ensureVolume(ctx, d.spec.Name, key)
		e = volumeErr
		// Do not construct mounts for an unverified or unavailable volume.
		if e != nil {
			return e
		}
		d.volumes = append(d.volumes, volumeName)
		d.mounts = append(d.mounts, map[string]any{"Type": "volume", "Source": volumeName, "Target": v.Mount})
	}
	return nil
}

// candidateContainer reuses or creates the operation-scoped workload with private bindings.
func (d *dockerDeployment) candidateContainer() error {
	// Version two uses a stable network namespace and independent member containers.
	if d.spec.Version == 2 {
		return d.candidateV2()
	}
	return d.candidateLegacy()
}

// candidateLegacy preserves the v1 single-container creation contract.
func (d *dockerDeployment) candidateLegacy() error {
	r := d.runtime
	ctx := d.ctx
	var e error

	d.portKey = strconv.Itoa(d.spec.Port) + "/tcp"
	d.candidate, e = r.container(ctx, d.name, d.spec.Name)
	// Create a candidate only when the operation-specific container does not already exist.
	if hasCode(e, "HTTP_404") {
		env := map[string]string{}
		// Copy literal environment values before adding resolved secret bindings.
		for k, v := range d.spec.Env {
			env[k] = v
		}
		// Resolved secret values override their approved environment destinations.
		for k, v := range d.dc.Secrets {
			env[k] = v
		}
		envList := []string{}
		// Encode the merged environment in deterministic order.
		for k, v := range env {
			envList = append(envList, k+"="+v)
		}
		sort.Strings(envList)
		endpoints := map[string]any{d.network: map[string]any{}}
		// Attach only the dependency networks already verified during preparation.
		for _, n := range d.dc.DependencyNetworks {
			endpoints[n] = map[string]any{}
		}
		// Persist a concrete private port in Docker HostConfig so daemon/server
		// restarts retain the same Caddy upstream, even without the controller.
		listener, listenErr := net.Listen("tcp4", "127.0.0.1:0")
		// A private backend requires an available loopback port reservation.
		if listenErr != nil {
			return model.Err("PRIVATE_PORT", "Cannot reserve a private backend port")
		}
		fixedPort := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
		listener.Close()
		host := map[string]any{"NetworkMode": d.network, "RestartPolicy": map[string]string{"Name": "unless-stopped"}, "PortBindings": map[string]any{d.portKey: []any{map[string]string{"HostIp": "127.0.0.1", "HostPort": fixedPort}}}, "Mounts": d.mounts, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "10m", "max-file": "3"}}}
		// Require representable runtime limits before creating a workload.
		if err := applyContainerLimits(host, d.spec.Resources); err != nil {
			return err
		}
		l := r.labels(d.spec.Name)
		l["io.hostlier.operation"] = d.operation
		var created struct{ ID string }
		e = r.api(ctx, "POST", "/containers/create?platform=linux%2F"+goruntime.GOARCH+"&name="+d.name, map[string]any{"Image": d.spec.Image, "Labels": l, "Env": envList, "ExposedPorts": map[string]any{d.portKey: map[string]any{}}, "NetworkingConfig": map[string]any{"EndpointsConfig": endpoints}, "HostConfig": host}, &created, nil)
		// Do not inspect or start a candidate whose creation failed.
		if e != nil {
			return e
		}
		d.candidate, e = r.container(ctx, created.ID, d.spec.Name)
	}
	// Propagate existing-container inspection or newly created-container lookup failures.
	if e != nil {
		return e
	}
	return nil
}

// start transfers exclusive volume writing to the candidate and starts it.
func (d *dockerDeployment) start() error {
	r := d.runtime
	ctx := d.ctx
	var e error

	// A persistent volume has one writer, so stop the old version before starting the candidate.
	if !d.oldStopped && d.old.RuntimeID != "" && d.old.RuntimeID != d.candidate.ID && (len(d.mounts) > 0 || groupHasVolumes(d.spec)) {
		// Recovered hosts may have no previous container; all live writers retain ownership checks.
		if e = d.stopPreviousWriter(); e != nil {
			return e
		}
	}
	// Reuse a running idempotent candidate instead of restarting it.
	if !d.candidate.State.Running {
		// A failed start triggers candidate cleanup and old-writer rollback.
		if e = r.api(ctx, "POST", "/containers/"+d.candidate.ID+"/start", nil, nil, nil); e != nil {
			return e
		}
	}

	return nil
}

// readiness discovers the private backend and verifies HTTP health.
func (d *dockerDeployment) readiness() error {
	// Group probes run inside the namespace, keeping private ports unpublished.
	if d.spec.Version == 2 {
		return d.groupReadiness()
	}
	r := d.runtime
	ctx := d.ctx
	var e error

	port, e := r.backendPort(ctx, d.candidate.ID, d.spec.Port)
	d.port = port
	// Require Docker to publish the owned loopback backend before probing readiness.
	if e != nil {
		return e
	}
	path := healthPath(d.spec)
	d.event("health", "Waiting for application HTTP readiness")
	healthTimeout := r.options.HealthTimeout
	// Honor the approved project timeout when it overrides the runtime default.
	if d.spec.Health != nil && d.spec.Health.TimeoutSeconds > 0 {
		healthTimeout = time.Duration(d.spec.Health.TimeoutSeconds) * time.Second
	}
	// Do not expose a candidate until its HTTP readiness probe succeeds.
	if e = r.healthy(ctx, d.candidate.ID, port, path, healthTimeout); e != nil {
		return e
	}
	return nil
}

// activate publishes the candidate route, verifies HTTPS and commits runtime state.
func (d *dockerDeployment) activate() error {
	r := d.runtime
	ctx := d.ctx
	var e error

	// Stable private discovery switches only after all required probes pass.
	if err := d.privateAlias(); err != nil {
		return err
	}
	upstreams := map[string]string{}
	// Resolve all named public mappings before changing any active edge route.
	if len(d.spec.Endpoints) > 0 {
		var err error
		upstreams, err = d.endpointUpstreams()
		// An incomplete mapping table cannot become the active edge revision.
		if err != nil {
			return err
		}
	}
	d.result = model.Deployment{Endpoints: endpointURLs(d.spec), Name: d.spec.Name, URL: d.route.URL, RuntimeID: d.candidate.ID, ImageDigest: d.spec.Image, Status: "running"}
	d.s.Apps[d.spec.Name] = appRecord{EndpointUpstreams: upstreams, Anchor: d.anchor, Members: d.members, PreviousRuntimeID: d.previousID(), PreviousGroup: d.sourceGroup(), Deployment: d.result, Spec: d.spec, Upstream: net.JoinHostPort(r.options.UpstreamHost, d.port), Volumes: d.volumes, OperationID: d.operation}
	d.event("route", "Activating Caddy route")
	// A failed edge activation leaves rollback responsible for the previous route.
	if e = r.caddy(ctx, d.s); e != nil {
		return e
	}
	d.activated = true
	d.event("certificate", "Verifying HTTPS readiness")
	// Require verified public HTTPS readiness before committing the deployment.
	if e = r.publicEndpointsReady(ctx, d.spec); e != nil {
		return e
	}
	// Preserve rollback when durable runtime metadata cannot be saved.
	if e = r.save(d.s); e != nil {
		return e
	}
	return nil
}

// cleanup removes the superseded owned container after successful activation.
func (d *dockerDeployment) cleanup() error {
	r := d.runtime
	previous := d.previousID()
	// A saved transition retains the source identity until drain succeeds.
	if previous == "" || previous == d.candidate.ID {
		return nil
	}
	// Report the durable cutover boundary before retiring source capacity.
	d.event("drain", "Application activated; draining previous capacity")
	// Recheck source ownership before graceful shutdown.
	if _, err := r.container(d.ctx, previous, d.spec.Name); err != nil {
		return nil
	}
	// Leave the durable pending-drain marker intact on failure for an idempotent retry.
	if err := r.api(d.ctx, "POST", "/containers/"+previous+"/stop?t="+strconv.Itoa(model.GraceSeconds(d.old.Spec)), nil, nil, nil); err != nil {
		d.event("cleanup", "Application activated; previous owned container drain requires retry")
		return nil
	}
	// Sidecars drain after the primary, retaining their namespace for later rollback.
	if err := d.stopSourceGroup(d.ctx); err != nil {
		return nil
	}
	app := d.s.Apps[d.spec.Name]
	app.PreviousRuntimeID = ""
	app.PreviousGroup = nil
	d.s.Apps[d.spec.Name] = app
	// Saving drain completion is best effort: repeating stop is safe.
	_ = r.save(d.s)
	return nil
}

// previousID preserves the pending source identity when replaying an accepted operation.
func (d *dockerDeployment) previousID() string {
	// Replaying an already committed candidate must still drain its recorded source.
	if d.old.OperationID == d.operation {
		return d.old.PreviousRuntimeID
	}
	return d.old.RuntimeID
}

// validateDependencies verifies every resource network belongs to this server.
func (d *dockerDeployment) validateDependencies() error {
	r := d.runtime
	ctx := d.ctx

	// Require controller-resolved network bindings whenever dependencies are declared.
	if len(d.spec.Dependencies) > 0 && d.dc.DependencyNetworks == nil {
		return model.Err("UNSUPPORTED_DEPENDENCIES", "Dependencies require controller-resolved resource networks")
	}
	// Verify every declared resource network independently.
	for _, network := range d.dc.DependencyNetworks {
		var detail struct{ Labels map[string]string }
		// Reject network names outside this server's resource ownership namespace.
		if !strings.HasPrefix(network, "hostlier-"+r.owner+"-resource-") {
			return model.Err("OWNERSHIP", "Dependency network belongs to another owner")
		}
		// Do not trust a dependency network that Docker cannot inspect.
		if e := r.api(ctx, "GET", "/networks/"+url.PathEscape(network), nil, &detail, nil); e != nil {
			return e
		}
		// Require both server ownership and the resource role label.
		if !r.owned(detail.Labels, "") || detail.Labels["io.hostlier.resource"] == "" {
			return model.Err("OWNERSHIP", "Dependency network ownership mismatch")
		}
	}
	return nil
}

// applyContainerLimits maps validated CPU and memory limits to Docker host settings.
func applyContainerLimits(host map[string]any, limits *model.Limits) error {
	// An absent limits block preserves Docker defaults.
	if limits != nil {
		// Convert an explicit CPU limit to Docker's nanocore unit.
		if limits.CPU != "" {
			cpu, _ := strconv.ParseFloat(limits.CPU, 64)
			host["NanoCpus"] = int64(cpu * 1e9)
		}
		// Apply memory only when an explicit bound was approved.
		if limits.Memory != "" {
			memory, e := ParseMemory(limits.Memory)
			// Reject overflow before sending a memory limit to Docker.
			if e != nil {
				return e
			}
			host["Memory"] = memory
		}
	}
	return nil
}

// rollbackGroup repairs sidecar membership and private discovery on failed attempts.
func (d *dockerDeployment) rollbackGroup(ctx context.Context) {
	r := d.runtime
	cleanup := ctx
	// A separate budget and durable queue cover anchors even when primary creation failed.
	d.cleanupFailedGroup()
	// Restore the old private alias after a failed candidate transition.
	if d.old.Anchor != "" && d.old.Spec.Endpoint != nil && d.old.Spec.Endpoint.Exposure == "private" {
		_ = r.aliasGroupNetwork(cleanup, d.old.Anchor, r.applicationNetwork(d.old.Name), d.old.Name)
	}
}

// restoreOldWriter restores all members before republishing a retained volume owner.
func (d *dockerDeployment) restoreOldWriter(ctx context.Context) {
	// Rollback must not start a previously stopped application.
	if !d.oldStopped {
		return
	}
	r := d.runtime
	_ = r.groupContainerAction(ctx, d.old.Name, d.old.RuntimeID, "start", 20)
	_ = r.memberAction(ctx, d.old, "start")
	id := d.old.RuntimeID
	// Group host bindings belong to the stable namespace holder.
	if d.old.Anchor != "" {
		id = d.old.Anchor
	}
	// Private groups have no host publication to refresh.
	if !model.Public(d.old.Spec) {
		return
	}
	port, err := r.backendPort(ctx, id, d.old.Spec.Port)
	// Preserve the retained upstream when Docker cannot establish a new one.
	if err == nil {
		d.old.Upstream = net.JoinHostPort(r.options.UpstreamHost, port)
	}
}
