package resources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"hostlier/internal/model"
)

// K3sManager owns private Kubernetes resource namespaces and durable local records.
type K3sManager struct {
	options Options
	owner   string
	mu      sync.Mutex
}

// newK3s constructs an explicitly selected Kubernetes resource manager.
func newK3s(o Options) (model.ResourceManager, error) {
	// Use the installed k3s binary to invoke its kubectl subcommand by default.
	if o.K3sCommand == "" {
		o.K3sCommand = "/usr/local/bin/k3s"
	}
	// Select the local k3s kubeconfig explicitly instead of ambient cluster configuration.
	if o.Kubeconfig == "" {
		o.Kubeconfig = "/etc/rancher/k3s/k3s.yaml"
	}
	// Bound resource health polling with the default timeout.
	if o.HealthTimeout == 0 {
		o.HealthTimeout = 3 * time.Minute
	}
	sum := sha256.Sum256([]byte(o.StateDir))
	return &K3sManager{options: o, owner: hex.EncodeToString(sum[:])[:16]}, nil
}

// Name identifies the resource backend.
func (m *K3sManager) Name() string { return "k3s" }

// namespace derives an owner-scoped namespace for one resource.
func (m *K3sManager) namespace(name string) string {
	value := "hostlier-" + m.owner + "-resource-" + name
	// Keep the owner-scoped namespace within Kubernetes DNS label limits.
	if len(value) > 63 {
		sum := sha256.Sum256([]byte(value))
		value = value[:46] + "-" + hex.EncodeToString(sum[:])[:16]
	}
	return value
}

// labels identifies the owner and resource on every managed object.
func (m *K3sManager) labels(name string) map[string]string {
	return map[string]string{"io.hostlier.managed": "true", "io.hostlier.owner": m.owner, "io.hostlier.resource": name}
}

// meta builds Kubernetes metadata with ownership labels.
func (m *K3sManager) meta(name, object string) map[string]any {
	return map[string]any{"name": object, "namespace": m.namespace(name), "labels": m.labels(name)}
}

