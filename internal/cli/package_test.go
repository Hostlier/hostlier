package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/controller"
	"hostlier/internal/model"
)

// recordingRuntime intentionally verifies CLI/controller integration only. Real
// Docker/Caddy, VM installation and public certificates have separate gates.
type recordingRuntime struct {
	mu          sync.Mutex
	deployments int
}

// Name supports name in the integration fixture.
func (*recordingRuntime) Name() string { return "docker" }

// InspectImage supports inspect image in the integration fixture.
func (*recordingRuntime) InspectImage(_ context.Context, image string) (model.ImageInfo, error) {
	return model.ImageInfo{Image: image, Digest: image}, nil
}

// Deploy supports deploy in the integration fixture.
func (r *recordingRuntime) Deploy(_ context.Context, p model.Project, _ model.DeployContext) (model.Deployment, error) {
	r.mu.Lock()
	r.deployments++
	r.mu.Unlock()
	return model.Deployment{Name: p.Name, RuntimeID: "recorded", ImageDigest: p.Image, Status: "running"}, nil
}

// Action supports action in the integration fixture.
func (*recordingRuntime) Action(context.Context, string, string, model.ActionContext) error {
	return nil
}

// Logs supports logs in the integration fixture.
func (*recordingRuntime) Logs(context.Context, string) (string, error) { return "", nil }

// Doctor supports doctor in the integration fixture.
func (*recordingRuntime) Doctor(context.Context) (any, error) {
	return map[string]any{"checks": []any{}}, nil
}

// count supports count in the integration fixture.
func (r *recordingRuntime) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.deployments }

const pythonPTY = `
import os,pty,subprocess,sys,select,time
master,slave=pty.openpty()
question,value=sys.argv[1:3]
p=subprocess.Popen(sys.argv[3:],stdin=slave,stdout=slave,stderr=slave,env=os.environ)
os.close(slave)
output=b''
sent=False
end=time.time()+30
while time.time()<end:
  if select.select([master],[],[],0.1)[0]:
    try: chunk=os.read(master,65536)
    except OSError: break
    if not chunk: break
    output+=chunk
    if question.encode() in output and not sent:
      os.write(master,value.encode()+b'\n');sent=True
  if p.poll() is not None: break
if p.poll() is None: p.kill()
sys.stdout.buffer.write(output)
sys.exit(p.wait())
`

// TestPackagedGoCLIHTTPSAndPTYApproval verifies packaged go clihttps and pty approval.
func TestPackagedGoCLIHTTPSAndPTYApproval(t *testing.T) {
	// Guard against python PTY harness unavailable.
	if _, e := exec.LookPath("python3"); e != nil {
		t.Skip("Python PTY harness unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("/tmp", "hostlier-cli-")
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, "hostlier")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/hostlier")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	// Guard against static build.
	if output, e := build.CombinedOutput(); e != nil {
		t.Fatalf("static build: %v %s", e, output)
	}
	runtime := &recordingRuntime{}
	server, e := controller.New(controller.Options{StateDir: filepath.Join(dir, "state"), Config: model.ServerConfig{Version: 1, Domains: []string{"example.test"}}, Runtime: runtime, Hostname: "control.example.test"})
	// Fail immediately if fixture setup or execution fails.
	if e != nil {
		t.Fatal(e)
	}
	defer server.Close(context.Background())
	socket := filepath.Join(dir, "controller.sock")
	// Fail immediately if fixture setup or execution fails.
	if e = server.Listen(controller.ListenOptions{SocketPath: socket}); e != nil {
		t.Fatal(e)
	}
	https := httptest.NewTLSServer(withFixtureHuman(t, server, server.Handler()))
	defer https.Close()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: https.Certificate().Raw}), 0600)
	local, e := client.New(client.Options{SocketPath: socket})
	// Fail immediately if fixture setup or execution fails.
	if e != nil {
		t.Fatal(e)
	}
	var token struct {
		Token string `json:"token"`
	}
	// Fail immediately if fixture setup or execution fails.
	if e = local.Request(ctx, "POST", "/v1/pairing/tokens", map[string]string{"name": "package"}, &token); e != nil {
		t.Fatal(e)
	}
	env := append(os.Environ(), "HOSTLIER_HOME="+filepath.Join(dir, "profiles"))
	harness := packagedCLI{ctx: ctx, binary: binary, dir: dir, env: env, ca: ca}
	run := harness.run
	// Guard against invalid certificate accepted.
	if output, e := run([]string{"connect", https.URL, "--name", "test", "--token-stdin"}, token.Token, false); e == nil {
		t.Fatalf("invalid certificate accepted: %s", output)
	}
	// Guard against enrollment.
	if output, e := run([]string{"connect", https.URL, "--name", "test", "--token-stdin"}, token.Token, true); e != nil {
		t.Fatalf("enrollment: %v %s", e, output)
	}

	assertRequestedExecutableEnrollment(t, harness, https.URL, local)
	image := "nginx@sha256:" + strings.Repeat("a", 64)
	os.WriteFile(filepath.Join(dir, ".hostlier"), []byte(fmt.Sprintf("version: 1\nname: fixture\nimage: %s\nport: 80\nroute: fixture.example.test\n", image)), 0600)
	assertExecutableApprovalGate(t, harness, runtime)
	pty := harness.pty
	assertExecutableSecretPrompts(t, pty)
	assertExecutableDeployment(t, harness, runtime)
}

