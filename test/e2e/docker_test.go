// Real, opt-in Docker and Caddy integration. No mocked runtime services.
package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hostlier/internal/model"
	hostruntime "hostlier/internal/runtime"
)

// availablePort reserves an available loopback port for an isolated fixture.
func availablePort(t *testing.T) int {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	// Require the explicitly selected local fixture listener to start.
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// fixtureTLS creates a private test CA and wildcard certificate for verified HTTPS.
func fixtureTLS(t *testing.T, dir string) (string, string, *x509.CertPool) {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	// Require fixture key generation before creating its certificate.
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Hostlier isolated E2E CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	// Require certificate signing before installing fixture TLS files.
	if e != nil {
		t.Fatal(e)
	}
	leafKey, e := rsa.GenerateKey(rand.Reader, 2048)
	// Require fixture key generation before creating its certificate.
	if e != nil {
		t.Fatal(e)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "*.hostlier.test"}, DNSNames: []string{"*.hostlier.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	// Require certificate signing before installing fixture TLS files.
	if e != nil {
		t.Fatal(e)
	}
	certPath, keyPath := filepath.Join(dir, "tls.pem"), filepath.Join(dir, "tls-key.pem")
	// Require the protected fixture file to be written successfully.
	if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	// Require the protected fixture file to be written successfully.
	if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)}), 0600); e != nil {
		t.Fatal(e)
	}
	// Persist the isolated CA so multi-process restart fixtures retain certificate validation.
	if e = os.WriteFile(filepath.Join(dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	return certPath, keyPath, pool
}

// TestDockerCaddyLifecycle checks real Docker lifecycle, verified HTTPS routing and persistent volumes.
func TestDockerCaddyLifecycle(t *testing.T) {
	// Require explicit opt-in before running disposable live infrastructure tests.
	if os.Getenv("HOSTLIER_DOCKER_E2E") != "1" {
		t.Skip("set HOSTLIER_DOCKER_E2E=1 for real Docker/Caddy test")
	}
	binary := os.Getenv("HOSTLIER_CADDY_BINARY")
	// Fail the fixture on a missing explicitly selected trusted Caddy binary.
	if binary == "" {
		t.Fatal("HOSTLIER_CADDY_BINARY must select a trusted local Caddy executable")
	}
	socket := os.Getenv("HOSTLIER_DOCKER_SOCKET")
	// Use the Docker Unix socket default for the opt-in fixture.
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	hash := sha256.Sum256([]byte(dir))
	owner := hex.EncodeToString(hash[:])[:16]
	docker := dockerCommand(t, ctx, socket)
	cleanupDockerOwner(t, socket, owner)
	admin, httpsPort, pool := startCaddyFixture(t, binary, dir)
	get := fixtureHTTPSGetter(t, httpsPort, pool)
	adapter, e := hostruntime.New(hostruntime.Options{StateDir: dir, SocketPath: socket, CaddyAdmin: admin, SkipPublicHTTPSProbe: true, HealthTimeout: 3 * time.Second})
	// Require the selected resource backend to initialize successfully.
	if e != nil {
		t.Fatal(e)
	}
	image := inspectDockerFixtureImage(t, ctx, adapter, docker, dir)
	spec := model.Project{Version: 1, Name: "app", Image: image.Digest, Port: 80, Route: "app.hostlier.test", Resources: &model.Limits{CPU: "0.5", Memory: "64Mi"}, Volumes: map[string]model.Volume{"data": {Mount: "/usr/share/nginx/html", Size: "1Gi"}}, Health: &model.Health{Path: "/"}}
	deployed, e := adapter.Deploy(ctx, spec, model.DeployContext{OperationID: "first"})
	// Fail the fixture on deploy.
	if e != nil {
		logs, _ := os.ReadFile(filepath.Join(dir, "caddy.log"))
		t.Fatalf("deploy: %v caddy:%s", e, logs)
	}
	status, body := get(deployed.URL)
	// Fail the fixture on an unexpected HTTPS route status or response body.
	if status != 200 || !strings.Contains(body, "nginx") {
		t.Fatalf("unexpected HTTPS route %d %s", status, body)
	}
	checkDockerDeploymentIsolation(t, docker, deployed.RuntimeID)
	checkDockerLifecyclePersistence(t, ctx, adapter, spec, deployed, docker, get)
	checkDockerPathMount(t, ctx, adapter, image.Digest, get)
	checkDockerVolumePurge(t, ctx, adapter, docker, owner)
}

// dockerCommand binds fixture Docker commands to the selected socket and deadline.
func dockerCommand(t *testing.T, ctx context.Context, socket string) func(...string) string {
	// Run Docker commands against the fixture socket under the shared test deadline.
	return func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...)
		out, e := command.CombinedOutput()
		// Fail the fixture on an unsuccessful Docker fixture command.
		if e != nil {
			t.Fatalf("docker %s failed: %v %s", args[0], e, out)
		}
		return strings.TrimSpace(string(out))
	}
}