// run executes kubectl against the explicit kubeconfig with bounded output.
func (m *K3sManager) run(ctx context.Context, args []string, input []byte) ([]byte, error) {
	fixed := append([]string{"kubectl", "--kubeconfig=" + m.options.Kubeconfig, "--context=default", "--request-timeout=20s"}, args...)
	// Use the explicitly injected kubectl runner when configured.
	if m.options.KubeRun != nil {
		return m.options.KubeRun(ctx, fixed, input)
	}
	bounded, cancel := context.WithTimeout(ctx, m.options.HealthTimeout)
	defer cancel()
	cmd := exec.CommandContext(bounded, m.options.K3sCommand, fixed...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "KUBECTL_KUBERC=false"}
	cmd.Stdin = bytes.NewReader(input)
	// Kill the running kubectl command as soon as stdout exceeds the snapshot bound.
	output := &kubeOutputLimit{limit: MaxBackupBytes, kill: func() {
		// Terminate only a process that was successfully started.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}}
	// Kill the running kubectl command if stderr exceeds the diagnostic bound.
	diagnostic := &kubeOutputLimit{limit: 65536, kill: func() {
		// Terminate only a process that was successfully started.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}}
	cmd.Stderr = diagnostic
	cmd.Stdout = output
	e := cmd.Run()
	// Reject this request: Resource output exceeds64 MiB limit.
	if output.exceeded {
		return nil, model.Err("BACKUP_LIMIT", "Resource output exceeds64 MiB limit")
	}
	// Reject this request: Kubernetes resource operation failed; inspect protected cluster diagnostics.
	if e != nil {
		// Emit maintenance diagnostics only when a diagnostic consumer was configured.
		if m.options.OnDiagnostic != nil {
			m.options.OnDiagnostic(strings.Join(args, " ") + ": " + diagnostic.buffer.String())
		}
		return nil, model.Err("RESOURCE_KUBERNETES", "Kubernetes resource operation failed; inspect protected cluster diagnostics")
	}
	return output.buffer.Bytes(), nil
}

// get reads a Kubernetes object without treating absence as an execution failure.
func (m *K3sManager) get(ctx context.Context, kind, name, namespace string) (map[string]any, error) {
	args := []string{"get", kind, name, "--ignore-not-found", "-o", "json"}
	// Scope namespaced object requests explicitly while allowing cluster-scoped objects.
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	raw, e := m.run(ctx, args, nil)
	// Require the explicitly scoped kubectl operation to complete successfully.
	if e != nil {
		return nil, e
	}
	// Treat empty kubectl output as an absent object.
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var object map[string]any
	// Reject this request: Invalid resource Kubernetes JSON.
	if json.Unmarshal(raw, &object) != nil {
		return nil, model.Err("RESOURCE_RESPONSE", "Invalid resource Kubernetes JSON")
	}
	return object, nil
}

// owned verifies that object labels match this manager and resource.
func (m *K3sManager) owned(object map[string]any, name string) bool {
	meta, _ := object["metadata"].(map[string]any)
	labels, _ := meta["labels"].(map[string]any)
	return labels["io.hostlier.managed"] == "true" && labels["io.hostlier.owner"] == m.owner && labels["io.hostlier.resource"] == name
}

// apply applies only manifests whose existing objects belong to this owner.
func (m *K3sManager) apply(ctx context.Context, manifest map[string]any, name string) error {
	kind := manifest["kind"].(string)
	meta := manifest["metadata"].(map[string]any)
	object := meta["name"].(string)
	namespace, _ := meta["namespace"].(string)
	existing, e := m.get(ctx, kind, object, namespace)
	// Require the explicitly scoped Kubernetes object lookup to complete.
	if e != nil {
		return e
	}
	// Reject this request: Cannot adopt unowned Kubernetes resource.
	if existing != nil && !m.owned(existing, name) {
		return model.Err("RESOURCE_OWNERSHIP", "Cannot adopt unowned Kubernetes resource")
	}
	// Keep recognized immutable selectors when reusing a deployment created by an older version.
	if e = m.preserveEngineSelector(manifest, existing, name); e != nil {
		return e
	}
	data, _ := json.Marshal(manifest)
	_, e = m.run(ctx, []string{"apply", "--server-side", "--field-manager=hostlier-resources", "-f", "-"}, data)
	return e
}

// remove deletes only an object whose ownership has been verified.
func (m *K3sManager) remove(ctx context.Context, kind, object, namespace, name string) error {
	existing, e := m.get(ctx, kind, object, namespace)
	// Propagate lookup failures and treat an already absent object as successfully removed.
	if e != nil || existing == nil {
		return e
	}
	// Reject this request: Cannot delete unowned Kubernetes resource.
	if !m.owned(existing, name) {
		return model.Err("RESOURCE_OWNERSHIP", "Cannot delete unowned Kubernetes resource")
	}
	args := []string{"delete", kind, object, "--ignore-not-found", "--wait=true", "--timeout=120s"}
	// Scope namespaced object requests explicitly while allowing cluster-scoped objects.
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	_, e = m.run(ctx, args, nil)
	return e
}

// read loads resource records from the private state file.
func (m *K3sManager) read() (map[string]record, error) {
	raw, e := os.ReadFile(filepath.Join(m.options.StateDir, "resources-k3s.json"))
	// Start with empty records when no state file has been created.
	if os.IsNotExist(e) {
		return map[string]record{}, nil
	}
	// Require readable resource metadata before decoding the persisted state.
	if e != nil {
		return nil, e
	}
	var state map[string]record
	// Reject this request: Cannot decode resource metadata.
	if json.Unmarshal(raw, &state) != nil {
		return nil, model.Err("RESOURCE_STATE", "Cannot decode resource metadata")
	}
	return state, nil
}

// save atomically persists resource records and syncs their parent directory.
func (m *K3sManager) save(state map[string]record) error {
	// Create the private resource state directory before saving records.
	if e := os.MkdirAll(m.options.StateDir, 0700); e != nil {
		return e
	}
	raw, _ := json.Marshal(state)
	f, e := os.CreateTemp(m.options.StateDir, ".resources-k3s-*")
	// Propagate the preceding persistence failure before advancing the atomic save.
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	// Write resource metadata only after protecting its temporary file permissions.
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	// Sync metadata only after its complete byte payload has been written.
	if e == nil {
		e = f.Sync()
	}
	f.Close()
	// Propagate the preceding persistence failure before advancing the atomic save.
	if e != nil {
		return e
	}
	// Require atomic state replacement before reporting persistence complete.
	if e = os.Rename(f.Name(), filepath.Join(m.options.StateDir, "resources-k3s.json")); e != nil {
		return e
	}
	directory, e := os.Open(m.options.StateDir)
	// Propagate the preceding persistence failure before advancing the atomic save.
	if e != nil {
		return e
	}
	defer directory.Close()
	return directory.Sync()
}

// find looks up an existing resource by its logical name.
func (m *K3sManager) find(name string) (record, error) {
	state, e := m.read()
	// Require valid persisted resource state before looking up or changing records.
	if e != nil {
		return record{}, e
	}
	item, ok := state[name]
	// Reject this request: Resource not found.
	if !ok {
		return item, model.Err("RESOURCE_NOT_FOUND", "Resource not found")
	}
	return item, nil
}

// Plan normalizes a resource request and reports changes without provisioning.
func (m *K3sManager) Plan(ctx context.Context, input model.ResourceSpec) (model.ResourcePlan, error) {
	spec, e := normalize(input)
	// Reject invalid resource specifications before creating engine objects.
	if e != nil {
		return model.ResourcePlan{}, e
	}
	state, e := m.read()
	// Require valid persisted resource state before looking up or changing records.
	if e != nil {
		return model.ResourcePlan{}, e
	}
	action := "Create"
	// Compare retained settings before deciding whether this resource can be reused.
	if prior, ok := state[spec.Name]; ok {
		// Reject this request: Retained resource settings differ; upgrades are unsupported.
		if prior.Spec != spec {
			return model.ResourcePlan{}, model.Err("RESOURCE_EXISTS", "Retained resource settings differ; upgrades are unsupported")
		}
		action = "Reuse"
	}
	changes := []string{action + " private k3s " + spec.Type + " resource " + spec.Name, "Pinned image " + spec.Image, "Requested local-path PVC " + spec.Storage + "; no enforced quota or server-failure failover", "Authenticated protocol readiness; only declared application namespaces can connect", "Retain namespace/PVC on ordinary delete; explicit purge removes selected owned namespace/data"}
	// Include the Redis AOF durability window in the resource plan.
	if spec.Type == "redis" {
		changes = append(changes, "Redis AOF appendfsync everysec; sudden failure may lose one second of acknowledged writes")
	}
	return model.ResourcePlan{Spec: spec, Changes: changes}, nil
}

// manifests builds private engine workloads and their credential Secrets.
func (m *K3sManager) manifests(spec model.ResourceSpec, c model.ResourceCredentials) []map[string]any {
	namespace := m.namespace(spec.Name)
	ns := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": namespace, "labels": m.labels(spec.Name)}}
	port := 5432
	mount := "/var/lib/postgresql/data"
	data := map[string]string{}
	envNames := []string{}
	container := map[string]any{"name": "engine", "image": spec.Image}
	// Select the native behavior for the requested engine or lifecycle operation.
	switch spec.Type {
	// Use PostgreSQL native commands and its authenticated database configuration.
	case "postgres":
		data = map[string]string{"POSTGRES_USER": c.Username, "POSTGRES_PASSWORD": c.Password, "POSTGRES_DB": c.Database, "POSTGRES_INITDB_ARGS": "--auth-host=scram-sha-256 --auth-local=scram-sha-256"}
		envNames = []string{"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB", "POSTGRES_INITDB_ARGS"}
	// Use Redis native commands and its authenticated persistence configuration.
	case "redis":
		port = 6379
		mount = "/data"
		data["redis.conf"] = "bind 0.0.0.0\nprotected-mode yes\nport 6379\ndir /data\nappendonly yes\nappendfsync everysec\nuser default off\nuser " + c.Username + " on >" + c.Password + " ~* +@all\n"
		container["command"] = []string{"redis-server", "/run/hostlier/redis.conf"}
	// Use Neo4j native commands and its authenticated database configuration.
	case "neo4j":
		port = 7687
		mount = "/data"
		data = map[string]string{"NEO4J_AUTH": c.Username + "/" + c.Password, "NEO4J_server_memory_heap_initial__size": "256m", "NEO4J_server_memory_heap_max__size": "512m", "NEO4J_server_memory_pagecache_size": "256m"}
		envNames = []string{"NEO4J_AUTH", "NEO4J_server_memory_heap_initial__size", "NEO4J_server_memory_heap_max__size", "NEO4J_server_memory_pagecache_size"}
	}
	env := []any{}
	// Reference each configured engine environment value through the credentials Secret.
	for _, name := range envNames {
		env = append(env, map[string]any{"name": name, "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": "credentials", "key": name}}})
	}
	// Recreated resources retain their durable desired allocation.
	if spec.CPU != "" || spec.Memory != "" {
		container["resources"] = map[string]any{"limits": map[string]string{"cpu": spec.CPU, "memory": spec.Memory}, "requests": map[string]string{"cpu": spec.CPU, "memory": spec.Memory}}
	}
	container["env"] = env
	container["ports"] = []any{map[string]any{"containerPort": port}}
	mounts := []any{map[string]string{"name": "data", "mountPath": mount}}
	volumes := []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]string{"claimName": "data"}}}
	// Mount the Redis configuration Secret read-only into its engine container.
	if spec.Type == "redis" {
		mounts = append(mounts, map[string]any{"name": "redis-config", "mountPath": "/run/hostlier", "readOnly": true})
		volumes = append(volumes, map[string]any{"name": "redis-config", "secret": map[string]any{"secretName": "credentials", "defaultMode": 0440, "items": []any{map[string]string{"key": "redis.conf", "path": "redis.conf"}}}})
	}
	container["volumeMounts"] = mounts
	container["readinessProbe"] = map[string]any{"tcpSocket": map[string]any{"port": port}, "periodSeconds": 2, "failureThreshold": 90}
	engineLabels := m.labels(spec.Name)
	engineLabels["io.hostlier.role"] = "engine"
	deployment := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": m.meta(spec.Name, "engine"), "spec": map[string]any{"replicas": 1, "strategy": map[string]string{"type": "Recreate"}, "selector": map[string]any{"matchLabels": engineLabels}, "template": map[string]any{"metadata": map[string]any{"labels": engineLabels}, "spec": map[string]any{"automountServiceAccountToken": false, "securityContext": map[string]any{"fsGroup": 999}, "containers": []any{container}, "volumes": volumes}}}}
	policy := map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": m.meta(spec.Name, "resource-isolation"), "spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []string{"Ingress", "Egress"}, "ingress": []any{map[string]any{"from": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"io.hostlier.owner": m.owner, "io.hostlier.managed": "true"}, "matchExpressions": []any{map[string]any{"key": "io.hostlier.app", "operator": "Exists"}}}}}, "ports": []any{map[string]any{"port": port, "protocol": "TCP"}}}}, "egress": []any{map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}}, "ports": []any{map[string]any{"port": 53, "protocol": "UDP"}, map[string]any{"port": 53, "protocol": "TCP"}}}}}}
	secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": m.meta(spec.Name, "credentials"), "type": "Opaque", "stringData": data}
	claim := map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": m.meta(spec.Name, "data"), "spec": map[string]any{"storageClassName": "local-path", "accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": spec.Storage}}}}
	service := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": m.meta(spec.Name, "engine"), "spec": map[string]any{"type": "ClusterIP", "selector": engineLabels, "ports": []any{map[string]any{"port": port, "targetPort": port}}}}
	return []map[string]any{ns, policy, secret, claim, deployment, service}
}

