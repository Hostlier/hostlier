package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"hostlier/internal/fleet"
	"hostlier/internal/model"
	runtimes "hostlier/internal/runtime"
	"hostlier/internal/telemetry"
)

// collectorLiveRuntime keeps real provisioning while admitting only the explicitly created log fixture.
type collectorLiveRuntime struct {
	model.Runtime
	mu       sync.Mutex
	instance string
	spec     model.CollectorDeployment
}

// EnsureCollector forwards every mutation to the actual Docker or Kubernetes adapter.
func (r *collectorLiveRuntime) EnsureCollector(ctx context.Context, s model.CollectorDeployment) error {
	err := r.Runtime.(model.ManagedCollectorRuntime).EnsureCollector(ctx, s)
	// Retain a private test copy only after the runtime confirms a ready singleton.
	if err == nil {
		r.mu.Lock()
		r.spec = s
		r.mu.Unlock()
	}
	return err
}

// ResolveTelemetry recognizes only the exact fixture ID created by this test, never supplied labels.
func (r *collectorLiveRuntime) ResolveTelemetry(_ context.Context, attrs map[string]string) (model.TelemetryIdentity, error) {
	// Native collector coordinates must match the known Docker container or Kubernetes Pod.
	if attrs["container.id"] != r.instance && attrs["k8s.pod.uid"] != r.instance {
		return model.TelemetryIdentity{}, fmt.Errorf("not fixture")
	}
	return model.TelemetryIdentity{Application: "sample", Revision: "fixture", Component: attrs["k8s.container.name"], Instance: r.instance}, nil
}

// TestManagedCollectorLive exercises actual automatic startup, authenticated pulls, repair and ingestion.
func TestManagedCollectorLive(t *testing.T) {
	backend, assets, host := liveCollectorInputs(t)
	state := t.TempDir()
	liveCommand(t, "cp", "-R", assets, filepath.Join(state, "collector-image"))
	endpoint, ca, listen := liveCollectorTransport(t, backend, host, state)
	liveCollectorCleanup(t, backend, state)
	adapter, err := runtimes.New(runtimes.Options{Type: backend, StateDir: state, HealthTimeout: 30 * time.Second})
	// Invalid runtime setup is a fixture error, never a skipped passing test.
	if err != nil {
		t.Fatal(err)
	}
	runtime := &collectorLiveRuntime{Runtime: adapter, instance: liveSource(t, backend)}
	c, err := New(Options{ManagedCollector: true, CollectorInterval: time.Second, CollectorEndpoint: endpoint, CollectorCAFile: ca, StateDir: state, Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: runtime, StatusInterval: time.Second})
	// The real controller owns token issuance, registry auth and background reconciliation.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) }) // Stop reconciliation before removing owned runtime fixtures.
	c.mu.Lock()
	fixtureSpec := project()
	fixtureSpec.Name = "sample"
	c.state.Applications["sample"] = &Application{Deployment: model.Deployment{Name: "sample", RuntimeID: "fixture"}, Revision: "fixture-revision", Spec: fixtureSpec}
	c.mu.Unlock()
	// Bind the production listener with a trusted test certificate and no public-DNS dependency.
	if err = c.Listen(listen); err != nil {
		t.Fatal(err)
	}
	// Require real observations from both signal pipelines.
	waitLive(t, "collector metrics and logs", func() bool { return liveHasHistory(c, "metrics") && liveHasHistory(c, "logs") })
	// Wait until successful provisioning has published its final spec.
	waitLive(t, "managed collector readiness", func() bool { return liveManagedReady(c) })
	before := liveCollectorID(t, backend)
	runtime.mu.Lock()
	spec := runtime.spec
	runtime.mu.Unlock()
	// Repeated provisioning must reuse the existing running process.
	if err = runtime.EnsureCollector(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	// A stable specification must not restart or duplicate the collector.
	if after := liveCollectorID(t, backend); after != before {
		t.Fatal("unchanged reconcile replaced collector")
	}
	liveCollectorIsolation(t, spec)
	c = liveRestartController(t, c, backend, listen)
	before = liveCollectorID(t, backend)
	liveRemoveCollector(t, backend)
	// A replacement must have a new immutable runtime identity.
	waitLive(t, "automatic collector repair", func() bool { id := liveCollectorMaybeID(backend); return id != "" && id != before })
	// Controller status must converge after the runtime replacement.
	waitLive(t, "ready after automatic repair", func() bool { return liveManagedReady(c) })
	t.Logf("%s automatic registry pull, metrics/log ingestion, idempotence and deletion repair passed", backend)
}

// liveCommand executes bounded fixture operations without shell interpolation.
func liveCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	// Commands print only fixture identities and ordinary Kubernetes status, never token Secrets.
	if err != nil {
		t.Fatalf("%s fixture command: %v %s", name, err, raw)
	}
	return strings.TrimSpace(string(raw))
}

