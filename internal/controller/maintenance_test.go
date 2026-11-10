package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// maintenanceProvider records admission at the privileged provider boundary.
type maintenanceProvider struct {
	previews, starts atomic.Int32
	pending          atomic.Bool
	started, release chan struct{}
}

// PreviewPreparation shares the provider's inspection counter and never starts a host operation.
func (m *maintenanceProvider) PreviewPreparation(_ context.Context, backup string) (json.RawMessage, error) {
	m.previews.Add(1)
	return json.Marshal(map[string]any{"backupDirectory": backup, "errors": []string{"external cluster active"}})
}

// PreviewMigration waits only when exercising lock-free inspection.
func (m *maintenanceProvider) PreviewMigration(ctx context.Context, backup string) (json.RawMessage, error) {
	m.previews.Add(1)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if m.started != nil {
		close(m.started)
		// Bound the observation so lock retention becomes an explicit test failure.
		select {
		// Exercise this independent maintenance outcome with the same retained request.
		case <-ctx.Done():
			return nil, ctx.Err()
		// Exercise this independent maintenance outcome with the same retained request.
		case <-m.release:
		}
	}
	return json.Marshal(map[string]any{"to": "k3s", "backupDirectory": backup, "errors": []string{"fixture blocker"}})
}

// PreviewUpdate returns a concrete server-retained review independent of caller payload.
func (m *maintenanceProvider) PreviewUpdate(context.Context, string, int64) (model.ControllerUpdatePlan, error) {
	return model.ControllerUpdatePlan{ID: "review", Fingerprint: "exact", Version: "v1.2.3", Changes: []string{"verified release"}}, nil
}

// ReviewUpdate rejects replacement fingerprints at the independent approval boundary.
func (m *maintenanceProvider) ReviewUpdate(_ context.Context, _ string, _ int64, input model.ControllerUpdateRequest) (model.ControllerUpdatePlan, error) {
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if input.Fingerprint != "exact" {
		return model.ControllerUpdatePlan{}, model.Err("STALE_PLAN", "Review changed")
	}
	return m.PreviewUpdate(context.Background(), "", 0)
}

// StartUpdate records only authorized dispatch after exact-request approval consumption.
func (m *maintenanceProvider) StartUpdate(context.Context, string, int64, model.ControllerUpdateRequest) (model.ControllerUpdateJob, error) {
	m.starts.Add(1)
	m.pending.Store(true)
	return model.ControllerUpdateJob{ID: "job", Version: "v1.2.3", Status: "queued"}, nil
}

// UpdateStatus supplies the separately retained restart receipt.
func (m *maintenanceProvider) UpdateStatus(context.Context, string, string) (model.ControllerUpdateJob, error) {
	status := "succeeded"
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if m.pending.Load() {
		status = "running"
	}
	return model.ControllerUpdateJob{ID: "job", Status: status}, nil
}

// TestMaintenanceRequiresOwnerLogin verifies authorization before any host inspection or download.
func TestMaintenanceRequiresOwnerLogin(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	provider := &maintenanceProvider{}
	c.opts.Maintenance = provider
	paths := []string{"/v1/controller/capabilities", "/v1/controller/migration/plan?to=k3s&backupDir=/backup/new", "/v1/controller/migration/preparation?backupDir=/backup/preparation", "/v1/controller/update/plan", "/v1/controller/update/jobs/job"}
	// Check each maintenance case without sharing its accepted execution state.
	for _, path := range paths {
		status, _ := humanHTTP(t, c, false, "GET", path, nil, "", "")
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if status == 200 {
			t.Fatal("anonymous maintenance", path)
		}
	}
	scoped, _, _ := pairedFixture(t, c)
	// Check each maintenance case without sharing its accepted execution state.
	for _, path := range paths {
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if err := scoped.Request(context.Background(), "GET", path, nil, nil); err == nil {
			t.Fatal("scoped maintenance", path)
		}
	}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if provider.previews.Load() != 0 || provider.starts.Load() != 0 {
		t.Fatal("unauthorized provider invoked")
	}
}