// protocol checks the native database protocol using credentials supplied through stdin.
func (m *K3sManager) protocol(ctx context.Context, item record, c model.ResourceCredentials) ([]byte, error) {
	script := ""
	// Select the native behavior for the requested engine or lifecycle operation.
	switch item.Type {
	// Use PostgreSQL native commands and its authenticated database configuration.
	case "postgres":
		script = `IFS= read -r PGPASSWORD; export PGPASSWORD; exec psql -h 127.0.0.1 -U hostlier -d app -Atc 'SELECT 1'`
	// Use Redis native commands and its authenticated persistence configuration.
	case "redis":
		script = `IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; exec redis-cli --user hostlier PING`
	// Use Neo4j native commands and its authenticated database configuration.
	case "neo4j":
		script = `IFS= read -r NEO4J_PASSWORD; export NEO4J_PASSWORD; exec cypher-shell -a bolt://127.0.0.1:7687 -u neo4j 'RETURN 1;'`
	}
	out, e := m.run(ctx, []string{"exec", "-i", "deployment/engine", "-n", item.Network, "--", "sh", "-c", script}, []byte(c.Password+"\n"))
	// Reject this request: Redis authentication failed.
	if e == nil && item.Type == "redis" && strings.TrimSpace(string(out)) != "PONG" {
		return nil, model.Err("RESOURCE_PROTOCOL", "Redis authentication failed")
	}
	return out, e
}

