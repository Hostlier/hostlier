// Package runtime implements the owned Docker workloads and private Caddy edge.
package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/dockerlogin"
	"hostlier/internal/model"
)

// Options configures private runtime transport, ownership paths and readiness limits.
type Options struct {
	// DockerLogin is the read-only server-account login source selected during installation.
	DockerLogin *dockerlogin.Source
	// CollectorTrustDirectory optionally relocates the local Docker registry trust root for embedding and tests.
	CollectorTrustDirectory                                                                       string
	KubeBinary, Kubeconfig                                                                        string
	KubeRun                                                                                       func(context.Context, []string, []byte) ([]byte, error)
	Type, StateDir, SocketPath, CaddyAdmin, ControllerUpstream, Hostname, ProbeHost, UpstreamHost string
	Proxy, NginxDirectory                                                                         string
	SkipPublicHTTPSProbe                                                                          bool
	HealthTimeout                                                                                 time.Duration
	RegistryCredentials                                                                           map[string]RegistryCredential
}

// Runtime owns Docker transport, private edge access and serialized application mutations.
type Runtime struct {
	idle          *idleGateway
	edgeMu        sync.Mutex
	usageMu       sync.Mutex
	usageSamples  map[string]dockerUsageSample
	stateFilename string
	options       Options
	docker, web   *http.Client
	mu            sync.Mutex
	collectorMu   sync.Mutex
	owner         string
}

// appRecord persists the active workload specification and the metadata needed for rollback.
type appRecord struct {
	IdleSleeping      bool               `json:"idleSleeping,omitempty"`
	EndpointUpstreams map[string]string  `json:"endpointUpstreams,omitempty"`
	Autoscaling       *autoscalingRecord `json:"autoscaling,omitempty"`
	PreviousGroup     *retiredGroup      `json:"previousGroup,omitempty"`
	Anchor            string             `json:"anchor,omitempty"`
	Members           map[string]string  `json:"members,omitempty"`
	// PreviousRuntimeID durably identifies source capacity awaiting graceful drain after cutover.
	PreviousRuntimeID  string   `json:"previousRuntimeId,omitempty"`
	DependencyNetworks []string `json:"dependencyNetworks,omitempty"`
	model.Deployment
	Spec                 model.Project `json:"spec"`
	Upstream             string        `json:"upstream"`
	Volumes              []string      `json:"volumes"`
	OperationID          string        `json:"operationId"`
	StorageQuotaEnforced bool          `json:"storageQuotaEnforced"`
	// PullSecret records that the k3s namespace holds this app's registry pull secret.
	PullSecret bool `json:"pullSecret,omitempty"`
}

// state separates live application records from data retained after deletion.
type state struct {
	Apps     map[string]appRecord `json:"apps"`
	Retained map[string]appRecord `json:"retained"`
}

// dockerContainer decodes only runtime fields needed for ownership, readiness and private ports.
type dockerContainer struct {
	ID     string `json:"Id"`
	Config struct{ Labels map[string]string }
	State  struct {
		Running   bool
		OOMKilled bool
		ExitCode  int
		Health    struct{ Status string }
	}
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string
		}
	}
}

var digestPattern = regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)

// shortHash derives stable bounded names without exposing full server paths or operation IDs.
func shortHash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:])[:16] }

// New constructs the selected runtime and validates private administration settings.
func New(o Options) (model.Runtime, error) {
	// Validate all constructor ownership and edge preconditions together.
	if err := validateRuntimeOptions(o); err != nil {
		return nil, err
	}

	// Use Docker unless the caller explicitly selected another supported backend.
	if o.Type == "" {
		o.Type = "docker"
	}
	// Construct the Kubernetes adapter through its own backend initialization.
	if o.Type == "k3s" {
		return newK3s(o)
	}
	// Reject unsupported runtime names before creating transports.
	if o.Type != "docker" {
		return nil, model.Err("UNSUPPORTED_RUNTIME", "Supported runtimes are Docker and k3s")
	}
	o = dockerDefaults(o)
	o.CaddyAdmin = strings.TrimRight(o.CaddyAdmin, "/")
	admin, e := url.Parse(o.CaddyAdmin)
	// Require Caddy administration to stay on unencrypted local loopback HTTP.
	if e != nil || admin.Scheme != "http" || !(admin.Hostname() == "localhost" || net.ParseIP(admin.Hostname()) != nil && net.ParseIP(admin.Hostname()).IsLoopback()) {
		return nil, model.Err("CADDY_PRIVATE", "Caddy administration must use private loopback HTTP")
	}
	// Connect Docker requests through the configured local Unix socket.
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", o.SocketPath)
	}}
	return &Runtime{options: o, owner: shortHash(o.StateDir), docker: &http.Client{Transport: transport, Timeout: 3 * time.Minute}, web: &http.Client{Timeout: 15 * time.Second}}, nil
}

