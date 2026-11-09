package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hostlier/internal/config"
	"hostlier/internal/model"
	hostruntime "hostlier/internal/runtime"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestAgentGroups exercises the same workload scenarios against real Docker and k3s.
func TestAgentGroups(t *testing.T) {
	ctx, r, image, namespace, get, backend, owner, _ := agentFixture(t)
	p := agentGroupProject(image.Digest, namespace.Digest)
	p, err := config.ValidateProject(p)
	// Real tests start from the same schema used by controller approval.
	if err != nil {
		t.Fatal(err)
	}
	dc := model.DeployContext{OperationID: "group-first", Secrets: map[string]string{"assistant/AGENT_TOKEN": "sidecar-only-secret"}}
	first, err := r.Deploy(ctx, p, dc)
	// Group activation must establish real sidecar loopback and HTTPS health.
	if err != nil {
		t.Fatal(err)
	}
	status, body := get("https://agent.hostlier.test/")
	// The primary response proves it successfully reached its loopback sidecar.
	if status != 200 || !strings.Contains(body, "sidecar-v1") {
		t.Fatalf("group response %d: %s", status, body)
	}
	verifyPrivateRuntime(t, backend, owner)
	verifyGroupWorker(t, ctx, r, p.Name)
	testAgentRollback(t, ctx, r, image.Digest, namespace.Digest, dc, get)
	// Lifecycle restart must keep primary and sidecar on the same loopback namespace.
	if err = r.Action(ctx, p.Name, "restart", model.ActionContext{Application: &first}); err != nil {
		t.Fatal(err)
	}
	status, body = get("https://agent.hostlier.test/")
	// Restart cannot accidentally detach the sidecar's localhost listener.
	if status != 200 || !strings.Contains(body, "sidecar-v1") {
		t.Fatalf("restart response %d: %s", status, body)
	}
	verifyGroupWorker(t, ctx, r, p.Name)
	testPrivateWorker(t, ctx, r, image.Digest, namespace.Digest)
	testPrivateService(t, ctx, r, image.Digest, namespace.Digest)
	// Explicit purge is confined to this test's unique owner.
	if err = r.Action(ctx, p.Name, "delete", model.ActionContext{Purge: true}); err != nil {
		t.Fatal(err)
	}
}

// agentGroupProject requires generic HTTP and worker sidecars with separate process credentials.
func agentGroupProject(image, namespace string) model.Project {
	p := model.Project{Version: 2, Name: "agent", NamespaceImage: namespace, Source: &model.Source{Image: image}, Shutdown: &model.Shutdown{GraceSeconds: 2}, Workload: "service", Endpoint: &model.Endpoint{Port: 8080, Exposure: "public", Route: "agent.hostlier.test"}, Command: []string{"sh", "-c"}, Args: []string{`mkdir -p /tmp/web; until wget -qO /tmp/web/index.html http://127.0.0.1:1907; do sleep 1; done; test -z "$AGENT_TOKEN" || exit 9; exec httpd -f -p 8080 -h /tmp/web`}, Health: &model.Health{Path: "/"}, Sidecars: map[string]model.Project{"assistant": {Shutdown: &model.Shutdown{GraceSeconds: 2}, Source: &model.Source{Image: image}, Endpoint: &model.Endpoint{Port: 1907, Exposure: "loopback"}, Command: []string{"sh", "-c"}, Args: []string{`test "$AGENT_TOKEN" = sidecar-only-secret || exit 8; mkdir -p /tmp/agent; echo sidecar-v1 > /tmp/agent/index.html; exec httpd -f -p 127.0.0.1:1907 -h /tmp/agent`}, Health: &model.Health{Exec: []string{"wget", "-qO-", "http://127.0.0.1:1907"}}}}}
	p.Sidecars["worker"] = model.Project{Source: &model.Source{Image: image}, Workload: "worker", Shutdown: &model.Shutdown{GraceSeconds: 2}, Endpoint: &model.Endpoint{Exposure: "none"}, Command: []string{"sh", "-c"}, Args: []string{`test -z "$AGENT_TOKEN" || exit 9; echo ordinary-worker-ready; touch /tmp/ready; exec sleep 3600`}, Health: &model.Health{Exec: []string{"test", "-f", "/tmp/ready"}}}
	return p
}

// verifyGroupWorker proves the listenerless sibling ran and has independently selectable logs.
func verifyGroupWorker(t *testing.T, ctx context.Context, r model.Runtime, name string) {
	t.Helper()
	reader := r.(interface {
		// ComponentLogs selects only a member owned by the deployed application group.
		ComponentLogs(context.Context, string, string) (string, error)
	})
	logs, err := reader.ComponentLogs(ctx, name, "worker")
	// The worker writes this marker only after proving it did not inherit the backend credential.
	if err != nil || !strings.Contains(logs, "ordinary-worker-ready") {
		t.Fatalf("generic worker logs: %q %v", logs, err)
	}
}