// assertRequestedExecutableEnrollment runs the executable enrollment flow through local inspection and approval.
func assertRequestedExecutableEnrollment(t *testing.T, harness packagedCLI, url string, local *client.Client) {
	t.Helper()
	// Exercise CLI-first enrollment through the actual HTTPS executable as well.
	pendingCLI := exec.CommandContext(harness.ctx, harness.binary, "connect", url, "--name", "requested", "--request")
	pendingCLI.Env = append(harness.env, "HOSTLIER_CA_FILE="+harness.ca)
	pendingOutput, err := pendingCLI.StdoutPipe()
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	var pendingErrors bytes.Buffer
	pendingCLI.Stderr = &pendingErrors
	// Fail immediately if fixture setup or execution fails.
	if err = pendingCLI.Start(); err != nil {
		t.Fatal(err)
	}
	defer pendingCLI.Process.Kill()
	scan := bufio.NewReader(pendingOutput)
	line, err := scan.ReadBytes('\n')
	// Guard against pending enrollment output.
	if err != nil {
		t.Fatalf("pending enrollment output: %v %s", err, pendingErrors.String())
	}
	var pending struct {
		Code string `json:"approvalCode"`
	}
	// Guard against pending code.
	if err = json.Unmarshal(line, &pending); err != nil || pending.Code == "" {
		t.Fatalf("pending code: %v %s", err, line)
	}
	assertPendingExecutableIdentity(t, harness.ctx, local, pending.Code)
	completed, err := io.ReadAll(scan)
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	// Guard against requested enrollment.
	if err = pendingCLI.Wait(); err != nil {
		t.Fatalf("requested enrollment: %v %s", err, pendingErrors.String())
	}
	// Guard against requested enrollment did not complete.
	if !strings.Contains(string(completed), `"connected":true`) {
		t.Fatalf("requested enrollment did not complete: %s", completed)
	}
}

// assertExecutableSecretPrompts checks hidden terminal input and Ctrl-C cancellation.
func assertExecutableSecretPrompts(t *testing.T, pty func(string, string, ...string) ([]byte, error)) {
	t.Helper()
	secret := "fixture-hidden-6d52d077"
	output, e := pty("Secret value: ", secret, "secret", "set", "hidden-key")
	// Guard against hidden input.
	if e != nil {
		t.Fatalf("hidden input: %v %s", e, output)
	}
	// Guard against hidden prompt echoed secret.
	if strings.Contains(string(output), secret) {
		t.Fatal("hidden prompt echoed secret")
	}
	cancelled, cancelErr := pty("Secret value: ", "\x03", "secret", "set", "cancelled-key")
	// Guard against hidden Ctrl-C did not cancel.
	if cancelErr == nil || !strings.Contains(string(cancelled), "Cancelled") {
		t.Fatalf("hidden Ctrl-C did not cancel: %v %s", cancelErr, cancelled)
	}
}