// Name reports the stable Docker backend identifier.
func (r *Runtime) Name() string { return "docker" }

// labels binds created objects to this server and application ownership scope.
func (r *Runtime) labels(app string) map[string]string {
	return map[string]string{"io.hostlier.managed": "true", "io.hostlier.owner": r.owner, "io.hostlier.app": app}
}

// owned verifies managed status, server identity and optional application ownership.
func (r *Runtime) owned(labels map[string]string, app string) bool {
	return labels["io.hostlier.managed"] == "true" && labels["io.hostlier.owner"] == r.owner && (app == "" || labels["io.hostlier.app"] == app)
}

// request sends bounded JSON HTTP requests while preserving structured runtime status codes.
func request(ctx context.Context, client *http.Client, method, target string, body any, headers map[string]string) ([]byte, error) {
	var reader io.Reader
	// Marshal only requests that carry a JSON body.
	if body != nil {
		data, e := json.Marshal(body)
		// Do not send a partially encoded request body.
		if e != nil {
			return nil, e
		}
		reader = bytes.NewReader(data)
	}
	req, e := http.NewRequestWithContext(ctx, method, target, reader)
	// Preserve invalid-request errors before transport execution.
	if e != nil {
		return nil, model.Err("RUNTIME_REQUEST", "Invalid runtime request")
	}
	// Advertise JSON only when a request body was supplied.
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Attach only the explicitly supplied per-request headers.
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, e := client.Do(req)
	// Normalize transport failures without exposing daemon or registry details.
	if e != nil {
		return nil, model.Err("RUNTIME_CONNECTION", "Cannot connect to runtime endpoint")
	}
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, 32*1024*1024+1))
	// Reject interrupted or oversized responses before decoding them.
	if e != nil || len(data) > 32*1024*1024 {
		return nil, model.Err("RUNTIME_RESPONSE", "Runtime response exceeded its limit")
	}
	// Preserve HTTP failure status without returning raw provider response bodies.
	if res.StatusCode >= 400 {
		return nil, model.Err(fmt.Sprintf("HTTP_%d", res.StatusCode), fmt.Sprintf("Runtime request failed (%d)", res.StatusCode))
	}
	return data, nil
}

// api wraps Docker JSON or raw responses using the runtime's private transport.
func (r *Runtime) api(ctx context.Context, method, path string, body any, out any, headers map[string]string) error {
	data, e := request(ctx, r.docker, method, "http://docker"+path, body, headers)
	// Do not decode failed transport or HTTP responses.
	if e != nil {
		return e
	}
	// Skip decoding for operations that expect no result body.
	if out != nil {
		// Raw byte outputs retain streamed Docker progress and log frames.
		if raw, ok := out.(*[]byte); ok {
			*raw = data
		} else if len(data) > 0 { // Decode structured output only when a response body exists.
			// Normalize malformed Docker JSON instead of returning parser detail.
			if json.Unmarshal(data, out) != nil {
				return model.Err("RUNTIME_RESPONSE", "Invalid Docker response")
			}
		}
	}
	return nil
}

// hasCode matches stable runtime error codes without string inspection.
func hasCode(e error, code string) bool { v, ok := e.(*model.Error); return ok && v.Code == code }

// read loads durable runtime metadata and initializes missing collections.
func (r *Runtime) read() (state, error) {
	s := state{Apps: map[string]appRecord{}, Retained: map[string]appRecord{}}
	data, e := os.ReadFile(filepath.Join(r.options.StateDir, r.stateFile()))
	// A fresh installation starts with empty live and retained application maps.
	if os.IsNotExist(e) {
		return s, nil
	}
	// Preserve unexpected read failures instead of silently losing ownership metadata.
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(data, &s)
	// Initialize legacy or empty live application collections.
	if s.Apps == nil {
		s.Apps = map[string]appRecord{}
	}
	// Initialize retained-data tracking when absent from older state.
	if s.Retained == nil {
		s.Retained = map[string]appRecord{}
	}
	return s, e
}

