// Package resources provisions private, owned built-in database instances.
package resources

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

const MaxBackupBytes = 64 << 20

var Images = map[string]string{
	"postgres": "postgres@sha256:23e88eb049fd5d54894d70100df61d38a49ed97909263f79d4ff4c30a5d5fca2",
	"redis":    "redis@sha256:02419de7eddf55aa5bcf49efb74e88fa8d931b4d77c07eff8a6b2144472b6952",
	"neo4j":    "neo4j@sha256:5878e533971f2fee1ce27cf51eb173cfd7d833c4038089f128a315bcc4b3414e",
}

// Options selects the backend, private transport and bounded health behavior.
type Options struct {
	OnDiagnostic                                       func(string)
	Type, StateDir, SocketPath, K3sCommand, Kubeconfig string
	KubeRun                                            func(context.Context, []string, []byte) ([]byte, error)
	HealthTimeout                                      time.Duration
}

// Manager owns Docker resource state and serializes lifecycle operations.
type Manager struct {
	options Options
	http    *http.Client
	owner   string
	mu      sync.Mutex
}

// record persists a resource together with its owned data and engine specification.
type record struct {
	RecoveryFence string `json:"recoveryFence,omitempty"`
	model.Resource
	Spec                 model.ResourceSpec `json:"spec"`
	Volume               string             `json:"volume"`
	StorageQuotaEnforced bool               `json:"storageQuotaEnforced"`
}

// container decodes the Docker fields needed for ownership and lifecycle checks.
type container struct {
	ID         string         `json:"Id"`
	HostConfig map[string]any `json:"HostConfig"`
	Config     struct {
		Image  string
		Labels map[string]string
	}
	State struct {
		Running  bool
		ExitCode int
	}
	NetworkSettings struct {
		Ports    map[string]any
		Networks map[string]any
	}
}

var resourceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var storageSize = regexp.MustCompile(`^[1-9][0-9]*(Mi|Gi|Ti)$`)

// New selects and configures a supported resource backend.
func New(options Options) (model.ResourceManager, error) {
	// Restored staging metadata cannot silently change existing resource ownership namespaces.
	if err := model.ValidateRecoveryLocation(options.StateDir); err != nil {
		return nil, err
	}
	// Choose Docker as the default resource backend.
	if options.Type == "" {
		options.Type = "docker"
	}

	// Resolve the private Hostlier state directory when none is provided.
	if options.StateDir == "" {
		options.StateDir = "/var/lib/hostlier"
	}
	// Apply the default bound for authenticated resource readiness.
	if options.HealthTimeout == 0 {
		options.HealthTimeout = 3 * time.Minute
	}
	// Route Kubernetes resource requests to their dedicated manager.
	if options.Type == "k3s" {
		return newK3s(options)
	}
	// Reject this request: Unsupported resource backend.
	if options.Type != "docker" {
		return nil, model.Err("UNSUPPORTED_RESOURCES", "Unsupported resource backend")
	}
	// Use the default private Docker Unix socket.
	if options.SocketPath == "" {
		options.SocketPath = "/var/run/docker.sock"
	}
	// Apply the default bound for authenticated resource readiness.
	if options.HealthTimeout == 0 {
		options.HealthTimeout = 3 * time.Minute
	}
	sum := sha256.Sum256([]byte(options.StateDir))
	// Send Docker requests only through the explicitly selected private Unix socket.
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", options.SocketPath)
	}}
	return &Manager{options: options, owner: hex.EncodeToString(sum[:])[:16], http: &http.Client{Transport: transport, Timeout: 4 * time.Minute}}, nil
}

// Name identifies the resource backend.
func (m *Manager) Name() string { return "docker" }

// network derives an owner-scoped private network name.
func (m *Manager) network(name string) string { return "hostlier-" + m.owner + "-resource-" + name }

// container uses the resource network identity for its engine container.
func (m *Manager) container(name string) string { return m.network(name) }

// volume derives the persistent data volume name for the selected resource.
func (m *Manager) volume(name string) string { return m.network(name) + "-data" }

// labels identifies the owner and resource on every managed object.
func (m *Manager) labels(name string) map[string]string {
	return map[string]string{"io.hostlier.managed": "true", "io.hostlier.owner": m.owner, "io.hostlier.resource": name}
}

// owned verifies that object labels match this manager and resource.
func (m *Manager) owned(labels map[string]string, name string) bool {
	return labels["io.hostlier.managed"] == "true" && labels["io.hostlier.owner"] == m.owner && labels["io.hostlier.resource"] == name
}

