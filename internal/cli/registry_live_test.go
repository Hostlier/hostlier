package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"hostlier/internal/artifact"
	"hostlier/internal/client"
	"hostlier/internal/controller"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/model"
	hostruntime "hostlier/internal/runtime"
)

// liveRegistryRuntime observes the real runtime; no image, registry or deployment operation is simulated.
type liveRegistryRuntime struct {
	model.Runtime
	mu        sync.Mutex
	pulls     int
	pullError error
}

// PrepareRegistryImage invokes the actual anonymous controller registry request and retains its outcome.
func (r *liveRegistryRuntime) PrepareRegistryImage(ctx context.Context, image, dir string) (artifact.Result, error) {
	result, err := r.Runtime.(interface {
		PrepareRegistryImage(context.Context, string, string) (artifact.Result, error)
	}).PrepareRegistryImage(ctx, image, dir)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pulls++
	r.pullError = err
	return result, err
}

// liveRegistryUpload counts real protocol upload bytes without recording credentials or payload contents.
type liveRegistryUpload struct {
	mu     sync.Mutex
	chunks int
	bytes  int64
}

// observe records the decoded CLI requests before they reach the unmodified controller API.
func (u *liveRegistryUpload) observe(in *pb.CommandRequest) {
	// Ignore unrelated commands, including pairing and approval payloads.
	if !strings.Contains(in.Route, "/chunks") {
		return
	}
	var chunk struct {
		Data []byte `json:"data"`
	}
	// Invalid protocol payloads cannot count as transferred image bytes.
	if json.Unmarshal(in.JsonBody, &chunk) != nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.chunks++
	u.bytes += int64(len(chunk.Data))
}

