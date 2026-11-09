package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hostlier/internal/controller"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
	"hostlier/pkg/workflow"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// lostHookRunner preserves the production executable runner while dropping one successful hook response.
type lostHookRunner struct {
	runner  packageexec.Runner
	dropped atomic.Bool
}

// Plan invokes actual installed package planning.
func (r *lostHookRunner) Plan(ctx context.Context, req workflow.Request) (workflow.Prepared, error) {
	return r.runner.Plan(ctx, req)
}

// Apply loses only the first successful after-readiness response after real durable native effects.
func (r *lostHookRunner) Apply(ctx context.Context, req workflow.Request, digest string) (workflow.Result, error) {
	result, err := r.runner.Apply(ctx, req, digest)
	// Inject the transport failure after the real package has provisioned the owned resource.
	if err == nil && req.Step.Phase == "afterSuccess" && !r.dropped.Swap(true) {
		return workflow.Result{}, errors.New("injected lost hook response")
	}
	return result, err
}

// InspectPinned uses the originally approved package bytes for authoritative native observation.
func (r *lostHookRunner) InspectPinned(ctx context.Context, req workflow.Request, digest string) (workflow.Inspection, error) {
	return r.runner.InspectPinned(ctx, req, digest)
}

// TestDockerProductContract exercises compiled CLI, installed packages, real app writes, restart and recovery.
func TestDockerProductContract(t *testing.T) {
	// Disposable local Docker work requires explicit opt-in.
	if os.Getenv("HOSTLIER_DOCKER_E2E") != "1" {
		t.Skip("set HOSTLIER_DOCKER_E2E=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	temporary, err := os.MkdirTemp("/tmp", "hostlier-product-")
	// Short canonical paths stay within macOS Unix socket length limits.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(
		// The test owns only this unique temporary state directory.
		func() { os.RemoveAll(temporary) })
	dir, err := filepath.EvalSymlinks(temporary)
	// Canonical state paths preserve package owner identity.
	if err != nil {
		t.Fatal(err)
	}
	socket := os.Getenv("HOSTLIER_DOCKER_SOCKET")
	// Match the normal local Docker fixture default.
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	hash := sha256.Sum256([]byte(dir))
	owner := hex.EncodeToString(hash[:])[:16]
	cleanupDockerOwner(t, socket, owner)
	installWorkflowEngines(t, ctx, dir)
	binary := buildProductCLI(t, ctx, dir)
	runtime := &localProbeRuntime{socket: socket, owner: owner}
	runner := &lostHookRunner{runner: packageexec.Runner{Directory: filepath.Join(dir, "packages")}}
	options := controller.Options{StateDir: dir, Config: model.ServerConfig{Version: 1, Domains: []string{"hostlier.test"}}, Runtime: runtime, Packages: runner}
	apiSocket := filepath.Join(dir, "controller.sock")
	server := startProductController(t, options, apiSocket)
	document := strings.Replace(customProductDocument(), "  inputs:\n", "  policy:\n    maxReplicas: 1\n    allowZero: false\n  inputs:\n", 1)
	document = strings.Replace(document, "    - id: database\n", "    - id: database\n      phase: beforeDeploy\n", 1)
	document = strings.Replace(document, "  bindings:\n", "    - id: audit\n      package: postgres\n      phase: afterSuccess\n      needs: [database]\n      config:\n        mode: new-instance\n        storage: 1Gi\n  bindings:\n", 1)
	file := filepath.Join(dir, "product.yaml")
	// Product source contains only public configuration and logical package identities.
	if err := os.WriteFile(file, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	output, deployErr := runProductBinary(ctx, binary, dir, true, "product", "deploy", "--file", file, "--socket", apiSocket)
	// Lost hook response must expose failure after both authenticated app probes succeeded.
	if deployErr == nil || runtime.probes.Load() != 2 || !runner.dropped.Load() {
		t.Fatalf("lost-hook deployment err=%v probes=%d output=%s", deployErr, runtime.probes.Load(), output)
	}
	owned := productOwnedContainers(t, ctx, socket, owner)
	closeProductController(t, server)
	server = startProductController(t, options, apiSocket)
	defer closeProductController(t, server)
	assertProductPolicyAndRecovery(t, ctx, binary, dir, apiSocket, runtime)
	// Reconciliation observes original native objects without creating replacement hook effects.
	if got := productOwnedContainers(t, ctx, socket, owner); got != owned {
		t.Fatalf("hook resources changed during recovery: %s -> %s", owned, got)
	}
}

// buildProductCLI compiles the ordinary command executable used for all reviewed user actions.
func buildProductCLI(t *testing.T, ctx context.Context, dir string) string {
	t.Helper()
	binary := filepath.Join(dir, "hostlier")
	command := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/hostlier")
	command.Dir = filepath.Join("..", "..")
	// Compiler diagnostics are public fixture information.
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %v %s", err, output)
	}
	return binary
}

// startProductController starts the real protected Unix API with persistent production state.
func startProductController(t *testing.T, options controller.Options, socket string) *controller.Controller {
	t.Helper()
	server, err := controller.New(options)
	// Refuse to run a partial controller fixture.
	if err != nil {
		t.Fatal(err)
	}
	// The CLI uses the same owner-only socket as a local installed deployment.
	if err := server.Listen(controller.ListenOptions{SocketPath: socket}); err != nil {
		t.Fatal(err)
	}
	return server
}

// closeProductController releases the durable writer before controller restart.
func closeProductController(t *testing.T, server *controller.Controller) {
	t.Helper()
	// Closing includes operation draining before opening the same state directory again.
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// runProductBinary uses a real terminal for the CLI's deliberate per-plan approval requirement.
func runProductBinary(ctx context.Context, binary, dir string, approve bool, args ...string) (string, error) {
	command := exec.CommandContext(ctx, binary, args...)
	// BSD script creates a PTY while preserving per-plan interactive approval.
	if approve {
		command = exec.CommandContext(ctx, "script", append([]string{"-q", "/dev/null", binary}, args...)...)
	}
	command.Dir = dir
	command.Env = append(os.Environ(), "HOSTLIER_HOME="+filepath.Join(dir, "home"))
	// Read-only commands need no terminal or synthetic input.
	if !approve {
		output, err := command.CombinedOutput()
		return string(output), err
	}
	input, err := command.StdinPipe()
	// Keep terminal input open until the prompt arrives, preventing an early EOF cancellation.
	if err != nil {
		return "", err
	}
	defer input.Close()
	writer := &productApprovalWriter{input: input}
	command.Stdout, command.Stderr = writer, writer
	err = command.Run()
	return writer.buffer.String(), err
}

// productApprovalWriter answers only after the compiled CLI displays its exact-plan approval prompt.
type productApprovalWriter struct {
	buffer   bytes.Buffer
	input    io.Writer
	answered bool
}

// Write records public command output and supplies one deliberate affirmative answer.
func (w *productApprovalWriter) Write(value []byte) (int, error) {
	count, err := w.buffer.Write(value)
	// Waiting for the actual prompt avoids PTY EOF and echo timing races.
	if !w.answered && strings.Contains(w.buffer.String(), "[y/N]") {
		w.answered = true
		_, err = io.WriteString(w.input, "yes\n")
	}
	return count, err
}

// assertProductPolicyAndRecovery verifies durable policy and exact hook inspection without replay.
func assertProductPolicyAndRecovery(t *testing.T, ctx context.Context, binary, dir, socket string, runtime *localProbeRuntime) {
	t.Helper()
	output, err := runProductBinary(ctx, binary, dir, true, "scale", "workflow-app", "--replicas", "0", "--socket", socket)
	// Even a fresh CLI process after server restart cannot bypass captured product bounds.
	if err == nil || !strings.Contains(output, "product policy") {
		t.Fatalf("policy bypass err=%v output=%s", err, output)
	}
	output, err = runProductBinary(ctx, binary, dir, false, "workflow", "list", "workflow-app", "--socket", socket)
	// Inventory returns only safe logical recovery identities, including the per-approval hook suffix.
	if err != nil {
		t.Fatal(err, output)
	}
	var rows []map[string]string
	// Actual command JSON must identify the one uncertain hook from the lost response.
	if json.Unmarshal([]byte(output), &rows) != nil {
		t.Fatal(output)
	}
	hook := ""
	// Select the exact attempted hook instead of a mutable declaration name.
	for _, row := range rows {
		// Only the attempted hook with an unknown result needs metadata reconciliation.
		if row["status"] == "uncertain" {
			hook = row["step"]
		}
	}
	// A missing uncertain record would permit unsafe mutation replay.
	if !strings.HasPrefix(hook, "audit@") {
		t.Fatal(rows)
	}
	output, err = runProductBinary(ctx, binary, dir, true, "workflow", "reconcile", "workflow-app", hook, "--socket", socket)
	// Read-only native inspection must verify the real hook outcome before approved metadata recovery.
	if err != nil || runtime.probes.Load() != 2 {
		t.Fatalf("hook recovery err=%v output=%s", err, output)
	}
}

// productOwnedContainers independently observes the three actual native engine identities.
func productOwnedContainers(t *testing.T, ctx context.Context, socket, owner string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "docker", "--host", "unix://"+socket, "container", "ls", "-aq", "--filter", "label=io.hostlier.owner="+owner)
	output, err := command.Output()
	// Only this test's PostgreSQL, Redis and after-success PostgreSQL hook may remain.
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Fields(string(output))
	// A missing engine means the fixture did not establish the required actual resources.
	if len(ids) != 3 {
		t.Fatalf("expected three owned native engine containers, got %v", ids)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}