// normalize validates resource identity, storage and immutable engine image.
func normalize(spec model.ResourceSpec) (model.ResourceSpec, error) {
	// Reject this request: Resource requires a valid name and postgres, redis, or neo4j type.
	if !resourceName.MatchString(spec.Name) || Images[spec.Type] == "" {
		return spec, model.Err("RESOURCE_SPEC", "Resource requires a valid name and postgres, redis, or neo4j type")
	}
	// Reject this request: Storage must be a positive Mi, Gi, or Ti capacity.
	if !storageSize.MatchString(spec.Storage) || !config.ValidSize(spec.Storage) {
		return spec, model.Err("RESOURCE_STORAGE", "Storage must be a positive Mi, Gi, or Ti capacity")
	}
	// Reject this request: Only the pinned built-in engine version is supported; major upgrades require a separate workflow.
	if !supportedDatabaseImage(spec) {
		return spec, model.Err("RESOURCE_VERSION", "Only the pinned built-in engine version is supported; major upgrades require a separate workflow")
	}
	// Advance only after the required recovery invariant holds.
	if spec.Image == "" {
		spec.Image = Images[spec.Type]
	}
	// Explicit allocations use the same bounded quantities as allocation maintenance.
	if spec.CPU != "" || spec.Memory != "" {
		// Reject invalid desired allocation before any container or pod creation.
		if err := resizeSpec(spec, spec); err != nil {
			return spec, err
		}
	}
	return spec, nil
}

