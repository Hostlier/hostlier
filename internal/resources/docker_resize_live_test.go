package resources

import (
	"context"
	"hostlier/internal/model"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveDockerResize preserves native database writes across owned-container limit updates.
func TestLiveDockerResize(t *testing.T) {
	// Disposable local infrastructure requires explicit opt in.
	if os.Getenv("HOSTLIER_LIVE_RESOURCES") != "1" {
		t.Skip("live Docker opt in required")
	}
	// Each built-in engine uses a separate temporary ownership scope.
	for _, kind := range []string{"postgres", "redis", "neo4j"} {
		// Run allocation and native protocol checks per supported engine.
		t.Run(kind, func(t *testing.T) { checkDockerResize(t, kind) })
	}
}

// checkDockerResize provisions an isolated engine and verifies actual limit enforcement metadata.
func checkDockerResize(t *testing.T, kind string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	generic, err := New(Options{StateDir: t.TempDir(), HealthTimeout: 2 * time.Minute})
	// Invalid fixture configuration must fail before any infrastructure writes.
	if err != nil {
		t.Fatal(err)
	}
	m := generic.(*Manager)
	c := model.ResourceCredentials{Username: "hostlier", Password: "fixture-aB7f53b9e0334e4da40d", Database: "app"}
	// Neo4j requires its fixed administrative account.
	if kind == "neo4j" {
		c.Username = "neo4j"
	}
	op := model.ResourceContext{Credentials: c, Purge: true, OperationID: "resize-approved"}
	spec := model.ResourceSpec{Name: "resize-fixture", Type: kind, Storage: "1Gi"}
	// Purge only this test's owned volume and private network.
	defer m.Action(context.Background(), spec.Name, "delete", op)
	// Engine readiness is authenticated before writing fixture contents.
	if _, err = m.Provision(ctx, spec, op); err != nil {
		t.Fatal(err)
	}
	prior, err := m.find(spec.Name)
	// Require successful resource observation before advancing maintenance.
	if err != nil {
		t.Fatal(err)
	}
	write, read, env := dockerFixtureCommands(kind, c)
	// Fixture writes must commit successfully before maintenance begins.
	if _, err = m.execute(ctx, spec.Name, write, env); err != nil {
		t.Fatal(err)
	}
	next := prior.Spec
	next.CPU = "0.5"
	next.Memory = "512Mi"
	// The built-in Neo4j heap and page cache require additional headroom.
	if kind == "neo4j" {
		next.Memory = "1536Mi"
	}
	// Apply the validated allocation and require authenticated readiness.
	if _, err = m.Resize(ctx, next, op); err != nil {
		t.Fatal(err)
	}
	checkDockerResizedData(t, ctx, m, next, read, env)
	checkDockerRejectedResize(t, ctx, m, next, op, read, env)
}

// checkDockerResizedData reads pre-maintenance data and checks Docker's enforced limits.
func checkDockerResizedData(t *testing.T, ctx context.Context, m *Manager, next model.ResourceSpec, read, env []string) {
	t.Helper()
	data, err := m.execute(ctx, next.Name, read, env)
	// Database contents must remain available under the same credentials.
	if err != nil || !strings.Contains(string(data), "persisted-value") {
		t.Fatalf("data after resize: %v %s", err, data)
	}
	current, err := m.inspect(ctx, next.Name)
	// Require successful resource observation before advancing maintenance.
	if err != nil {
		t.Fatal(err)
	}
	expected := dockerAllocation(next)
	// Compare native observed enforcement fields rather than desired state alone.
	for _, key := range []string{"NanoCpus", "Memory"} {
		// Require successful resource observation before advancing maintenance.
		if current.HostConfig[key] != float64(expected[key].(int64)) {
			t.Fatalf("%s observed %v expected %v", key, current.HostConfig[key], expected[key])
		}
	}
	retained, err := m.find(next.Name)
	// Desired allocation must survive reopening the resource state file.
	if err != nil || retained.Spec != next {
		t.Fatal("desired allocation not retained", err)
	}
}

// checkDockerRejectedResize checks rejected low-memory maintenance rolls back without losing data.
func checkDockerRejectedResize(t *testing.T, ctx context.Context, m *Manager, prior model.ResourceSpec, op model.ResourceContext, read, env []string) {
	t.Helper()
	invalid := prior
	invalid.Memory = "1Mi"
	// Docker rejects allocations below its minimum before database readiness can succeed.
	if _, err := m.Resize(ctx, invalid, op); err == nil {
		t.Fatal("Docker accepted allocation below its minimum")
	}
	checkDockerResizedData(t, ctx, m, prior, read, env)
}