// ready waits for authenticated engine health within the configured deadline.
func (m *K3sManager) ready(ctx context.Context, item record, c model.ResourceCredentials) error {
	bounded, cancel := context.WithTimeout(ctx, m.options.HealthTimeout)
	defer cancel()
	// Poll authenticated engine health until success or context timeout.
	for {
		out, e := m.protocol(bounded, item, c)
		// Accept readiness only after an authenticated protocol success and Redis PONG.
		if e == nil && (item.Type != "redis" || strings.TrimSpace(string(out)) == "PONG") {
			return nil
		}
		// Reject this request: Resource did not pass authenticated protocol readiness.
		if e = wait(bounded, 500*time.Millisecond); e != nil {
			return model.Err("RESOURCE_READINESS", "Resource did not pass authenticated protocol readiness")
		}
	}
}

// Provision creates or resumes one owned resource with validated credentials.
func (m *K3sManager) Provision(ctx context.Context, input model.ResourceSpec, operation model.ResourceContext) (model.Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan, e := m.Plan(ctx, input)
	// Require a valid read-only resource plan before using its normalized specification.
	if e != nil {
		return model.Resource{}, e
	}
	spec := plan.Spec
	// Validate engine credentials before issuing authenticated commands.
	if e = validateCredentials(spec.Type, operation.Credentials); e != nil {
		return model.Resource{}, e
	}
	state, e := m.read()
	// Require valid persisted resource state before looking up or changing records.
	if e != nil {
		return model.Resource{}, e
	}
	item, ok := state[spec.Name]
	// Persist a new provisioning record only when this resource has no retained record.
	if !ok {
		item = record{Resource: model.Resource{Name: spec.Name, Type: spec.Type, RuntimeID: "engine", Network: m.namespace(spec.Name), Status: "provisioning"}, Spec: spec, Volume: "data"}
		state[spec.Name] = item
		// Require durable resource state persistence before reporting completion.
		if e = m.save(state); e != nil {
			return item.Resource, e
		}
	}
	// Apply the namespace, isolation policy, credentials, storage and workload in dependency order.
	for _, manifest := range m.manifests(spec, operation.Credentials) {
		// Require the owned Kubernetes manifest to apply successfully.
		if e = m.apply(ctx, manifest, spec.Name); e != nil {
			return item.Resource, e
		}
	}
	// Require the explicitly scoped kubectl operation to complete successfully.
	if _, e = m.run(ctx, []string{"rollout", "status", "deployment/engine", "-n", item.Network, "--timeout=" + strconv.Itoa(int(m.options.HealthTimeout.Seconds())) + "s"}, nil); e != nil {
		return item.Resource, e
	}
	// Require authenticated readiness before recording the engine as running.
	if e = m.ready(ctx, item, operation.Credentials); e != nil {
		return item.Resource, e
	}
	item.Status = "running"
	state[spec.Name] = item
	return item.Resource, m.save(state)
}