// api sends a bounded Docker API request over the configured Unix socket.
func (m *Manager) api(ctx context.Context, method, path string, body any, result any) ([]byte, error) {
	var payload []byte
	contentType := "application/json"
	var err error
	// Send native binary payloads without JSON encoding.
	if data, ok := body.([]byte); ok {
		payload = data
		contentType = "application/x-tar"
	} else if body != nil { // Encode structured Docker request bodies as JSON.
		payload, err = json.Marshal(body)
		// Require structured request encoding before sending data to Docker.
		if err != nil {
			return nil, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, bytes.NewReader(payload))
	// Require a cancellable Docker request before sending it.
	if err != nil {
		return nil, err
	}
	// Attach the encoded payload to the Docker HTTP request.
	if payload != nil {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := m.http.Do(request)
	// Reject this request: Cannot reach Docker resource runtime.
	if err != nil {
		return nil, model.Err("RESOURCE_RUNTIME", "Cannot reach Docker resource runtime")
	}
	defer response.Body.Close()
	return decodeResourceResponse(response, result)
}

// read loads resource records from the private state file.
func (m *Manager) read() (map[string]record, error) {
	data, err := os.ReadFile(filepath.Join(m.options.StateDir, "resources-docker.json"))
	// Treat an uncreated resource state file as empty records.
	if os.IsNotExist(err) {
		return map[string]record{}, nil
	}
	// Require readable resource metadata before decoding the persisted state.
	if err != nil {
		return nil, err
	}
	var state map[string]record
	// Reject this request: Cannot decode resource metadata.
	if json.Unmarshal(data, &state) != nil {
		return nil, model.Err("RESOURCE_STATE", "Cannot decode resource metadata")
	}
	return state, nil
}

// save atomically persists resource records and syncs their parent directory.
func (m *Manager) save(state map[string]record) error {
	// Create the private resource state directory before saving records.
	if err := os.MkdirAll(m.options.StateDir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	// Propagate the preceding persistence failure before advancing the atomic save.
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(m.options.StateDir, ".resources-*")
	// Propagate the preceding persistence failure before advancing the atomic save.
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	file.Chmod(0600)
	_, err = file.Write(data)
	// Sync metadata only after its complete byte payload has been written.
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	// Propagate the preceding persistence failure before advancing the atomic save.
	if err != nil {
		return err
	}
	// Do not publish the replacement state file when closing its descriptor failed.
	if closeErr != nil {
		return closeErr
	}
	// Require atomic state replacement before reporting persistence complete.
	if err = os.Rename(file.Name(), filepath.Join(m.options.StateDir, "resources-docker.json")); err != nil {
		return err
	}
	return syncDirectory(m.options.StateDir)
}

// find looks up an existing resource by its logical name.
func (m *Manager) find(name string) (record, error) {
	state, err := m.read()
	// Require valid persisted resource state before looking up or changing records.
	if err != nil {
		return record{}, err
	}
	item, ok := state[name]
	// Reject this request: Resource not found.
	if !ok {
		return record{}, model.Err("RESOURCE_NOT_FOUND", "Resource not found")
	}
	return item, nil
}

// inspect reads and verifies the selected engine container ownership.
func (m *Manager) inspect(ctx context.Context, name string) (container, error) {
	var value container
	_, err := m.api(ctx, "GET", "/containers/"+url.PathEscape(m.container(name))+"/json", nil, &value)
	// Require the owned Docker API request to complete before using its result.
	if err != nil {
		return value, err
	}
	// Reject this request: Refusing to modify unowned resource container.
	if !m.owned(value.Config.Labels, name) {
		return value, model.Err("RESOURCE_OWNERSHIP", "Refusing to modify unowned resource container")
	}
	return value, nil
}

// code compares a structured error code without matching error text.
func code(err error, value string) bool {
	var failure *model.Error
	return errors.As(err, &failure) && failure.Code == value
}

// ensure creates a network or volume only when no conflicting owned object exists.
func (m *Manager) ensure(ctx context.Context, kind, name, object string, body any) error {
	var value struct {
		Labels   map[string]string
		Internal bool
	}
	_, err := m.api(ctx, "GET", "/"+kind+"/"+url.PathEscape(object), nil, &value)
	// Validate ownership and network isolation before reusing an existing Docker object.
	if err == nil {
		// Reject this request: Resource name belongs to another owner.
		if !m.owned(value.Labels, name) {
			return model.Err("RESOURCE_OWNERSHIP", "Resource name belongs to another owner")
		}
		// Reject this request: Resource network must be internal.
		if kind == "networks" && !value.Internal {
			return model.Err("RESOURCE_NETWORK", "Resource network must be internal")
		}
		return nil
	}
	// Propagate lookup failures other than an absent Docker object.
	if !code(err, "HTTP_404") {
		return err
	}
	_, err = m.api(ctx, "POST", "/"+kind+"/create", body, nil)
	return err
}

// Plan normalizes a resource request and reports changes without provisioning.
func (m *Manager) Plan(ctx context.Context, input model.ResourceSpec) (model.ResourcePlan, error) {
	spec, err := normalize(input)
	// Reject invalid resource specifications before creating engine objects.
	if err != nil {
		return model.ResourcePlan{}, err
	}
	state, err := m.read()
	// Require valid persisted resource state before looking up or changing records.
	if err != nil {
		return model.ResourcePlan{}, err
	}
	action := "Create"
	// Compare retained settings before deciding whether this resource can be reused.
	if prior, ok := state[spec.Name]; ok {
		// Reject this request: Retained resource settings differ; type, version, or storage changes are unsupported.
		if prior.Spec != spec {
			return model.ResourcePlan{}, model.Err("RESOURCE_EXISTS", "Retained resource settings differ; type, version, or storage changes are unsupported")
		}
		action = "Reuse"
	}
	changes := []string{action + " private " + spec.Type + " instance " + spec.Name, "Image " + spec.Image, "Requested storage " + spec.Storage + "; Docker volume quotas are not enforced", "Data retained on ordinary deletion; purge is explicitly destructive", "No database ports published on host; only declared dependent application networks can connect"}
	// Include the Redis AOF durability window in the resource plan.
	if spec.Type == "redis" {
		changes = append(changes, "Redis AOF with appendfsync everysec; sudden host failure may lose up to one second of acknowledged writes")
	}
	// Include the Neo4j Community offline dump and load requirement in the plan.
	if spec.Type == "neo4j" {
		changes = append(changes, "Neo4j Community logical dump/load requires an offline maintenance window")
	}
	return model.ResourcePlan{Spec: spec, Changes: changes}, nil
}

// validateCredentials rejects credentials that violate the engine authentication contract.
func validateCredentials(kind string, c model.ResourceCredentials) error {
	expected := "hostlier"
	// Require the Neo4j image authentication username for this engine.
	if kind == "neo4j" {
		expected = "neo4j"
	}
	// Reject this request: Protected generated resource credentials required.
	if c.Username != expected || c.Database != "app" || !regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`).MatchString(c.Password) {
		return model.Err("RESOURCE_CREDENTIALS", "Protected generated resource credentials required")
	}
	return nil
}

// authEnv supplies engine credentials through environment entries rather than arguments.
func authEnv(kind string, c model.ResourceCredentials) []string {
	// Apply PostgreSQL-specific authentication and configuration.
	if kind == "postgres" {
		return []string{"PGPASSWORD=" + c.Password}
	}
	// Supply the Redis password through its supported authentication environment variable.
	if kind == "redis" {
		return []string{"REDISCLI_AUTH=" + c.Password}
	}
	return []string{"NEO4J_PASSWORD=" + c.Password}
}

// demultiplex separates Docker stdout and stderr while preserving binary snapshots.
func demultiplex(data []byte) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	offset := 0
	// Decode every complete Docker frame while preserving its payload bytes.
	for offset+8 <= len(data) && data[offset] <= 2 && data[offset+1] == 0 {
		size := int(binary.BigEndian.Uint32(data[offset+4 : offset+8]))
		// Reject this request: Truncated engine output.
		if size > len(data)-offset-8 {
			return nil, nil, model.Err("RESOURCE_OUTPUT", "Truncated engine output")
		}
		// Route stderr frames separately from binary stdout snapshots.
		if data[offset] == 2 {
			stderr.Write(data[offset+8 : offset+8+size])
		} else {
			stdout.Write(data[offset+8 : offset+8+size])
		}
		offset += 8 + size
	}
	// Preserve raw stdout when Docker did not return framed output.
	if offset == 0 {
		return data, nil, nil
	}
	// Reject this request: Invalid engine output.
	if offset != len(data) {
		return nil, nil, model.Err("RESOURCE_OUTPUT", "Invalid engine output")
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}

// execute runs an authenticated engine command and decodes its Docker output.
func (m *Manager) execute(ctx context.Context, name string, command, env []string) ([]byte, error) {
	current, err := m.inspect(ctx, name)
	// Require a verified owned container before changing or reporting its state.
	if err != nil {
		return nil, err
	}
	var created struct {
		ID string `json:"Id"`
	}
	_, err = m.api(ctx, "POST", "/containers/"+current.ID+"/exec", map[string]any{"AttachStdout": true, "AttachStderr": true, "Tty": false, "Cmd": command, "Env": env}, &created)
	// Require the owned Docker API request to complete before using its result.
	if err != nil {
		return nil, err
	}
	data, err := m.api(ctx, "POST", "/exec/"+created.ID+"/start", map[string]any{"Detach": false, "Tty": false}, nil)
	// Require the owned Docker API request to complete before using its result.
	if err != nil {
		return nil, err
	}
	var status struct{ ExitCode int }
	_, err = m.api(ctx, "GET", "/exec/"+created.ID+"/json", nil, &status)
	// Require the owned Docker API request to complete before using its result.
	if err != nil {
		return nil, err
	}
	// Reject this request: Authenticated resource operation failed.
	if status.ExitCode != 0 {
		return nil, model.Err("RESOURCE_PROTOCOL", "Authenticated resource operation failed")
	}
	stdout, _, err := demultiplex(data)
	return stdout, err
}

// wait pauses polling while allowing context cancellation.
func wait(ctx context.Context, period time.Duration) error {
	timer := time.NewTimer(period)
	defer timer.Stop()
	// Wait for completion or the cancellation and shutdown deadline.
	select {
	// Stop waiting immediately when the operation is cancelled.
	case <-ctx.Done():
		return ctx.Err()
	// Resume polling after the configured interval.
	case <-timer.C:
		return nil
	}
}

// archive packages one private file for the Docker archive transport.
func archive(name string, value []byte, uid int) ([]byte, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	// Require archive metadata to be written before the file payload.
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Uid: uid, Gid: uid, Size: int64(len(value)), ModTime: time.Unix(0, 0)}); err != nil {
		return nil, err
	}
	// Require the private archive or state bytes to be written successfully.
	if _, err := writer.Write(value); err != nil {
		return nil, err
	}
	// Finalize the tar stream before returning bytes for Docker upload.
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// upload copies a private file into the selected container using tar transport.
func (m *Manager) upload(ctx context.Context, id, path, name string, data []byte, uid int) error {
	body, err := archive(name, data, uid)
	// Require the private tar payload to encode successfully before upload.
	if err != nil {
		return err
	}
	_, err = m.api(ctx, "PUT", "/containers/"+id+"/archive?path="+url.QueryEscape(path), body, nil)
	return err
}

// syncDirectory makes an atomic state replacement durable on its filesystem.
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	// Require the containing directory to open before syncing the atomic replacement.
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// decodeResourceResponse bounds Docker output and normalizes errors before decoding metadata.
func decodeResourceResponse(response *http.Response, result any) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxBackupBytes+1048577))
	// Reject this request: Resource response interrupted.
	if err != nil {
		return nil, model.Err("RESOURCE_RUNTIME", "Resource response interrupted")
	}
	// Reject this request: Resource output exceeds 64 MiB backup limit.
	if len(data) > MaxBackupBytes+1048576 {
		return nil, model.Err("BACKUP_LIMIT", "Resource output exceeds 64 MiB backup limit")
	}
	// Reject unsuccessful Docker execution API responses.
	if response.StatusCode >= 400 {
		return nil, model.Err(fmt.Sprintf("HTTP_%d", response.StatusCode), fmt.Sprintf("Resource runtime request failed (%d)", response.StatusCode))
	}
	// Decode Docker metadata only when a destination and a nonempty response were supplied.
	if result != nil && len(data) > 0 {
		// Reject this request: Invalid Docker resource response.
		if json.Unmarshal(data, result) != nil {
			return nil, model.Err("RESOURCE_RESPONSE", "Invalid Docker resource response")
		}
	}
	return data, nil
}

// supportedDatabaseImage accepts only the shipped default or a separately validated immutable upgrade target.
func supportedDatabaseImage(spec model.ResourceSpec) bool {
	return spec.Image == "" || spec.Image == Images[spec.Type] || model.ApprovedDatabaseImage(spec.Type, spec.Image)
}