// livePort reserves an unused listener port for the short controller setup sequence.
func livePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	// Fixture bind failures must abort before installing registry trust.
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// liveCertificate creates a short-lived private CA used only by this fixture registry and exporter.
func liveCertificate(t *testing.T, host string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// Cryptographic setup must succeed before any listener is advertised.
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "Hostlier collector fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP(host)}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	// A malformed certificate cannot be worked around with disabled verification.
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, _ := x509.MarshalECPrivateKey(key)
	certificate, err := tls.X509KeyPair(raw, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	// Require the server keypair to match its trusted public certificate.
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	// The fixture CA is public material, but its containing temporary directory remains private.
	if err = os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	return certificate, path
}

// liveRegistryTrust installs scoped test-registry trust without changing any existing registry entry.
func liveRegistryTrust(t *testing.T, backend, host, ca string) {
	t.Helper()
	root := "/etc/docker/certs.d"
	// k3s containerd consults its generated registry hosts directory on each pull.
	if backend == "k3s" {
		root = "/var/lib/rancher/k3s/agent/etc/containerd/certs.d"
	}
	directory := filepath.Join(root, host)
	// Exclusive creation prevents a fixture from replacing existing administrator trust.
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	// Never adopt an existing registry trust directory, even for a random port.
	if err := os.Mkdir(directory, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) }) // Remove only the directory exclusively created by this fixture.
	liveCommand(t, "cp", ca, filepath.Join(directory, "ca.crt"))
	// Containerd needs an explicit host entry and uses the same verified CA as the exporter.
	if backend == "k3s" {
		body := fmt.Sprintf("server = %q\n[host.%q]\ncapabilities = [\"pull\", \"resolve\"]\nca = %q\n", "https://"+host, "https://"+host, filepath.Join(directory, "ca.crt"))
		// This affects only the ephemeral registry origin for the live test.
		if err := os.WriteFile(filepath.Join(directory, "hosts.toml"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// liveSource creates one listenerless fixture whose metrics and logs exercise the real collector inputs.
func liveSource(t *testing.T, backend string) string {
	t.Helper()
	// Docker discovery must include workers with no published or exposed ports.
	if backend == "docker" {
		cidfile := filepath.Join(t.TempDir(), "source.cid")
		liveCommand(t, "docker", "run", "-d", "--cidfile", cidfile, "--label", "io.hostlier.managed=true", "rancher/mirrored-library-busybox:1.37.0", "sh", "-c", "while true; do echo HOSTLIER_AUTOMATIC_COLLECTOR_FIXTURE; sleep 1; done")
		raw, err := os.ReadFile(cidfile)
		// Pull progress belongs to stderr and must not contaminate the immutable container identity.
		if err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(string(raw))
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() }) // Remove only the exact container created above.
		return id
	}
	name := "hostlier-collector-source-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	liveCommand(t, "k3s", "kubectl", "create", "namespace", name)
	t.Cleanup(func() { exec.Command("k3s", "kubectl", "delete", "namespace", name, "--wait=false").Run() }) // This namespace belongs solely to this fixture.
	liveCommand(t, "k3s", "kubectl", "-n", name, "run", "worker", "--image=rancher/mirrored-library-busybox:1.37.0", "--restart=Never", "--command", "--", "sh", "-c", "while true; do echo HOSTLIER_AUTOMATIC_COLLECTOR_FIXTURE; sleep 1; done")
	liveCommand(t, "k3s", "kubectl", "-n", name, "wait", "--for=condition=Ready", "pod/worker", "--timeout=60s")
	return liveCommand(t, "k3s", "kubectl", "-n", name, "get", "pod", "worker", "-o", "jsonpath={.metadata.uid}")
}

// liveCollectorCleanup guards preexisting singleton infrastructure and removes only the fixture owner.
func liveCollectorCleanup(t *testing.T, backend, state string) {
	t.Helper()
	// Tests must not replace an already installed collector.
	if backend == "docker" {
		// Refuse an existing daemon-wide singleton before any fixture adoption.
		if exec.Command("docker", "inspect", "hostlier-collector").Run() == nil {
			t.Fatal("collector already exists; refusing live fixture")
		}
	} else if exec.Command("k3s", "kubectl", "get", "namespace", "hostlier-observability").Run() == nil { // A cluster-wide singleton must also be absent before this isolated test.
		t.Fatal("collector namespace already exists; refusing live fixture")
	}
	hash := sha256.Sum256([]byte(state))
	owner := hex.EncodeToString(hash[:])[:16]
	t.Cleanup(func() { // Ownership must still match before fixture cleanup can remove infrastructure.
		// Cleanup uses the same native identity boundary as provisioning.
		if backend == "docker" {
			raw, _ := exec.Command("docker", "inspect", "--format", `{{index .Config.Labels "io.hostlier.owner"}}`, "hostlier-collector").Output()
			// Never remove a replacement owned by another controller.
			if strings.TrimSpace(string(raw)) == owner {
				exec.Command("docker", "rm", "-f", "hostlier-collector").Run()
			} // Never remove a foreign replacement.
			return
		}
		raw, _ := exec.Command("k3s", "kubectl", "get", "namespace", "hostlier-observability", "-o", "jsonpath={.metadata.labels.io\\.hostlier\\.owner}").Output()
		if strings.TrimSpace(string(raw)) == owner { // All names are fixed but their runtime owner must match this test.
			exec.Command("k3s", "kubectl", "delete", "namespace", "hostlier-observability", "--wait=true", "--timeout=60s").Run()
			exec.Command("k3s", "kubectl", "delete", "clusterrole,clusterrolebinding", "hostlier-observability-collector").Run()
		}
	})
}

// liveCollectorMaybeID identifies the current singleton without failing while it is being repaired.
func liveCollectorMaybeID(backend string) string {
	var command *exec.Cmd
	// Docker exposes an immutable container ID while Kubernetes exposes the collector Pod UID.
	if backend == "docker" {
		command = exec.Command("docker", "inspect", "--format", "{{.Id}}", "hostlier-collector")
	} else {
		command = exec.Command("k3s", "kubectl", "-n", "hostlier-observability", "get", "pods", "-l", "app.kubernetes.io/name=hostlier-collector", "-o", "jsonpath={.items[0].metadata.uid}")
	}
	raw, _ := command.Output()
	return strings.TrimSpace(string(raw))
}

// liveCollectorID requires an existing singleton at a verified-ready checkpoint.
func liveCollectorID(t *testing.T, backend string) string {
	t.Helper()
	id := liveCollectorMaybeID(backend)
	// Empty inventory at a readiness checkpoint is a real provisioning failure.
	if id == "" {
		t.Fatal("collector missing after ready")
	}
	return id
}

// liveRemoveCollector simulates loss of the system process while preserving its desired configuration.
func liveRemoveCollector(t *testing.T, backend string) {
	t.Helper()
	// Runtime supervisors should recreate exactly one replacement after this deletion.
	if backend == "docker" {
		liveCommand(t, "docker", "rm", "-f", "hostlier-collector")
		return
	}
	liveCommand(t, "k3s", "kubectl", "-n", "hostlier-observability", "delete", "pods", "-l", "app.kubernetes.io/name=hostlier-collector", "--wait=false")
}

// liveHasHistory confirms the actual collector exported observations through authenticated controller ingest.
func liveHasHistory(c *Controller, signal string) bool {
	store, err := c.telemetryStore()
	// Pending startup can legitimately have no history database yet.
	if err != nil {
		return false
	}
	page, err := store.Query(context.Background(), telemetry.Query{Signal: signal, Application: "sample", Limit: 1})
	return err == nil && len(page.Records) > 0
}

// liveManagedReady checks the public health contract rather than an internal goroutine state.
func liveManagedReady(c *Controller) bool {
	raw, _ := json.Marshal(c.telemetryHealth())
	var value struct {
		Managed *struct {
			State string `json:"state"`
		} `json:"managed"`
	}
	json.Unmarshal(raw, &value)
	return value.Managed != nil && value.Managed.State == "ready"
}

// waitLive bounds retries while preserving a useful failure location for each integration checkpoint.
func waitLive(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Second)
	// Image pull, collector polling and export batching are asynchronous runtime operations.
	for time.Now().Before(deadline) {
		// A positive observation completes this checkpoint immediately.
		if ready() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("timed out waiting for", description)
}

// liveCollectorInputs requires explicit opt-in before touching disposable runtime infrastructure.
func liveCollectorInputs(t *testing.T) (string, string, string) {
	t.Helper()
	backend := os.Getenv("HOSTLIER_MANAGED_COLLECTOR_E2E")
	// Default unit-test invocations never mutate container daemons or trust stores.
	if backend == "" {
		t.Skip("set HOSTLIER_MANAGED_COLLECTOR_E2E=docker|k3s in a disposable Linux VM")
	}
	// Only the tested Linux runtime backends may perform this privileged fixture.
	if (backend != "docker" && backend != "k3s") || os.Geteuid() != 0 {
		t.Fatal("live collector fixture requires root Linux Docker or k3s")
	}
	assets := os.Getenv("HOSTLIER_COLLECTOR_IMAGE")
	// The production release assets must be supplied explicitly rather than inferred from host state.
	if assets == "" {
		t.Fatal("HOSTLIER_COLLECTOR_IMAGE is required")
	}
	host := os.Getenv("HOSTLIER_COLLECTOR_TEST_IP")
	// Container-reachable addressing is mandatory; container loopback cannot reach the controller.
	if net.ParseIP(host) == nil {
		t.Fatal("HOSTLIER_COLLECTOR_TEST_IP must be a VM address reachable by containers")
	}
	return backend, assets, host
}

// liveRestartController proves persisted managed credentials reuse the singleton after process restart.
func liveRestartController(t *testing.T, previous *Controller, backend string, listen ListenOptions) *Controller {
	t.Helper()
	before := liveCollectorID(t, backend)
	options := previous.opts
	// Close the original listeners and workers before reopening the same durable authority.
	if err := previous.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	next, err := New(options)
	// Reopen exactly the existing state and runtime rather than creating a new owner.
	if err != nil {
		t.Fatal(err)
	}
	// The same origin must continue serving registry pulls and telemetry after restart.
	if err = next.Listen(listen); err != nil {
		next.Close(context.Background())
		t.Fatal(err)
	}
	// Managed readiness must be recomputed by the restarted controller's real worker.
	waitLive(t, "managed collector after controller restart", func() bool { return liveManagedReady(next) })
	// Valid persisted credentials must not force an unnecessary collector rollout.
	if liveCollectorID(t, backend) != before {
		t.Fatal("controller restart replaced healthy collector")
	}
	return next
}

// liveCollectorTransport selects ordinary HTTPS or automatic fleet collector mutual TLS.
func liveCollectorTransport(t *testing.T, backend, host, state string) (string, string, ListenOptions) {
	t.Helper()
	port := livePort(t)
	listen := ListenOptions{TCP: true, Host: "0.0.0.0", Port: port}
	// Fleet mode deliberately provides no external CA setup or certificate mounts.
	if os.Getenv("HOSTLIER_MANAGED_COLLECTOR_FLEET") == "1" {
		identity, err := fleet.Generate()
		// A distinct management authority must never be shared with the collector.
		if err != nil {
			t.Fatal(err)
		}
		listen.ControllerFingerprint, err = fleet.Fingerprint(identity.Certificate)
		// Invalid fixture identity is a setup failure.
		if err != nil {
			t.Fatal(err)
		}
		port = livePort(t)
		origin := net.JoinHostPort(host, strconv.Itoa(port))
		liveManagedTrustCleanup(t, backend, origin, state)
		return "https://" + origin, "", listen
	}
	cert, ca := liveCertificate(t, host)
	listen.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	liveRegistryTrust(t, backend, net.JoinHostPort(host, strconv.Itoa(port)), ca)
	return "https://" + net.JoinHostPort(host, strconv.Itoa(port)), ca, listen
}

// liveCollectorIsolation verifies the real collector identity cannot enter management routes.
func liveCollectorIsolation(t *testing.T, spec model.CollectorDeployment) {
	t.Helper()
	// Ordinary HTTPS has no dedicated mutual-TLS identity to exercise.
	if spec.TLS == nil {
		return
	}
	roots := x509.NewCertPool()
	// The controller must have supplied a usable dedicated trust bundle.
	if !roots.AppendCertsFromPEM([]byte(spec.TLS.CA)) {
		t.Fatal("missing collector CA")
	}
	cert, err := tls.X509KeyPair([]byte(spec.TLS.Certificate), []byte(spec.TLS.PrivateKey))
	// Provisioned client material must parse exactly as mounted by the runtime.
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, _ := http.NewRequest("GET", spec.Endpoint+"/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+spec.Token)
	response, err := client.Do(request)
	// A valid collector identity must receive an explicit route denial, never management status.
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	// The transport must not delegate normal controller authority to collector certificates.
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("collector reached management route: %d", response.StatusCode)
	}
}

// liveManagedTrustCleanup removes only trust installed under this fixture's runtime ownership.
func liveManagedTrustCleanup(t *testing.T, backend, origin, state string) {
	t.Helper()
	root := "/etc/docker/certs.d"
	// k3s bootstraps containerd trust on each node before pulling the collector image.
	if backend == "k3s" {
		root = "/var/lib/rancher/k3s/agent/etc/containerd/certs.d"
	}
	directory := filepath.Join(root, origin)
	sum := sha256.Sum256([]byte(state))
	owner := hex.EncodeToString(sum[:])[:16]
	// A pre-existing origin belongs to an administrator or another test, even if the port is random.
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatal("registry origin already exists; refusing automatic trust fixture")
	}
	// Cleanup is limited to the origin created by this test and its exact ownership marker.
	t.Cleanup(func() {
		marker, err := os.ReadFile(filepath.Join(directory, ".hostlier-owner"))
		// Never remove registry trust that has been replaced by another owner.
		if err == nil && strings.TrimSpace(string(marker)) == owner {
			os.RemoveAll(directory)
		}
	})
}