// testPrivateWorker verifies lifecycle for a workload with no port or public route.
func testPrivateWorker(t *testing.T, ctx context.Context, r model.Runtime, image, namespace string) {
	t.Helper()
	p := model.Project{Version: 2, Name: "worker", NamespaceImage: namespace, Source: &model.Source{Image: image}, Workload: "worker", Command: []string{"sh", "-c"}, Args: []string{"touch /tmp/ready; exec sleep 3600"}, Health: &model.Health{Exec: []string{"test", "-f", "/tmp/ready"}}}
	p, err := config.ValidateProject(p)
	// Listenerless workers are a first-class schema case on both backends.
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.Deploy(ctx, p, model.DeployContext{OperationID: "worker"})
	// A worker must run successfully without fabricated public access.
	if err != nil || d.URL != "" {
		t.Fatalf("worker: %+v %v", d, err)
	}
	// Stop/start must not require any HTTP endpoint.
	for _, action := range []string{"stop", "start", "delete"} {
		// Every action is applied to only the recorded test worker.
		if err = r.Action(ctx, p.Name, action, model.ActionContext{Purge: action == "delete"}); err != nil {
			t.Fatal(action, err)
		}
	}
}

// testPrivateService checks authorized discovery and rejects an undeclared consumer.
func testPrivateService(t *testing.T, ctx context.Context, r model.Runtime, image, namespace string) {
	t.Helper()
	p := model.Project{Version: 2, Name: "provider", NamespaceImage: namespace, Source: &model.Source{Image: image}, Endpoint: &model.Endpoint{Exposure: "private", Port: 1908, Consumers: []string{"consumer"}}, Command: []string{"sh", "-c"}, Args: []string{"mkdir -p /tmp/web; echo private-agent > /tmp/web/index.html; exec httpd -f -p 1908 -h /tmp/web"}, Health: &model.Health{Exec: []string{"wget", "-qO-", "http://127.0.0.1:1908"}}}
	p, err := config.ValidateProject(p)
	// Provider access is authored in the same v2 contract as public apps.
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.Deploy(ctx, p, model.DeployContext{OperationID: "provider"})
	// Private services must return no public URL.
	if err != nil || d.URL != "" {
		t.Fatalf("provider: %+v %v", d, err)
	}
	c := model.Project{Version: 2, Name: "consumer", NamespaceImage: namespace, Source: &model.Source{Image: image}, Workload: "worker", Command: []string{"sh", "-c"}, Args: []string{`until wget -qO /tmp/result "$AGENT_URL"; do sleep 1; done; exec sleep 3600`}, Bindings: map[string]string{"AGENT_URL": "applications.provider.url"}, Health: &model.Health{Exec: []string{"grep", "-q", "private-agent", "/tmp/result"}}}
	c, err = config.ValidateProject(c)
	// Consumer requests are syntactically validated before access is evaluated.
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Deploy(ctx, c, model.DeployContext{OperationID: "consumer"})
	// A real process must resolve DNS and reach the provider's private listener.
	if err != nil {
		t.Fatal(err)
	}
	c.Name = "intruder"
	_, err = r.Deploy(ctx, c, model.DeployContext{OperationID: "intruder"})
	// Caller-authored bindings cannot grant themselves access.
	if err == nil {
		t.Fatal("unauthorized consumer reached provider")
	}
	// Remove only fixtures owned by this adapter instance.
	for _, name := range []string{"consumer", "provider"} {
		// Retention and namespace cleanup must work for nonpublic applications.
		if err = r.Action(ctx, name, "delete", model.ActionContext{Purge: true}); err != nil {
			t.Fatal(err)
		}
	}
}

// cleanupAgentBackend confines all fixture cleanup to its generated owner labels.
func cleanupAgentBackend(t *testing.T, backend, owner string) {
	t.Helper()
	// Docker fixture cleanup already verifies its exact owner filter.
	if backend == "docker" {
		cleanupDockerOwner(t, "/var/run/docker.sock", owner)
		return
	}
	t.Cleanup(func() { // Delete only namespaces created by this isolated k3s adapter.
		cmd := exec.Command("/usr/local/bin/k3s", "kubectl", "delete", "namespace", "-l", "io.hostlier.owner="+owner, "--wait=false")
		_ = cmd.Run()
	})
}

