package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hostlier/internal/model"
	hostruntime "hostlier/internal/runtime"
	"os"
	"testing"
	"time"
)

// TestDockerScalingCapacityFailure proves daemon rejection and real kernel OOM preserve the previous service.
func TestDockerScalingCapacityFailure(t *testing.T) {
	// Kernel and daemon behavior require explicit access to a disposable local Docker instance.
	if os.Getenv("HOSTLIER_DOCKER_E2E") != "1" {
		t.Skip("set HOSTLIER_DOCKER_E2E=1")
	}
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
	r, err := hostruntime.New(hostruntime.Options{StateDir: dir, SocketPath: socket, CaddyAdmin: admin, SkipPublicHTTPSProbe: true, HealthTimeout: 2 * time.Second})
	// No external deployment proceeds without a valid runtime configuration.
	if err != nil {
		t.Fatal(err)
	}
	image := inspectDockerFixtureImage(t, ctx, r, docker, dir)
	spec := model.Project{Version: 1, Name: "capacity", Image: image.Digest, Port: 80, Route: "capacity.hostlier.test", Resources: &model.Limits{CPU: "0.25", Memory: "128Mi"}, Volumes: map[string]model.Volume{"boot": {Mount: "/docker-entrypoint.d", Size: "1Gi"}}}
	source := scalingDeploy(t, ctx, r, spec, "capacity-source")
	rejected := spec
	rejected.Resources = &model.Limits{CPU: "4096", Memory: "128Mi"}
	_, err = r.Deploy(ctx, rejected, model.DeployContext{OperationID: "impossible-cpu"})
	// A real daemon rejects more CPU capacity than the local test host possesses.
	if err == nil {
		t.Fatal("daemon accepted impossible CPU allocation")
	}
	assertCrashRecoveryRoute(t, get, source.URL)
	checkDockerCandidateOOM(t, ctx, r, docker, spec, source, owner)
	assertCrashRecoveryRoute(t, get, source.URL)
	// Only the committed source may remain running after both failed replacements.
	if got := docker("ps", "-q", "--filter", "label=io.hostlier.owner="+owner); got != source.RuntimeID[:12] {
		t.Fatalf("failed replacements changed running source: %s", got)
	}
}

// checkDockerCandidateOOM injects memory pressure into the candidate's PID1 while retaining original volume ownership.
func checkDockerCandidateOOM(t *testing.T, ctx context.Context, r model.Runtime, docker func(...string) string, spec model.Project, source model.Deployment, owner string) {
	t.Helper()
	// Nginx sources envsh files, so exec makes the allocator the candidate's main process.
	script := "printf '%s\\n' \"exec awk 'BEGIN { while (1) { a[i++]=sprintf(\\\"%10000s\\\",i) } }'\" > /docker-entrypoint.d/99-oom.envsh; chmod +x /docker-entrypoint.d/99-oom.envsh"
	docker("exec", source.RuntimeID, "sh", "-c", script)
	spec.Resources = &model.Limits{CPU: "0.5", Memory: "16Mi"}
	observed := false
	// Observe the failure boundary before allowing the runtime to restore its previous writer.
	_, err := r.Deploy(ctx, spec, model.DeployContext{OperationID: "candidate-oom", OnEvent: func(event model.Event) {
		// Wait until the replacement starts before observing the real daemon OOM flag.
		if event.Step != "health" {
			return
		}
		observed = waitCandidateOOM(docker, owner)
		// Remove only the test allocator before runtime rollback restarts the retained source.
		docker("run", "--rm", "--label", "io.hostlier.owner="+owner, "--volumes-from", source.RuntimeID, "--entrypoint", "sh", spec.Image, "-c", "rm /docker-entrypoint.d/99-oom.envsh")
	}})
	// Failed readiness must be backed by independent evidence of a kernel-killed candidate.
	if err == nil || !observed {
		t.Fatalf("candidate OOM was not observed and rolled back: error=%v observed=%t", err, observed)
	}
	// Report the observed OOM explicitly instead of only a generic readiness failure.
	if failure, ok := err.(*model.Error); !ok || failure.Code != "APPLICATION_OOM" {
		t.Fatalf("missing OOM diagnosis: %v", err)
	}
	// Failed limits must not change the old container's effective approved memory allocation.
	if got := docker("inspect", "--format", "{{.HostConfig.Memory}} {{.State.Running}}", source.RuntimeID); got != "134217728 true" {
		t.Fatal(got)
	}
}

// waitCandidateOOM polls the daemon's kernel result rather than inferring OOM from an HTTP timeout.
func waitCandidateOOM(docker func(...string) string, owner string) bool {
	id := docker("ps", "-aq", "--filter", "label=io.hostlier.owner="+owner, "--filter", "label=io.hostlier.operation=candidate-oom")
	// Disable only the fixture candidate's restart loop to make the first kernel failure terminal.
	docker("update", "--restart=no", id)
	deadline := time.Now().Add(10 * time.Second)
	// Bound the pressure test so failure cannot turn into an unbounded runner.
	for time.Now().Before(deadline) {
		// Docker reports the actual cgroup OOM event on the main container process.
		if docker("inspect", "--format", "{{.State.OOMKilled}} {{.State.Running}}", id) == "true false" {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
