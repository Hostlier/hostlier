package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hostlier/internal/model"
	hostruntime "hostlier/internal/runtime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// crashRequest carries public test execution settings to a process that exits without cleanup.
type crashRequest struct {
	State, Socket, Admin, Stage string
	Spec                        model.Project
}

// TestDockerCrashChild runs only as an isolated hard-exit worker created by the parent gate.
func TestDockerCrashChild(t *testing.T) {
	path := os.Getenv("HOSTLIER_CRASH_REQUEST")
	// Ordinary test enumeration must never exit the main test runner.
	if path == "" {
		t.Skip("subprocess only")
	}
	raw, err := os.ReadFile(path)
	// A malformed child request must fail rather than silently skipping a crash gate.
	if err != nil {
		t.Fatal(err)
	}
	var request crashRequest
	// Decode only the explicit parent fixture's non-secret configuration.
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	r, err := hostruntime.New(hostruntime.Options{StateDir: request.State, SocketPath: request.Socket, CaddyAdmin: request.Admin, SkipPublicHTTPSProbe: true, HealthTimeout: 3 * time.Second})
	// Runtime initialization precedes the deliberate process termination point.
	if err != nil {
		t.Fatal(err)
	}
	// Abrupt exit bypasses all deferred runtime rollback, like a terminated controller process.
	_, err = r.Deploy(context.Background(), request.Spec, model.DeployContext{OperationID: "interrupted-" + request.Stage, OnEvent: func(event model.Event) {
		// The selected real runtime event proves external execution reached this boundary.
		if event.Step == request.Stage {
			os.Exit(91)
		}
	}})
	t.Fatalf("crash boundary was not reached: %v", err)
}

// TestDockerScalingProcessCrash proves fresh-process recovery across candidate creation and route cutover.
func TestDockerScalingProcessCrash(t *testing.T) {
	// These tests require an explicitly selected disposable local Docker daemon.
	if os.Getenv("HOSTLIER_DOCKER_E2E") != "1" {
		t.Skip("set HOSTLIER_DOCKER_E2E=1")
	}
	// Cover both continuously running stateless sources and exclusive persistent-volume writers.
	for _, stateful := range []bool{false, true} {
		label := "stateless"
		// Name persistent cases separately so their data assertions remain visible in test evidence.
		if stateful {
			label = "volume"
		}
		// Exercise crashes before provisioning, with a running candidate, and after edge switch.
		for _, stage := range []string{"pull", "health", "certificate", "drain"} {
			// Each crash has an independently owned daemon footprint and retained state directory.
			t.Run(label+"/"+stage, func(t *testing.T) { checkDockerProcessCrash(t, stateful, stage) })
		}
	}
}

// checkDockerProcessCrash validates recovery through actual Docker and Caddy with a new runtime process.
func checkDockerProcessCrash(t *testing.T, stateful bool, stage string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := t.TempDir()
	sum := sha256.Sum256([]byte(dir))
	owner := hex.EncodeToString(sum[:])[:16]
	socket := os.Getenv("HOSTLIER_DOCKER_SOCKET")
	docker := dockerCommand(t, ctx, socket)
	cleanupDockerOwner(t, socket, owner)
	admin, port, pool := startCaddyFixture(t, os.Getenv("HOSTLIER_CADDY_BINARY"), dir)
	get := fixtureHTTPSGetter(t, port, pool)
	options := hostruntime.Options{StateDir: dir, SocketPath: socket, CaddyAdmin: admin, SkipPublicHTTPSProbe: true, HealthTimeout: 3 * time.Second}
	r, err := hostruntime.New(options)
	// Fixture creation must succeed before any external mutation.
	if err != nil {
		t.Fatal(err)
	}
	image := inspectDockerFixtureImage(t, ctx, r, docker, dir)
	spec := model.Project{Version: 1, Name: "crash", Image: image.Digest, Port: 80, Route: "crash.hostlier.test", Resources: &model.Limits{CPU: "0.25", Memory: "64Mi"}}
	// A mounted source is deliberately stopped before the replacement writer starts.
	if stateful {
		spec.Volumes = map[string]model.Volume{"data": {Mount: "/usr/share/nginx/html", Size: "1Gi"}}
	}
	first := scalingDeploy(t, ctx, r, spec, "original")
	docker("exec", first.RuntimeID, "sh", "-c", "echo scaling-data > /usr/share/nginx/html/index.html")
	spec.Resources = &model.Limits{CPU: "0.5", Memory: "96Mi"}
	request := crashRequest{State: dir, Socket: socket, Admin: admin, Stage: stage, Spec: spec}
	runCrashProcess(t, ctx, request)
	restarted, err := hostruntime.New(options)
	// A fresh adapter must read retained crash evidence rather than in-memory transaction flags.
	if err != nil {
		t.Fatal(err)
	}
	result := scalingDeploy(t, ctx, restarted, spec, "recovered")
	// A successful operation leaves exactly one running instance with the new limits.
	if got := docker("ps", "-q", "--filter", "label=io.hostlier.owner="+owner); got != result.RuntimeID[:12] {
		t.Fatalf("running capacity leaked: %s, expected %s", got, result.RuntimeID)
	}
	// Interrupted candidates must be removed rather than retained as stray revisions.
	if got := docker("ps", "-aq", "--filter", "label=io.hostlier.owner="+owner, "--filter", "label=io.hostlier.operation=interrupted-"+stage); got != "" && stage != "drain" {
		t.Fatalf("uncommitted candidate leaked: %s", got)
	}
	// Persistent app data survives the hard exit and two transfers of exclusive writing.
	if stateful {
		scalingData(t, get, result.URL)
	}
	assertCrashRecoveryRoute(t, get, result.URL)
}

// runCrashProcess requires the exact deliberate exit status from the external execution boundary.
func runCrashProcess(t *testing.T, ctx context.Context, request crashRequest) {
	t.Helper()
	raw, err := json.Marshal(request)
	// A serialization failure cannot establish a child crash assertion.
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(request.State, "crash-request.json")
	// Keep fixture state consistent with normal protected controller files.
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDockerCrashChild$")
	child.Env = append(os.Environ(), "HOSTLIER_CRASH_REQUEST="+path)
	out, err := child.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	// A startup failure or normal completion cannot pass the crash gate.
	if !ok || exit.ExitCode() != 91 {
		t.Fatalf("child did not hard-exit at %s: %v %s", request.Stage, err, out)
	}
}

// assertCrashRecoveryRoute verifies trusted local TLS and actual application response after recovery.
func assertCrashRecoveryRoute(t *testing.T, get func(string) (int, string), address string) {
	t.Helper()
	status, body := get(address)
	// Both stock nginx and persistent fixture content are valid recovered application responses.
	if status != 200 || (!strings.Contains(body, "nginx") && !strings.Contains(body, "scaling-data")) {
		t.Fatalf("recovered route: %d %s", status, body)
	}
}