// Action performs a lifecycle action on the selected owned resource.
func (m *K3sManager) Action(ctx context.Context, name, action string, operation model.ResourceContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, e := m.read()
	// Require valid persisted resource state before looking up or changing records.
	if e != nil {
		return e
	}
	item, ok := state[name]
	// Reject this request: Resource not found.
	if !ok {
		return model.Err("RESOURCE_NOT_FOUND", "Resource not found")
	}
	namespace, e := m.get(ctx, "namespace", item.Network, "")
	// Require the explicitly scoped Kubernetes object lookup to complete.
	if e != nil {
		return e
	}
	// Reject this request: Resource namespace belongs to another owner.
	if namespace != nil && !m.owned(namespace, name) {
		return model.Err("RESOURCE_OWNERSHIP", "Resource namespace belongs to another owner")
	}
	// Require the Kubernetes lifecycle action before persisting its resulting state.
	if err := m.applyResourceAction(ctx, name, action, item, state, operation); err != nil {
		return err
	}
	return m.save(state)
}

// Status reports the observed state of the selected owned resource.
func (m *K3sManager) Status(ctx context.Context, name string) (model.Resource, error) {
	item, e := m.find(name)
	// Require an existing resource record before operating on engine state.
	if e != nil {
		return model.Resource{}, e
	}
	deployment, e := m.get(ctx, "deployment", "engine", item.Network)
	// Require the explicitly scoped Kubernetes object lookup to complete.
	if e != nil {
		return item.Resource, e
	}
	item.Status = "deleted"
	// Report workload readiness only when the deployment still exists.
	if deployment != nil {
		status, _ := deployment["status"].(map[string]any)
		ready, _ := status["readyReplicas"].(float64)
		item.Status = "stopped"
		// Report running only after Kubernetes observes a ready replica.
		if ready > 0 {
			item.Status = "running"
		}
	}
	return item.Resource, nil
}