// atomicWrite durably replaces protected runtime metadata through a staging file.
func atomicWrite(path string, data []byte) error {
	// Create the protected parent before opening the staging record.
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	// Do not write metadata when its staging file cannot be opened.
	if e != nil {
		return e
	}
	// Sync only a completely written record.
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	// A failed write or sync must leave the previous metadata intact.
	if e != nil {
		return e
	}
	// Do not activate a record whose file close failed.
	if ce != nil {
		return ce
	}
	// Atomically publish the completed runtime record.
	if e = os.Rename(path+".tmp", path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	// Require parent-directory access before confirming rename durability.
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// save serializes current runtime metadata and commits it atomically.
func (r *Runtime) save(s state) error {
	data, e := json.Marshal(s)
	// Do not replace durable state with unencodable metadata.
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(r.options.StateDir, r.stateFile()), data)
}

// container inspects a workload and verifies ownership before any later mutation.
func (r *Runtime) container(ctx context.Context, id, app string) (dockerContainer, error) {
	var result dockerContainer
	e := r.api(ctx, "GET", "/containers/"+url.PathEscape(id)+"/json", nil, &result, nil)
	// Reject a successful lookup whose server or application ownership differs.
	if e == nil && !r.owned(result.Config.Labels, app) {
		e = model.Err("OWNERSHIP", "Refusing to modify an unowned container")
	}
	return result, e
}

// ensureNetwork creates or verifies a private server-owned application network.
func (r *Runtime) ensureNetwork(ctx context.Context, app string) (string, error) {
	name := "hostlier-" + r.owner + "-" + app
	var network struct{ Labels map[string]string }
	e := r.api(ctx, "GET", "/networks/"+name, nil, &network, nil)
	// Never reuse a network owned by another server or application.
	if e == nil && !r.owned(network.Labels, app) {
		return "", model.Err("OWNERSHIP", "Network belongs to another owner")
	}
	// Create a network only when Docker reports it absent.
	if hasCode(e, "HTTP_404") {
		e = r.api(ctx, "POST", "/networks/create", map[string]any{"Name": name, "Driver": "bridge", "Labels": r.labels(app)}, nil, nil)
	}
	return name, e
}

// ensureVolume creates or verifies an application-scoped persistent volume.
func (r *Runtime) ensureVolume(ctx context.Context, app, key string) (string, error) {
	name := "hostlier-" + r.owner + "-" + app + "-" + key
	var volume struct{ Labels map[string]string }
	e := r.api(ctx, "GET", "/volumes/"+name, nil, &volume, nil)
	// Never reuse a same-named volume without matching ownership labels.
	if e == nil && !r.owned(volume.Labels, app) {
		return "", model.Err("OWNERSHIP", "Volume belongs to another owner")
	}
	// Create a volume only when Docker reports it absent.
	if hasCode(e, "HTTP_404") {
		e = r.api(ctx, "POST", "/volumes/create", map[string]any{"Name": name, "Labels": r.labels(app)}, nil, nil)
	}
	return name, e
}

// ParseMemory retains the runtime API while sharing checked byte conversion with planning.
func ParseMemory(v string) (int64, error) { return config.ParseMemory(v) }

// pause waits without losing context cancellation.
func pause(ctx context.Context, d time.Duration) error {
	// Choose between the requested delay and cancellation.
	select {
	// Abort retries promptly when the caller cancels.
	case <-ctx.Done():
		return ctx.Err()
	// Resume only after the requested backoff interval.
	case <-time.After(d):
		return nil
	}
}

// healthy verifies the candidate keeps running and responds to its HTTP probe.
func (r *Runtime) healthy(ctx context.Context, id, port, path string, timeout time.Duration) error {
	// Use the default readiness window when no positive timeout was provided.
	if timeout <= 0 {
		timeout = r.options.HealthTimeout
	}
	deadline := time.Now().Add(timeout)
	// Bound all candidate inspection and HTTP probes by the readiness deadline.
	for time.Now().Before(deadline) {
		c, e := r.container(ctx, id, "")
		// Propagate candidate inspection failures before assuming it is healthy.
		if e != nil {
			return e
		}
		// A container that exits during readiness cannot receive traffic.
		if !c.State.Running {
			return containerExitError(c)
		}
		probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		req, _ := http.NewRequestWithContext(probeCtx, "GET", "http://"+net.JoinHostPort(r.options.ProbeHost, port)+path, nil)
		res, e := r.web.Do(req)
		ok := e == nil && res.StatusCode >= 200 && res.StatusCode < 300
		// Inspect HTTP status only for a completed private backend request.
		if e == nil {
			res.Body.Close()
		}
		cancel()
		// A response below the server-error range establishes HTTP readiness.
		if ok {
			return nil
		}
		// Respect cancellation during the readiness retry delay.
		if e = pause(ctx, 300*time.Millisecond); e != nil {
			return e
		}
	}
	return model.Err("HEALTH_TIMEOUT", "Application did not become HTTP ready")
}

// Deploy deploys one approved immutable workload with failure rollback.
func (r *Runtime) Deploy(ctx context.Context, spec model.Project, dc model.DeployContext) (result model.Deployment, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Reject unsupported replica layouts before any Docker mutation.
	if err = validateReplicas(spec, "docker"); err != nil {
		return result, err
	}
	// Scale-to-zero retains the workload record and all data.
	if model.DesiredReplicas(spec) == 0 {
		return r.deployZero(ctx, spec, dc)
	}
	d := &dockerDeployment{runtime: r, ctx: ctx, spec: spec, dc: dc}
	// Validate immutable inputs and ownership before installing rollback handlers or mutating runtime state.
	if err = d.prepare(); err != nil {
		return result, err
	}
	// Any later failure restores the previous writer and edge using a bounded cleanup context.
	defer func() {
		// Successful deployments retain their activated workload and durable state.
		if err != nil {
			d.rollback()
		}
	}()
	// Execute preparation, candidate creation, readiness and activation in transactional order.
	for _, stage := range []func() error{d.pull, d.prepareVolumes, d.prepareGroup, d.candidateContainer, d.startGroupMembers, d.start, d.readiness, d.activate, d.cleanup, d.completeTransition} {
		// Stop at the first failing stage so the deferred rollback sees its error.
		if err = stage(); err != nil {
			return d.result, err
		}
	}
	d.event("ready", "Application ready")
	return d.result, nil
}

// Doctor reports reachability of the local Docker and private Caddy services.
func (r *Runtime) Doctor(ctx context.Context) (any, error) {
	checks := []map[string]any{}
	e := r.api(ctx, "GET", "/_ping", nil, nil, nil)
	checks = append(checks, map[string]any{"name": "docker", "ok": e == nil})
	e = r.edgeHealth(ctx)
	checks = append(checks, map[string]any{"name": defaultProxyName(r.options.Proxy), "ok": e == nil})
	return map[string]any{"runtime": "docker", "checks": checks}, nil
}

// Docker may allocate a different ephemeral host port after a stop/start.
func (r *Runtime) backendPort(ctx context.Context, id string, appPort int) (string, error) {
	deadline := time.Now().Add(r.options.HealthTimeout)
	// Wait briefly for Docker to publish the persisted private backend port.
	for time.Now().Before(deadline) {
		container, e := r.container(ctx, id, "")
		// Ownership or inspection failures stop backend discovery.
		if e != nil {
			return "", e
		}
		ports := container.NetworkSettings.Ports[strconv.Itoa(appPort)+"/tcp"]
		// Accept only an allocated loopback binding on a running container.
		if container.State.Running && len(ports) > 0 && ports[0].HostIP == "127.0.0.1" && ports[0].HostPort != "" {
			return ports[0].HostPort, nil
		}
		// Honor cancellation while Docker finishes exposing its private binding.
		if e = pause(ctx, 100*time.Millisecond); e != nil {
			return "", e
		}
	}
	return "", model.Err("PRIVATE_PORT", "Docker did not allocate a private backend port")
}

// stateFile selects backend-specific metadata or the default Docker state path.
func (r *Runtime) stateFile() string {
	// An adapter may explicitly select a separate owned state filename.
	if r.stateFilename != "" {
		return r.stateFilename
	}
	return "runtime-docker.json"
}

// SupportsDependencies reports Docker's support for controller-resolved private resource networks.
func (r *Runtime) SupportsDependencies() bool { return true }

// dockerDefaults supplies local runtime paths, private endpoints and readiness bounds.
func dockerDefaults(o Options) Options {
	// Keep persistent runtime metadata under the standard protected controller directory.
	if o.StateDir == "" {
		o.StateDir = "/var/lib/hostlier"
	}
	// Use the system Docker Unix socket unless explicitly configured.
	if o.SocketPath == "" {
		o.SocketPath = "/var/run/docker.sock"
	}
	// Default Caddy administration to private loopback HTTP.
	if o.CaddyAdmin == "" {
		o.CaddyAdmin = "http://127.0.0.1:2019"
	}
	// Keep the controller backend separate from public edge listeners.
	if o.ControllerUpstream == "" {
		o.ControllerUpstream = "127.0.0.1:7463"
	}
	// Probe application health through loopback by default.
	if o.ProbeHost == "" {
		o.ProbeHost = "127.0.0.1"
	}
	// Publish loopback upstream addresses into the private edge by default.
	if o.UpstreamHost == "" {
		o.UpstreamHost = "127.0.0.1"
	}
	// Bound application readiness to one minute unless explicitly configured.
	if o.HealthTimeout == 0 {
		o.HealthTimeout = 60 * time.Second
	}
	return o
}
