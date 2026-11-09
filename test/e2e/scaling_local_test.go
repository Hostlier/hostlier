package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hostlier/internal/model"
	hostruntime "hostlier/internal/runtime"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDockerScaling verifies real limits, persistent data and durable zero through replacement.
func TestDockerScaling(t *testing.T) {
	// Live fixtures require explicit local runtime opt-in.
	if os.Getenv("HOSTLIER_DOCKER_E2E") != "1" {
		t.Skip("set HOSTLIER_DOCKER_E2E=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	sum := sha256.Sum256([]byte(dir))
	owner := hex.EncodeToString(sum[:])[:16]
	socket := os.Getenv("HOSTLIER_DOCKER_SOCKET")
	docker := dockerCommand(t, ctx, socket)
	cleanupDockerOwner(t, socket, owner)
	admin, port, pool := startCaddyFixture(t, os.Getenv("HOSTLIER_CADDY_BINARY"), dir)
	get := fixtureHTTPSGetter(t, port, pool)
	options := hostruntime.Options{StateDir: dir, SocketPath: socket, CaddyAdmin: admin, SkipPublicHTTPSProbe: true, HealthTimeout: 2 * time.Second}
	r, err := hostruntime.New(options)
	// Runtime setup must succeed before exercising workloads.
	if err != nil {
		t.Fatal(err)
	}
	image := inspectDockerFixtureImage(t, ctx, r, docker, dir)
	checkStatelessReplacement(t, ctx, r, image.Digest, docker, get)
	spec := model.Project{Version: 1, Name: "scaling", Image: image.Digest, Port: 80, Route: "scaling.hostlier.test", Resources: &model.Limits{CPU: "0.25", Memory: "64Mi"}, Volumes: map[string]model.Volume{"data": {Mount: "/usr/share/nginx/html", Size: "1Gi"}}}
	first := scalingDeploy(t, ctx, r, spec, "first")
	docker("exec", first.RuntimeID, "sh", "-c", "echo scaling-data > /usr/share/nginx/html/index.html")
	spec.Resources = &model.Limits{CPU: "0.5", Memory: "96Mi"}
	larger := scalingDeploy(t, ctx, r, spec, "larger")
	// Observe effective daemon limits independently of desired state.
	if got := docker("inspect", "--format", "{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}}", larger.RuntimeID); got != "500000000 100663296" {
		t.Fatal(got)
	}
	scalingData(t, get, larger.URL)
	checkScalingFailure(t, ctx, r, spec, larger.URL, get)
	zero := 0
	spec.Replicas = &zero
	stopped := scalingDeploy(t, ctx, r, spec, "zero")
	checkScalingZero(t, ctx, options, stopped, docker, get)
	one := 1
	spec.Replicas = &one
	spec.Health = &model.Health{Path: "/never-ready"}
	_, failed := r.Deploy(ctx, spec, model.DeployContext{OperationID: "failed-up"})
	// A failed approved start must not wake the old desired-zero workload during rollback.
	if failed == nil {
		t.Fatal("unready scale-up accepted")
	}
	checkScalingZero(t, ctx, options, stopped, docker, get)
	spec.Health = nil
	spec.Replicas = &one
	up := scalingDeploy(t, ctx, r, spec, "up")
	scalingData(t, get, up.URL)
}

// scalingDeploy applies one immutable approved fixture revision.
func scalingDeploy(t *testing.T, ctx context.Context, r model.Runtime, p model.Project, id string) model.Deployment {
	t.Helper()
	out, err := r.Deploy(ctx, p, model.DeployContext{OperationID: id})
	// Failed fixture steps cannot establish readiness.
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// scalingData verifies stable routing and independently written retained application data.
func scalingData(t *testing.T, get func(string) (int, string), address string) {
	t.Helper()
	status, body := get(address)
	// Routed responses must contain the data written before replacement.
	if status != 200 || !strings.Contains(body, "scaling-data") {
		t.Fatalf("data route %d %s", status, body)
	}
}

// checkScalingFailure ensures failed candidate readiness restores the previous writer and route.
func checkScalingFailure(t *testing.T, ctx context.Context, r model.Runtime, p model.Project, address string, get func(string) (int, string)) {
	t.Helper()
	p.Health = &model.Health{Path: "/never-ready"}
	_, err := r.Deploy(ctx, p, model.DeployContext{OperationID: "failed"})
	// Unready candidates must fail instead of taking traffic.
	if err == nil {
		t.Fatal("unready candidate accepted")
	}
	scalingData(t, get, address)
}

// checkScalingZero reconstructs runtime metadata and verifies zero cannot be overridden by lifecycle.
func checkScalingZero(t *testing.T, ctx context.Context, o hostruntime.Options, app model.Deployment, docker func(...string) string, get func(string) (int, string)) {
	t.Helper()
	r, err := hostruntime.New(o)
	// Reconstructed adapters must retain the approved desired specification.
	if err != nil {
		t.Fatal(err)
	}
	// Both lifecycle commands must preserve zero capacity.
	for _, action := range []string{"start", "restart"} {
		// Scaling above zero requires a fresh approval plan.
		if r.Action(ctx, app.Name, action, model.ActionContext{}) == nil {
			t.Fatal("zero overridden", action)
		}
	}
	// Inspect actual running containers rather than trusting metadata status.
	if docker("ps", "-q", "--filter", "label=io.hostlier.app="+app.Name) != "" {
		t.Fatal("zero still running")
	}
	status, _ := get(app.URL)
	// Zero retains its reserved route with an unavailable response.
	if status != 503 {
		t.Fatalf("zero route status %d", status)
	}
}

// checkStatelessReplacement verifies overlap before readiness and graceful retention after cutover.
func checkStatelessReplacement(t *testing.T, ctx context.Context, r model.Runtime, image string, docker func(...string) string, get func(string) (int, string)) {
	t.Helper()
	spec := model.Project{Version: 1, Name: "stateless", Image: image, Port: 80, Route: "stateless.hostlier.test"}
	first := scalingDeploy(t, ctx, r, spec, "stateless-first")
	docker("exec", first.RuntimeID, "sh", "-c", "echo previous-capacity > /usr/share/nginx/html/index.html")
	observed := false
	// Observe readiness before the runtime has permission to switch the route.
	_, err := r.Deploy(ctx, spec, model.DeployContext{OperationID: "stateless-next", OnEvent: func(event model.Event) {
		// At candidate readiness both containers must run while traffic still reaches the old service.
		if event.Step == "health" {
			observed = true
			// Independently verify the source is still alive during candidate readiness.
			if docker("inspect", "--format", "{{.State.Running}}", first.RuntimeID) != "true" {
				t.Fatal("source stopped before readiness")
			}
			status, body := get(first.URL)
			// Traffic must stay on healthy old capacity until activation.
			if status != 200 || !strings.Contains(body, "previous-capacity") {
				t.Fatalf("premature cutover %d %s", status, body)
			}
		}
	}})
	// Candidate activation and readiness observation must both complete.
	if err != nil || !observed {
		t.Fatal("replacement", err, observed)
	}
	// The source is retained and gracefully stopped only after the route succeeds.
	if docker("inspect", "--format", "{{.State.Running}}", first.RuntimeID) != "false" {
		t.Fatal("source not drained")
	}
	checkStatelessFailure(t, ctx, r, spec, first.URL, get)
}

// checkStatelessFailure verifies failed candidate readiness leaves the active service available.
func checkStatelessFailure(t *testing.T, ctx context.Context, r model.Runtime, spec model.Project, address string, get func(string) (int, string)) {
	t.Helper()
	spec.Health = &model.Health{Path: "/never-ready"}
	_, err := r.Deploy(ctx, spec, model.DeployContext{OperationID: "stateless-failure"})
	// A failed stateless replacement must leave existing healthy capacity serving.
	if err == nil {
		t.Fatal("unready stateless candidate accepted")
	}
	status, body := get(address)
	// The active replacement remains healthy after candidate cleanup.
	if status != 200 || !strings.Contains(body, "nginx") {
		t.Fatalf("stateless rollback %d %s", status, body)
	}
}