// Connection returns a private engine connection after validating its credentials.
func (m *K3sManager) Connection(ctx context.Context, name string, c model.ResourceCredentials) (map[string]string, error) {
	item, e := m.find(name)
	// Require an existing resource record before operating on engine state.
	if e != nil {
		return nil, e
	}
	// Validate engine credentials before issuing authenticated commands.
	if e = validateCredentials(item.Type, c); e != nil {
		return nil, e
	}
	host := "engine." + item.Network + ".svc.cluster.local"
	// Return the Bolt URI and separate Neo4j credentials expected by clients.
	if item.Type == "neo4j" {
		return map[string]string{"uri": "bolt://" + host + ":7687", "username": c.Username, "password": c.Password}, nil
	}
	scheme, port, path := "postgresql", "5432", "/"+c.Database
	// Build a Redis URL using its native port and default database index.
	if item.Type == "redis" {
		scheme = "redis"
		port = "6379"
		path = "/0"
	}
	address := url.URL{Scheme: scheme, User: url.UserPassword(c.Username, c.Password), Host: net.JoinHostPort(host, port), Path: path}
	return map[string]string{"url": address.String()}, nil
}

// Logs reads diagnostics from the selected resource workload.
func (m *K3sManager) Logs(ctx context.Context, name string) (string, error) {
	item, e := m.find(name)
	// Require an existing resource record before operating on engine state.
	if e != nil {
		return "", e
	}
	out, e := m.run(ctx, []string{"logs", "deployment/engine", "-n", item.Network, "--tail=100", "--limit-bytes=65536"}, nil)
	return string(out), e
}

// kubeOutputLimit kills the child as soon as stdout exceeds the export bound;
// it never accumulates an unbounded database stream in controller memory.
type kubeOutputLimit struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	kill     func()
}

// Write bounds kubectl output and terminates a command that exceeds the limit.
func (w *kubeOutputLimit) Write(p []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	// Truncate this write before it exceeds the process output budget.
	if len(p) > remaining {
		// Retain the last bytes that fit in the bounded output buffer.
		if remaining > 0 {
			_, _ = w.buffer.Write(p[:remaining])
		}
		// Append in-budget output only while no earlier write has exceeded the limit.
		if !w.exceeded {
			w.exceeded = true
			// Terminate the producer when an output-limit cancellation hook is available.
			if w.kill != nil {
				w.kill()
			}
		}
		return len(p), nil
	}
	// Append in-budget output only while no earlier write has exceeded the limit.
	if !w.exceeded {
		_, _ = w.buffer.Write(p)
	}
	return len(p), nil
}

// applyResourceAction reconciles Kubernetes engine workloads for the requested lifecycle action.
func (m *K3sManager) applyResourceAction(ctx context.Context, name, action string, item record, state map[string]record, operation model.ResourceContext) error {
	var e error

	// Select the native behavior for the requested engine or lifecycle operation.
	switch action {
	// Delete the workload and purge data only when explicitly requested.
	case "delete":
		return m.deleteK3sResource(ctx, name, item, state, operation)
	// Stop the selected workload without deleting its data.
	case "stop":
		// Require the explicitly scoped kubectl operation to complete successfully.
		if _, e = m.run(ctx, []string{"scale", "deployment/engine", "-n", item.Network, "--replicas=0"}, nil); e != nil {
			return e
		}
		// Require the explicitly scoped kubectl operation to complete successfully.
		if _, e = m.run(ctx, []string{"wait", "--for=delete", "pods", "-n", item.Network, "-l", "io.hostlier.role=engine", "--timeout=120s"}, nil); e != nil {
			return e
		}
		item.Status = "stopped"
		state[name] = item
	// Start the workload and verify authenticated readiness.
	case "start", "restart":
		return m.startK3sResource(ctx, name, action, item, state, operation)
	// Reject unsupported values rather than guessing an engine behavior.
	default:
		return model.Err("RESOURCE_ACTION", "Unsupported resource action")
	}
	return nil
}