// TestMaintenanceGRPCSavedLoginAndApproval exercises real signed native gRPC and retained exact approval.
func TestMaintenanceGRPCSavedLoginAndApproval(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	provider := &maintenanceProvider{}
	c.opts.Maintenance = provider
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	paired, _, _ := pairWithKey(t, c, deploymentScope{}, pub, key)
	api, _, _ := grpcFixture(t, c, client.EncodeDeviceCredential(paired.Token, key))
	ctx := context.Background()
	var preview map[string]any
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err := api.Request(ctx, "GET", "/v1/controller/migration/plan?to=k3s&backupDir=/backup/new", nil, &preview); err != nil {
		t.Fatal(err)
	}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if preview["to"] != "k3s" || provider.previews.Load() != 1 {
		t.Fatal(preview)
	}
	// The new route uses the same signed gRPC login while remaining read-only.
	if err := api.Request(ctx, "GET", "/v1/controller/migration/preparation?backupDir=/backup/preparation", nil, &preview); err != nil {
		t.Fatal(err)
	}
	// Preparation inspection cannot submit an update or runtime migration.
	if provider.previews.Load() != 2 || provider.starts.Load() != 0 {
		t.Fatal("preparation crossed the mutation boundary")
	}
	exerciseMaintenanceApproval(t, api, provider)
	// New protected work is rejected while the detached job reserves the controller.
	status, _ := humanHTTP(t, c, true, "PUT", "/v1/secrets/new", map[string]string{"value": "fixture", "requestId": "fixture-write"}, "", "")
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if status == 200 {
		t.Fatal("mutation raced update")
	}
	provider.pending.Store(false)
	status, _ = humanHTTP(t, c, true, "PUT", "/v1/secrets/new", map[string]string{"value": "fixture", "requestId": "fixture-write"}, "", "")
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if status != 200 {
		t.Fatal("terminal receipt did not release reservation", status)
	}
}

// exerciseMaintenanceApproval checks exact proposal consumption and encrypted receipt replay over gRPC.
func exerciseMaintenanceApproval(t *testing.T, api *client.Client, provider *maintenanceProvider) {
	t.Helper()
	ctx := context.Background()
	input := model.ControllerUpdateRequest{ID: "review", Fingerprint: "exact", RequestID: client.RequestID()}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err := api.Request(ctx, "POST", "/v1/controller/update/apply", input, nil); err == nil {
		t.Fatal("direct remote update accepted")
	}
	var approval map[string]any
	body := map[string]any{"method": "POST", "path": "/v1/controller/update/apply", "body": input}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err := api.Request(ctx, "POST", "/v1/approvals", body, &approval); err != nil {
		t.Fatal(err)
	}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if approval["status"] != "approved" {
		t.Fatal(approval)
	}
	path := "/v1/approvals/" + approval["id"].(string) + "/execute"
	// Check each maintenance case without sharing its accepted execution state.
	for i := 0; i < 2; i++ {
		var job model.ControllerUpdateJob
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if err := api.Request(ctx, "POST", path, map[string]any{}, &job); err != nil {
			t.Fatal(err)
		}
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if job.Status != "queued" {
			t.Fatal(job)
		}
	}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if provider.starts.Load() != 1 {
		t.Fatal("duplicate privileged dispatch")
	}
}

// TestMaintenanceInspectionDoesNotLockController verifies expensive previews permit ordinary status traffic.
func TestMaintenanceInspectionDoesNotLockController(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	provider := &maintenanceProvider{started: make(chan struct{}), release: make(chan struct{})}
	c.opts.Maintenance = provider
	api, _, _ := pairedScopeFixture(t, c, deploymentScope{})
	done := make(chan error, 1)
	// Keep this fixture callback bound to the controlled maintenance outcome.
	go func() {
		done <- api.Request(context.Background(), "GET", "/v1/controller/migration/plan?to=k3s", nil, nil)
	}()
	<-provider.started
	// A short independent deadline exposes accidental retention of the aggregate lock.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status := make(chan error, 1)
	// Keep this fixture callback bound to the controlled maintenance outcome.
	go func() { status <- api.Request(ctx, "GET", "/v1/status", nil, nil) }()
	// Bound the observation so lock retention becomes an explicit test failure.
	select {
	// Exercise this independent maintenance outcome with the same retained request.
	case err := <-status:
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if err != nil {
			t.Fatal(err)
		}
	// Exercise this independent maintenance outcome with the same retained request.
	case <-ctx.Done():
		close(provider.release)
		t.Fatal("preview held controller lock")
	}
	close(provider.release)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
