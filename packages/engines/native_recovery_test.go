package engines

import (
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDockerNativeInterruptedRecovery observes owned native effects after a lost executable response.
func TestDockerNativeInterruptedRecovery(t *testing.T) {
	// Native provisioning is explicitly opt-in because it creates retained Docker storage.
	if os.Getenv("HOSTLIER_TEST_DOCKER") != "1" {
		t.Skip("set HOSTLIER_TEST_DOCKER=1 for native ownership recovery")
	}
	// Both engines must prove generated credentials and controller-scoped runtime ownership.
	for _, engine := range []string{"postgres", "redis"} {
		t.Run(engine, func(t *testing.T) { testNativeRecovery(t, engine) }) // Isolate retained resources and approvals per engine.
	}
}

// testNativeRecovery discards apply output before authoritative inspection and rejects mismatched credentials.
func testNativeRecovery(t *testing.T, engine string) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	// Native state paths must use the same canonical owner scope across executable restarts.
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req := workflow.Request{Version: 1, Action: "plan", Key: "native-recovery/db", Step: workflow.Step{ID: "db", Package: engine, Config: map[string]any{"mode": "new-instance", "storage": "1Gi"}}, Host: &workflow.HostContext{Runtime: "docker", StateDir: dir}}
	p := Package{Engine: engine}
	plan, err := p.Handle(ctx, req)
	// Planning generates protected credentials without resource creation.
	if err != nil {
		t.Fatal(err)
	}
	req.Prepared = plan.Prepared.Payload
	assertInspection(t, ctx, p, req, workflow.Uncertain)
	req.Action = "apply"
	installNativeCleanup(t, req)
	// The successful result is deliberately discarded to model a lost package process response.
	if _, err = p.Handle(ctx, req); err != nil {
		t.Fatal(err)
	}
	assertInspection(t, ctx, p, req, workflow.Completed)
	var changed payload
	json.Unmarshal(req.Prepared, &changed)
	changed.Credentials.Password = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	req.Prepared, _ = json.Marshal(changed)
	assertInspection(t, ctx, p, req, workflow.Uncertain)
}

// installNativeCleanup purges only the exact resource owned by this temporary controller state root.
func installNativeCleanup(t *testing.T, req workflow.Request) {
	t.Helper()
	// Purge only disposable resources inside this temporary controller owner scope.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m, err := manager(req)
		// Preserve the primary failure when native setup did not reach resource creation.
		if err != nil {
			return
		}
		var approved payload
		json.Unmarshal(req.Prepared, &approved)
		// Explicit test cleanup is the sole destructive operation in this recovery fixture.
		_ = m.Action(ctx, localName(req), "delete", model.ResourceContext{Credentials: approved.Credentials, Purge: true, OperationID: req.Key})
	})
}