// deleteK3sResource preserves data unless namespace purge was explicitly approved.
func (m *K3sManager) deleteK3sResource(ctx context.Context, name string, item record, state map[string]record, operation model.ResourceContext) error {
	var e error

	// Remove persistent data only for an explicitly requested purge.
	if operation.Purge {
		// Require ownership-checked Kubernetes deletion before continuing cleanup.
		if e = m.remove(ctx, "namespace", item.Network, "", name); e != nil {
			return e
		}
		delete(state, name)
	} else {
		// Require ownership-checked Kubernetes deletion before continuing cleanup.
		if e = m.remove(ctx, "deployment", "engine", item.Network, name); e != nil {
			return e
		}
		// Require ownership-checked Kubernetes deletion before continuing cleanup.
		if e = m.remove(ctx, "service", "engine", item.Network, name); e != nil {
			return e
		}
		item.Status = "deleted"
		state[name] = item
	}
	return nil
}

// startK3sResource verifies credentials and native readiness after scaling or restarting.
func (m *K3sManager) startK3sResource(ctx context.Context, name, action string, item record, state map[string]record, operation model.ResourceContext) error {
	var e error

	// Validate engine credentials before issuing authenticated commands.
	if e = validateCredentials(item.Type, operation.Credentials); e != nil {
		return e
	}
	// Restart the deployment through a rolling restart command.
	if action == "restart" {
		_, e = m.run(ctx, []string{"rollout", "restart", "deployment/engine", "-n", item.Network}, nil)
	} else {
		_, e = m.run(ctx, []string{"scale", "deployment/engine", "-n", item.Network, "--replicas=1"}, nil)
	}
	// Require the explicitly scoped kubectl operation to complete successfully.
	if e != nil {
		return e
	}
	// Require the explicitly scoped kubectl operation to complete successfully.
	if _, e = m.run(ctx, []string{"rollout", "status", "deployment/engine", "-n", item.Network, "--timeout=" + strconv.Itoa(int(m.options.HealthTimeout.Seconds())) + "s"}, nil); e != nil {
		return e
	}
	// Require authenticated readiness before recording the engine as running.
	if e = m.ready(ctx, item, operation.Credentials); e != nil {
		return e
	}
	item.Status = "running"
	state[name] = item
	return nil
}

// preserveEngineSelector retains either recognized owned selector while rejecting unrelated selector drift.
func (m *K3sManager) preserveEngineSelector(manifest, existing map[string]any, name string) error {
	// Only existing engine Deployments have this historical immutable selector compatibility boundary.
	if existing == nil || manifest["kind"] != "Deployment" {
		return nil
	}
	existingSpec, _ := existing["spec"].(map[string]any)
	selector := existingSpec["selector"]
	legacy := resourceDeploymentSelector(m.labels(name))
	currentLabels := m.labels(name)
	currentLabels["io.hostlier.role"] = "engine"
	current := resourceDeploymentSelector(currentLabels)
	// Preserve only the exact old or current ownership selector, never arbitrary user-selected workloads.
	if !reflect.DeepEqual(selector, legacy) && !reflect.DeepEqual(selector, current) {
		return model.Err("RESOURCE_SELECTOR_DRIFT", "Owned resource deployment selector differs from supported engine selectors")
	}
	desiredSpec := manifest["spec"].(map[string]any)
	desiredSpec["selector"] = selector
	return nil
}

// resourceDeploymentSelector builds the decoded JSON shape used for exact immutable-selector comparisons.
func resourceDeploymentSelector(labels map[string]string) map[string]any {
	values := map[string]any{}
	// Copy ownership labels without sharing mutable maps with pod-template or service selectors.
	for key, value := range labels {
		values[key] = value
	}
	return map[string]any{"matchLabels": values}
}