// assertExecutableDeployment checks approved terminal deployment and resulting application state.
func assertExecutableDeployment(t *testing.T, harness packagedCLI, runtime *recordingRuntime) {
	t.Helper()
	output, e := harness.pty("Approve this exact deployment plan?", "yes", "deploy")
	// Guard against pTY deployment.
	if e != nil {
		t.Fatalf("PTY deployment: %v %s", e, output)
	}
	// Guard against deployment result.
	if !strings.Contains(string(output), "succeeded") || runtime.count() != 1 {
		t.Fatalf("deployment result: %s", output)
	}
	output, e = harness.run([]string{"status", "fixture"}, "", true)
	// Guard against status.
	if e != nil {
		t.Fatalf("status: %v %s", e, output)
	}
	var app map[string]any
	// Guard against app state.
	if e = json.Unmarshal(output, &app); e != nil || app["status"] != "running" {
		t.Fatalf("app state: %v %s", e, output)
	}
}

// packagedCLI holds process configuration shared by executable integration scenarios.
type packagedCLI struct {
	ctx             context.Context
	binary, dir, ca string
	env             []string
}

// run executes one CLI invocation with optional certificate trust and input.
func (h packagedCLI) run(args []string, input string, trusted bool) ([]byte, error) {
	cmd := exec.CommandContext(h.ctx, h.binary, args...)
	cmd.Dir = h.dir
	cmd.Env = h.env
	// Fail immediately if fixture setup or execution fails.
	if trusted {
		cmd.Env = append(cmd.Env, "HOSTLIER_CA_FILE="+h.ca)
	}
	cmd.Stdin = strings.NewReader(input)
	return cmd.CombinedOutput()
}

// pty answers one interactive prompt through a real terminal device.
func (h packagedCLI) pty(question, value string, args ...string) ([]byte, error) {
	parameters := append([]string{"-c", pythonPTY, question, value, h.binary}, args...)
	cmd := exec.CommandContext(h.ctx, "python3", parameters...)
	cmd.Dir = h.dir
	cmd.Env = append(h.env, "HOSTLIER_CA_FILE="+h.ca)
	return cmd.CombinedOutput()
}

// assertExecutableApprovalGate checks that planning and bypass attempts never deploy.
func assertExecutableApprovalGate(t *testing.T, harness packagedCLI, runtime *recordingRuntime) {
	t.Helper()
	// Guard against approval bypass accepted.
	for _, args := range [][]string{{"deploy", "--yes"}} {
		// Guard against approval bypass accepted.
		if output, e := harness.run(args, "", true); e == nil {
			t.Fatalf("approval bypass accepted: %s", output)
		}
	}
	// Guard against plan.
	if output, e := harness.run([]string{"plan"}, "", true); e != nil {
		t.Fatalf("plan: %v %s", e, output)
	}
	// Guard against preapproval mutation.
	if runtime.count() != 0 {
		t.Fatal("preapproval mutation")
	}
}

// assertPendingExecutableIdentity checks the requested identity before granting local enrollment approval.
func assertPendingExecutableIdentity(t *testing.T, ctx context.Context, local *client.Client, code string) {
	t.Helper()
	var reviewed map[string]any
	// Fail immediately if fixture setup or execution fails.
	if err := local.Request(ctx, "POST", "/v1/pairing/inspect", map[string]string{"code": code}, &reviewed); err != nil {
		t.Fatal(err)
	}
	// Guard against review identity.
	if reviewed["name"] != "requested" {
		t.Fatalf("review identity: %#v", reviewed)
	}
	// Fail immediately if fixture setup or execution fails.
	if err := local.Request(ctx, "POST", "/v1/pairing/approve", map[string]string{"code": code}, nil); err != nil {
		t.Fatal(err)
	}
}