// cleanupDockerOwner removes containers, volumes and networks for this fixture owner.
func cleanupDockerOwner(t *testing.T, socket, owner string) {
	// Cleanup is scoped to this unique state-directory owner, including failed candidates.
	t.Cleanup(func() {
		cleanup, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		// Run best-effort Docker cleanup commands under the independent shutdown deadline.
		run := func(args ...string) string {
			out, _ := exec.CommandContext(cleanup, "docker", append([]string{"--host", "unix://" + socket}, args...)...).Output()
			return strings.TrimSpace(string(out))
		}
		// Apply the same contract to each supported engine or owned object kind.
		for _, kind := range []string{"container", "volume", "network"} {
			ids := strings.Fields(run(kind, "ls", "-q", "--filter", "label=io.hostlier.owner="+owner))
			// Run owner-scoped deletion only when matching fixture objects exist.
			if len(ids) > 0 {
				args := []string{kind, "rm"}
				// Force removal of fixture containers that may still be running.
				if kind == "container" {
					args = append(args, "--force")
				}
				run(append(args, ids...)...)
			}
		}
	})
}

// startCaddyFixture starts an isolated TLS Caddy instance and waits for its admin endpoint.
func startCaddyFixture(t *testing.T, binary, dir string) (string, int, *x509.CertPool) {
	t.Helper()
	cert, key, pool := fixtureTLS(t, dir)
	adminPort, httpsPort := availablePort(t), availablePort(t)
	admin := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	configuration := map[string]any{"admin": map[string]any{"listen": fmt.Sprintf("127.0.0.1:%d", adminPort)}, "apps": map[string]any{"tls": map[string]any{"certificates": map[string]any{"load_files": []any{map[string]string{"certificate": cert, "key": key}}}}, "http": map[string]any{"servers": map[string]any{"hostlier": map[string]any{"listen": []string{fmt.Sprintf("127.0.0.1:%d", httpsPort)}, "tls_connection_policies": []any{map[string]any{}}, "automatic_https": map[string]any{"disable": true}, "routes": []any{}}}}}}
	data, _ := json.Marshal(configuration)
	configPath := filepath.Join(dir, "caddy.json")
	os.WriteFile(configPath, data, 0600)
	logfile, e := os.Create(filepath.Join(dir, "caddy.log"))
	// Require the isolated Caddy log file to open before starting its process.
	if e != nil {
		t.Fatal(e)
	}
	defer logfile.Close()
	command := exec.Command(binary, "run", "--config", configPath)
	command.Env = append(os.Environ(), "XDG_DATA_HOME="+filepath.Join(dir, "caddy-data"), "XDG_CONFIG_HOME="+filepath.Join(dir, "caddy-config"))
	command.Stdout = logfile
	command.Stderr = logfile
	// Require the isolated Caddy process to start before probing its admin endpoint.
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	// Observe Caddy exit asynchronously so cleanup can bound shutdown.
	go func() { done <- command.Wait() }()
	// Interrupt Caddy and bound graceful process shutdown before forcing termination.
	t.Cleanup(func() {
		command.Process.Signal(os.Interrupt)
		// Wait for completion or the cancellation and shutdown deadline.
		select {
		// Continue once the fixture process has exited.
		case <-done:
		// Force termination when graceful process shutdown exceeds the deadline.
		case <-time.After(3 * time.Second):
			command.Process.Kill()
		}
	})
	// Poll the isolated Caddy admin endpoint until it responds or its startup deadline expires.
	for deadline := time.Now().Add(10 * time.Second); ; {
		res, e := http.Get(admin + "/config/")
		// Inspect Caddy readiness only after its admin request returns a response.
		if e == nil {
			res.Body.Close()
			// Proceed once the isolated Caddy admin configuration is reachable.
			if res.StatusCode == 200 {
				break
			}
		}
		// Fail the fixture on Caddy not ready.
		if time.Now().After(deadline) {
			logs, _ := os.ReadFile(filepath.Join(dir, "caddy.log"))
			t.Fatalf("Caddy not ready: %s", logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return admin, httpsPort, pool
}

// fixtureHTTPSGetter routes verified fixture hostnames to the isolated Caddy listener.
func fixtureHTTPSGetter(t *testing.T, httpsPort int, pool *x509.CertPool) func(string) (int, string) {
	// Connect only to the explicitly selected private fixture endpoint.
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", httpsPort))
		// Expose redirect responses so the fixture can assert mount behavior.
	}}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	// Read route status and body through the fixture TLS client without following redirects.
	return func(target string) (int, string) {
		t.Helper()
		res, e := client.Get(target)
		// Require the fixture HTTPS request to complete before reading its response.
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}
}

// checkDockerDeploymentIsolation checks CPU, memory and loopback-only port bindings.
func checkDockerDeploymentIsolation(t *testing.T, docker func(...string) string, id string) {
	t.Helper()
	var e error
	var inspect []struct {
		HostConfig struct {
			NanoCpus     int64
			Memory       int64
			PortBindings map[string][]struct {
				HostIP string `json:"HostIp"`
			}
		}
	}
	// Reject malformed engine or CLI metadata before checking its fields.
	if e = json.Unmarshal([]byte(docker("inspect", id)), &inspect); e != nil {
		t.Fatal(e)
	}
	// Fail the fixture on resource constraints not applied.
	if len(inspect) != 1 || inspect[0].HostConfig.NanoCpus != 500000000 || inspect[0].HostConfig.Memory != 67108864 {
		t.Fatal("resource constraints not applied")
	}
	// Inspect every workload binding to ensure only loopback addresses are published.
	for _, bindings := range inspect[0].HostConfig.PortBindings {
		// Inspect every workload binding to ensure only loopback addresses are published.
		for _, binding := range bindings {
			// Fail the fixture on public workload port.
			if binding.HostIP != "127.0.0.1" {
				t.Fatal("public workload port")
			}
		}
	}
}

// checkDockerLifecyclePersistence checks durable updates and preserves a healthy route after failure.
func checkDockerLifecyclePersistence(t *testing.T, ctx context.Context, adapter model.Runtime, spec model.Project, deployed model.Deployment, docker func(...string) string, get func(string) (int, string)) {
	t.Helper()
	var e error
	var body string
	var status int
	docker("exec", deployed.RuntimeID, "sh", "-c", "printf 'hostlier-persistent-data' > /usr/share/nginx/html/index.html")
	// Exercise each supported lifecycle transition before checking durable data.
	for _, action := range []string{"stop", "start", "restart"} {
		// Fail the fixture on an unsuccessful lifecycle action.
		if e = adapter.Action(ctx, "app", action, model.ActionContext{}); e != nil {
			t.Fatalf("%s: %v", action, e)
		}
	}
	_, body = get(deployed.URL)
	// Fail the fixture on data lost after lifecycle.
	if body != "hostlier-persistent-data" {
		t.Fatal("data lost after lifecycle")
	}
	updated, e := adapter.Deploy(ctx, spec, model.DeployContext{OperationID: "update"})
	// Require the requested fixture deployment to succeed.
	if e != nil {
		t.Fatal(e)
	}
	_, body = get(updated.URL)
	// Fail the fixture on data lost after update.
	if body != "hostlier-persistent-data" {
		t.Fatal("data lost after update")
	}
	failed := spec
	failed.Health = &model.Health{Path: "/missing-health-endpoint", TimeoutSeconds: 1}
	// Fail the fixture on unhealthy update succeeded.
	if _, e = adapter.Deploy(ctx, failed, model.DeployContext{OperationID: "unhealthy"}); e == nil {
		t.Fatal("unhealthy update succeeded")
	}
	status, body = get(updated.URL)
	// Fail the fixture on failed update displaced healthy route.
	if status != 200 || body != "hostlier-persistent-data" {
		t.Fatalf("failed update displaced healthy route: %d %s", status, body)
	}
}

// checkDockerPathMount checks mounted route redirect, prefix stripping and sibling exclusion.
func checkDockerPathMount(t *testing.T, ctx context.Context, adapter model.Runtime, image string, get func(string) (int, string)) {
	t.Helper()
	var e error
	var status int
	var body string
	path := model.Project{Version: 1, Name: "path", Image: image, Port: 80, Route: "path.hostlier.test/reports", Proxy: &model.Proxy{StripPrefix: true}, Health: &model.Health{Path: "/"}}
	// Require the requested fixture deployment to succeed.
	if _, e = adapter.Deploy(ctx, path, model.DeployContext{OperationID: "path"}); e != nil {
		t.Fatal(e)
	}
	// Fail the fixture on mount redirect.
	if status, _ = get("https://path.hostlier.test/reports"); status != 308 {
		t.Fatalf("mount redirect: %d", status)
	}
	// Fail the fixture on stripped path failed.
	if status, body = get("https://path.hostlier.test/reports/"); status != 200 || !strings.Contains(body, "nginx") {
		t.Fatal("stripped path failed")
	}
	// Fail the fixture on path matcher captured sibling.
	if _, body = get("https://path.hostlier.test/reportss"); strings.Contains(body, "nginx") {
		t.Fatal("path matcher captured sibling")
	}
}

// checkDockerVolumePurge checks that ordinary deletion retains data until explicit purge.
func checkDockerVolumePurge(t *testing.T, ctx context.Context, adapter model.Runtime, docker func(...string) string, owner string) {
	t.Helper()
	var e error
	volumesBefore := docker("volume", "ls", "-q", "--filter", "label=io.hostlier.owner="+owner)
	// Fail the fixture on no persistent volume.
	if volumesBefore == "" {
		t.Fatal("no persistent volume")
	}
	// Require the requested fixture lifecycle transition to succeed.
	if e = adapter.Action(ctx, "app", "delete", model.ActionContext{}); e != nil {
		t.Fatal(e)
	}
	// Fail the fixture on normal deletion removed data.
	if docker("volume", "ls", "-q", "--filter", "label=io.hostlier.owner="+owner) != volumesBefore {
		t.Fatal("normal deletion removed data")
	}
	// Require the requested fixture lifecycle transition to succeed.
	if e = adapter.Action(ctx, "app", "delete", model.ActionContext{Purge: true}); e != nil {
		t.Fatal(e)
	}
	// Fail the fixture on explicit purge retained app volume.
	if docker("volume", "ls", "-q", "--filter", "label=io.hostlier.owner="+owner) != "" {
		t.Fatal("explicit purge retained app volume")
	}
	// Require the requested fixture lifecycle transition to succeed.
	if e = adapter.Action(ctx, "path", "delete", model.ActionContext{Purge: true}); e != nil {
		t.Fatal(e)
	}
}

// inspectDockerFixtureImage verifies immutable image planning does not mutate runtime state.
func inspectDockerFixtureImage(t *testing.T, ctx context.Context, adapter model.Runtime, docker func(...string) string, dir string) model.ImageInfo {
	t.Helper()
	before := docker("container", "ls", "-aq")
	image, e := adapter.InspectImage(ctx, "nginx:1.28-alpine")
	// Require immutable image resolution before deploying the fixture.
	if e != nil {
		t.Fatal(e)
	}
	after := docker("container", "ls", "-aq")
	// Fail the fixture on read-only image planning changed containers.
	if before != after {
		t.Fatal("read-only image planning changed containers")
	}
	// Fail the fixture on image inspection wrote runtime state.
	if _, e = os.Stat(filepath.Join(dir, "runtime-docker.json")); !os.IsNotExist(e) {
		t.Fatal("image inspection wrote runtime state")
	}
	t.Log("immutable test image", image.Digest)
	return image
}