// verifyPrivateRuntime checks actual backend publication rather than desired configuration.
func verifyPrivateRuntime(t *testing.T, backend, owner string) {
	t.Helper()
	// Kubernetes Pods must not expose sidecar ports through host networking or Services.
	if backend == "k3s" {
		raw, err := exec.Command("/usr/local/bin/k3s", "kubectl", "get", "svc", "-A", "-l", "io.hostlier.owner="+owner, "-o", "json").Output()
		// Service inventory must be readable and must not include the sidecar listener.
		if err != nil || strings.Contains(string(raw), `"port": 1907`) {
			t.Fatalf("sidecar service exposed: %s %v", raw, err)
		}
		return
	}
	raw, err := exec.Command("docker", "ps", "--filter", "label=io.hostlier.owner="+owner, "--format", "{{.Ports}}").Output()
	// No member is allowed to publish the sidecar's private loopback port.
	if err != nil || strings.Contains(string(raw), "1907->") {
		t.Fatalf("sidecar port published: %s %v", raw, err)
	}
}

// agentHTTPSGetter waits for the real Kubernetes Service endpoint controller after cutover.
func agentHTTPSGetter(get func(string) (int, string)) func(string) (int, string) {
	return func(address string) (int, string) { // Retry only transient service propagation, preserving the final response.
		status, body := get(address)
		// Kubernetes readiness can precede EndpointSlice publication briefly.
		for i := 0; i < 30 && status >= 500; i++ {
			time.Sleep(200 * time.Millisecond)
			status, body = get(address)
		}
		return status, body
	}
}

// testAgentRollback preserves prior traffic when a required candidate sidecar fails.
func testAgentRollback(t *testing.T, ctx context.Context, r model.Runtime, image, namespace string, dc model.DeployContext, get func(string) (int, string)) {
	t.Helper()
	var err error
	var status int
	var body string
	// A failed required sidecar must preserve the previous active group.
	bad := agentGroupProject(image, namespace)
	member := bad.Sidecars["assistant"]
	member.Health = &model.Health{Exec: []string{"false"}, TimeoutSeconds: 2}
	bad.Sidecars["assistant"] = member
	bad, err = config.ValidateProject(bad)
	// The failure case must be a valid deployment with failing runtime readiness.
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Deploy(ctx, bad, model.DeployContext{OperationID: "group-bad", Secrets: dc.Secrets})
	// Never activate a group whose required sidecar cannot become ready.
	if err == nil {
		t.Fatal("unready sidecar activated")
	}
	status, body = get("https://agent.hostlier.test/")
	// Readiness rollback must preserve the prior application response.
	if status != 200 || !strings.Contains(body, "sidecar-v1") {
		t.Fatalf("rollback response %d: %s", status, body)
	}
}

// agentFixture starts real isolated ingress and resolves immutable images for either backend.
func agentFixture(t *testing.T) (context.Context, model.Runtime, model.ImageInfo, model.ImageInfo, func(string) (int, string), string, string, string) {
	t.Helper()
	backend := os.Getenv("HOSTLIER_AGENT_E2E")
	// Live infrastructure is never changed by the ordinary unit test suite.
	if backend != "docker" && backend != "k3s" {
		t.Skip("set HOSTLIER_AGENT_E2E=docker or k3s in a disposable Linux server")
	}
	binary := os.Getenv("HOSTLIER_CADDY_BINARY")
	// The fixture runs an isolated real Caddy instance rather than mocking ingress.
	if binary == "" {
		t.Fatal("HOSTLIER_CADDY_BINARY is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	dir := t.TempDir()
	sum := sha256.Sum256([]byte(dir))
	owner := hex.EncodeToString(sum[:])[:16]
	admin, port, pool := startCaddyFixture(t, binary, dir)
	rawGet := fixtureHTTPSGetter(t, port, pool)
	get := agentHTTPSGetter(rawGet)
	r, err := hostruntime.New(hostruntime.Options{Type: backend, StateDir: dir, CaddyAdmin: admin, SkipPublicHTTPSProbe: true, HealthTimeout: 30 * time.Second})
	// Require the actual selected backend to be configured successfully.
	if err != nil {
		t.Fatal(err)
	}
	cleanupAgentBackend(t, backend, owner)
	image, err := r.InspectImage(ctx, "busybox:1.37.0")
	// A real immutable registry image is required for both runtimes.
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := r.InspectImage(ctx, "registry.k8s.io/pause:3.10")
	// Docker's network holder also has a pinned revision identity.
	if err != nil {
		t.Fatal(err)
	}
	return ctx, r, image, namespace, get, backend, owner, dir
}