// TestPrivateRegistryLiveFallback runs the packaged CLI, private registry, real controller, Docker and Caddy.
func TestPrivateRegistryLiveFallback(t *testing.T) {
	// Access to a real account and Docker daemon requires explicit test opt-in.
	if os.Getenv("HOSTLIER_PRIVATE_REGISTRY_E2E") != "1" {
		t.Skip("set HOSTLIER_PRIVATE_REGISTRY_E2E=1 and HOSTLIER_PRIVATE_IMAGE for the live private-registry test")
	}
	image, caddy := os.Getenv("HOSTLIER_PRIVATE_IMAGE"), os.Getenv("HOSTLIER_CADDY_BINARY")
	// Require caller-selected inputs instead of guessing a private repository or executable.
	if image == "" || caddy == "" {
		t.Fatal("HOSTLIER_PRIVATE_IMAGE and HOSTLIER_CADDY_BINARY are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	// A short private path also fits macOS's Unix-domain socket limit on mounted test volumes.
	dir, err := os.MkdirTemp("", "ghcr-")
	// All test profiles and controller state must live in this private disposable directory.
	if err != nil {
		t.Fatal(err)
	}
	// Remove temporary authority and state after the fixture processes have stopped.
	t.Cleanup(func() { os.RemoveAll(dir) })
	state := filepath.Join(dir, "state")
	socket := os.Getenv("HOSTLIER_DOCKER_SOCKET")
	// Respect an explicitly selected test daemon, otherwise use Docker's standard Unix socket.
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	liveRegistryCleanup(t, socket, state)
	admin, edge := liveRegistryCaddy(t, caddy, dir)
	// Nil DockerLogin and RegistryCredentials deliberately keep the controller anonymous.
	adapter, err := hostruntime.New(hostruntime.Options{StateDir: state, SocketPath: socket, CaddyAdmin: admin, SkipPublicHTTPSProbe: true, HealthTimeout: 60 * time.Second})
	// Runtime initialization must succeed before constructing controller authority.
	if err != nil {
		t.Fatal(err)
	}
	observed := &liveRegistryRuntime{Runtime: adapter}
	c, err := controller.New(controller.Options{StateDir: state, Config: model.ServerConfig{Version: 1, Domains: []string{"example.test"}}, Runtime: observed, Hostname: "control.example.test"})
	// Fail on real controller initialization errors rather than substituting a fake runtime.
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	upload := &liveRegistryUpload{}
	https := httptest.NewUnstartedServer(withFixtureGRPCHuman(t, c, upload.observe))
	https.EnableHTTP2 = true
	https.StartTLS()
	defer https.Close()
	harness := liveRegistryCLI(t, ctx, dir, https)
	local := filepath.Join(dir, "controller.sock")
	// Local authority issues a one-time pairing token; the CLI then enrolls over verified TLS.
	liveRegistryPair(t, c, harness, local, https.URL)
	liveRegistryProject(t, dir, image)
	parameters := []string{"-c", strings.Replace(pythonPTY, "end=time.time()+30", "end=time.time()+900", 1), "Approve this exact deployment plan?", "yes", harness.binary, "deploy"}
	command := exec.CommandContext(ctx, "python3", parameters...)
	command.Dir = dir
	command.Env = append(harness.env, "HOSTLIER_CA_FILE="+harness.ca)
	output, err := command.CombinedOutput()
	t.Logf("CLI deployment output:\n%s", output)
	// A successful upload alone cannot satisfy this gate; the actual deployment must finish.
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	liveRegistryVerify(t, harness, observed, upload, socket, state, edge, string(output))
}

// liveRegistryPair enrolls a fresh CLI profile using the production token and TLS protocol.
func liveRegistryPair(t *testing.T, c *controller.Controller, harness packagedCLI, local, url string) {
	t.Helper()
	// Bind only this fixture's protected local administration socket.
	if err := c.Listen(controller.ListenOptions{SocketPath: local}); err != nil {
		t.Fatal(err)
	}
	api, err := client.NewGRPC(client.Options{SocketPath: local})
	// Local token issuance still uses the production gRPC transport.
	if err != nil {
		t.Fatal(err)
	}
	var token struct {
		Token string `json:"token"`
	}
	// Mint a short-lived token for this one disposable client profile.
	if err := api.Request(harness.ctx, "POST", "/v1/pairing/tokens", map[string]string{"name": "private-registry-e2e"}, &token); err != nil {
		t.Fatal(err)
	}
	// Pair the built executable over verified TLS without exposing its token in argv or logs.
	if output, err := harness.run([]string{"connect", url, "--name", "private-registry-e2e", "--token-stdin"}, token.Token, true); err != nil {
		t.Fatalf("connect: %v %s", err, output)
	}
}

// liveRegistryCLI builds this working tree's executable and gives it a new isolated controller profile.
func liveRegistryCLI(t *testing.T, ctx context.Context, dir string, https *httptest.Server) packagedCLI {
	t.Helper()
	binary := filepath.Join(dir, "hostlier")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/hostlier")
	build.Dir = filepath.Join("..", "..")
	// The gate must exercise the current working tree's packaged executable.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, output)
	}
	ca := filepath.Join(dir, "ca.pem")
	// Trust only the fixture certificate for this isolated CLI profile.
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: https.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return packagedCLI{ctx: ctx, binary: binary, dir: dir, ca: ca, env: append(os.Environ(), "HOSTLIER_HOME="+filepath.Join(dir, "profiles"))}
}

// liveRegistryProject uses only the public image field, without an authored architecture or server login.
func liveRegistryProject(t *testing.T, dir, image string) {
	t.Helper()
	port := os.Getenv("HOSTLIER_PRIVATE_IMAGE_PORT")
	// Margin Central's UI is the default runnable private-image fixture.
	if port == "" {
		port = "3011"
	}
	// Reject nonnumeric YAML fragments before writing the selected port.
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}
	configuration := fmt.Sprintf("version: 1\nname: private-ghcr-e2e\nimage: %s\nport: %s\nroute: private-ghcr.example.test\nhealth:\n  path: /\n  timeoutSeconds: 60\nenv:\n  AI_SQLITE_PATH: /tmp/hostlier-e2e.db\n", strconv.Quote(image), port)
	// No production secrets, dependencies or persistent volumes participate in this deployment.
	if err := os.WriteFile(filepath.Join(dir, ".hostlier"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
}

// liveRegistryVerify checks real controller failure, uploaded bytes, imported identity and edge readiness.
func liveRegistryVerify(t *testing.T, h packagedCLI, r *liveRegistryRuntime, u *liveRegistryUpload, socket, state, edge, output string) {
	t.Helper()
	failure := liveRegistryFailure(t, r, output)
	app, receipt := liveRegistryReceipt(t, h, state)
	u.mu.Lock()
	chunks, transferred := u.chunks, u.bytes
	u.mu.Unlock()
	// A fresh controller must receive the complete archive through actual chunk requests.
	if chunks == 0 || transferred != receipt.Size {
		t.Fatalf("upload bytes=%d chunks=%d archive size=%d", transferred, chunks, receipt.Size)
	}
	liveRegistryIdentity(t, h, socket, app, receipt)
	code := liveRegistryRoute(t, h.ctx, edge)
	t.Logf("Verified controller=%s platform=linux/%s chunks=%d bytes=%d artifact=%s config=%s edgeHTTP=%d", failure, runtime.GOARCH, chunks, transferred, receipt.Image, receipt.ConfigDigest, code)
}

// liveRegistryFailure requires a real anonymous failure before the CLI switches to its local login.
func liveRegistryFailure(t *testing.T, r *liveRegistryRuntime, output string) string {
	t.Helper()
	r.mu.Lock()
	pulls, pullErr := r.pulls, r.pullError
	r.mu.Unlock()
	var failure *model.Error
	// An anonymous registry request must genuinely fail before fallback can satisfy this gate.
	if pulls != 1 || !errors.As(pullErr, &failure) || !registryDownloadFallback(failure.Code) {
		t.Fatalf("expected one real anonymous registry failure; calls=%d error=%v", pulls, pullErr)
	}
	// Receiver discovery and the local download must be visible in the packaged CLI's actual output.
	if !strings.Contains(output, "trying local credentials and upload") || !strings.Contains(output, "locally for linux/"+runtime.GOARCH) || !strings.Contains(output, "succeeded") {
		t.Fatal("CLI did not complete the automatic local fallback")
	}
	return failure.Code
}

// liveRegistryReceipt rehashes the controller's committed archive for the actual running deployment.
func liveRegistryReceipt(t *testing.T, h packagedCLI, state string) (model.Deployment, artifact.Result) {
	t.Helper()
	status, err := h.run([]string{"status", "private-ghcr-e2e"}, "", true)
	// Read the controller's resulting deployment through the packaged CLI as well.
	if err != nil {
		t.Fatalf("status: %v %s", err, status)
	}
	var app model.Deployment
	// Completed planning or upload without a running deployment is insufficient.
	if err := json.Unmarshal(status, &app); err != nil || app.Status != "running" {
		t.Fatalf("not running: %v %s", err, status)
	}
	receipt, err := (artifact.Store{Root: filepath.Join(state, "artifacts")}).Inspect(app.ImageDigest)
	// The retained artifact must pass the store's independent archive verification.
	if err != nil {
		t.Fatal(err)
	}
	// An optional independently inspected registry digest binds this run to the intended source image.
	if expected := os.Getenv("HOSTLIER_PRIVATE_IMAGE_CONFIG_DIGEST"); expected != "" && receipt.ConfigDigest != expected {
		t.Fatalf("uploaded config=%s registry config=%s", receipt.ConfigDigest, expected)
	}
	return app, receipt
}

// liveRegistryIdentity handles both classic Docker config IDs and containerd manifest IDs without weakening identity checks.
func liveRegistryIdentity(t *testing.T, h packagedCLI, socket string, app model.Deployment, receipt artifact.Result) {
	t.Helper()
	inspect := exec.CommandContext(h.ctx, "docker", "--host", "unix://"+socket, "inspect", "--format", "{{.Image}} {{.State.Running}}", app.RuntimeID)
	identity, err := inspect.CombinedOutput()
	// Docker's containerd store identifies the image manifest rather than its config in container inspect.
	installed, inspectErr := exec.CommandContext(h.ctx, "docker", "--host", "unix://"+socket, "image", "inspect", "--format", "{{.Id}}", artifact.RuntimeImage(receipt.Image)).Output()
	// The actual running container must reference the exact imported image object.
	if err != nil || inspectErr != nil || strings.TrimSpace(string(identity)) != strings.TrimSpace(string(installed))+" true" {
		t.Fatalf("running image differs from verified upload: %v %s; config=%s", err, identity, receipt.ConfigDigest)
	}
	export := exec.CommandContext(h.ctx, "docker", "--host", "unix://"+socket, "image", "save", artifact.RuntimeImage(receipt.Image))
	stream, err := export.StdoutPipe()
	// Stream the real daemon export so its config bytes can be independently hashed.
	if err != nil {
		t.Fatal(err)
	}
	// A failed daemon export cannot verify runnable configuration bytes.
	if err := export.Start(); err != nil {
		t.Fatal(err)
	}
	verified := artifact.VerifyExport(stream, receipt)
	stream.Close()
	// Require both a valid config digest and successful completion of the real daemon export.
	if err := export.Wait(); err != nil || verified != nil {
		t.Fatalf("verify running image export: %v %v", err, verified)
	}
}

// liveRegistryRoute checks the default application responds through the real loopback Caddy edge.
func liveRegistryRoute(t *testing.T, ctx context.Context, edge string) int {
	t.Helper()
	request, _ := http.NewRequestWithContext(ctx, "GET", edge+"/", nil)
	request.Host = "private-ghcr.example.test"
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	// The route must reach the actual application's default command through Caddy.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	// Require a successful response rather than treating an edge error page as readiness.
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Caddy route status=%d", response.StatusCode)
	}
	return response.StatusCode
}

// liveRegistryCaddy starts a real edge on two disposable loopback ports without external DNS or certificates.
func liveRegistryCaddy(t *testing.T, binary, dir string) (string, string) {
	t.Helper()
	// Reserve ephemeral loopback addresses so the fixture never binds public edge ports.
	port := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		// Binding failure must not fall back to a wildcard listener.
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().String()
	}
	admin, edge := port(), port()
	configuration := map[string]any{"admin": map[string]any{"listen": admin}, "apps": map[string]any{"http": map[string]any{"servers": map[string]any{"hostlier": map[string]any{"listen": []string{edge}, "automatic_https": map[string]bool{"disable": true}, "routes": []any{}}}}}}
	raw, _ := json.Marshal(configuration)
	path := filepath.Join(dir, "caddy.json")
	// Keep all fixture edge configuration private and separate from installed Caddy state.
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(dir, "caddy.log"))
	// Diagnostics stay in the disposable fixture directory.
	if err != nil {
		t.Fatal(err)
	}
	// Close logs only after the Caddy process has stopped.
	t.Cleanup(func() { log.Close() })
	command := exec.Command(binary, "run", "--config", path)
	command.Env = append(os.Environ(), "XDG_DATA_HOME="+filepath.Join(dir, "caddy-data"), "XDG_CONFIG_HOME="+filepath.Join(dir, "caddy-config"))
	command.Stdout, command.Stderr = log, log
	// Exercise a real selected Caddy executable, not an HTTP admin stub.
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	// Reap Caddy independently so shutdown remains bounded even after a failed test.
	go func() { done <- command.Wait() }()
	// Stop only this fixture process and force termination after a short graceful deadline.
	t.Cleanup(func() {
		command.Process.Signal(os.Interrupt)
		// A stalled edge cannot prevent fixture resource cleanup.
		select {
		case <-done: // The process exited after its normal interrupt.
		case <-time.After(3 * time.Second): // Force-stop only this fixture's process.
			command.Process.Kill()
			<-done
		}
	})
	// Wait for Caddy's real admin endpoint within a bounded startup window.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		response, err := (&http.Client{Timeout: time.Second}).Get("http://" + admin + "/config/")
		// Inspect status only when the private admin connection succeeds.
		if err == nil {
			response.Body.Close()
			// Readiness requires an available configuration, not merely an open TCP listener.
			if response.StatusCode == http.StatusOK {
				return "http://" + admin, "http://" + edge
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Caddy did not start")
	return "", ""
}

// liveRegistryCleanup removes only this test's uniquely owned Docker resources, including failed candidates.
func liveRegistryCleanup(t *testing.T, socket, state string) {
	t.Helper()
	baseline, err := exec.Command("docker", "--host", "unix://"+socket, "image", "ls", "hostlier.local/artifacts", "--format", "{{.Repository}}:{{.Tag}}").Output()
	// Cleanup may remove only imported tags absent before this test began.
	if err != nil {
		t.Fatalf("snapshot imported image tags: %v", err)
	}
	existing := map[string]bool{}
	// Retain preexisting cache entries even when this fixture uses the same image content.
	for _, tag := range strings.Fields(string(baseline)) {
		existing[tag] = true
	}
	hash := sha256.Sum256([]byte(state))
	owner := hex.EncodeToString(hash[:])[:16]
	// Independent cleanup deadlines also apply when the main deployment context has expired.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Ownership labels confine deletions to the unique fixture state directory.
		for _, kind := range []string{"container", "volume", "network"} {
			ids, err := exec.CommandContext(ctx, "docker", "--host", "unix://"+socket, kind, "ls", "-q", "--filter", "label=io.hostlier.owner="+owner).Output()
			// Preserve cleanup failures in the test result rather than silently leaking workloads.
			if err != nil {
				t.Errorf("list owned %s: %v", kind, err)
				continue
			}
			// Never invoke an unscoped removal command when the owner has no matching resources.
			if len(strings.Fields(string(ids))) == 0 {
				continue
			}
			args := []string{"--host", "unix://" + socket, kind, "rm"}
			// Owned containers may still be running after a failed deployment or assertion.
			if kind == "container" {
				args = append(args, "--force")
			}
			// Report any owned resources the daemon refuses to remove.
			if output, err := exec.CommandContext(ctx, "docker", append(args, strings.Fields(string(ids))...)...).CombinedOutput(); err != nil {
				t.Errorf("cleanup %s: %v %s", kind, err, output)
			}
		}
		liveRegistryCleanupImages(t, ctx, socket, state, existing)
	})
}

// liveRegistryCleanupImages removes only new local tags named by this fixture's verified artifact receipts.
func liveRegistryCleanupImages(t *testing.T, ctx context.Context, socket, state string, existing map[string]bool) {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(state, "artifacts", "*.json"))
	// Never enumerate unrelated images for deletion; receipts identify exactly the staged test content.
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		var receipt artifact.Result
		// Incomplete or unrelated state cannot authorize removal of an image tag.
		if err != nil || json.Unmarshal(raw, &receipt) != nil || !artifact.ValidReceipt(receipt) {
			continue
		}
		tag := artifact.RuntimeImage(receipt.Image)
		// Shared cache entries that existed before this run remain available to their owner.
		if existing[tag] {
			continue
		}
		// A preparation failure may have committed an archive without importing its image.
		if err := exec.CommandContext(ctx, "docker", "--host", "unix://"+socket, "image", "inspect", tag).Run(); err != nil {
			continue
		}
		// Remove only the new fixture tag, without forcing removal of containers or shared layers.
		if output, err := exec.CommandContext(ctx, "docker", "--host", "unix://"+socket, "image", "rm", tag).CombinedOutput(); err != nil {
			t.Errorf("cleanup imported tag: %v %s", err, output)
		}
	}
}
