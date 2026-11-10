package controllercmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/controller"
	"hostlier/internal/installer"
	"hostlier/internal/model"
)

// maintenanceFixture stages harmless payloads using the real durable receipt implementation.
func maintenanceFixture(t *testing.T) (*serverMaintenance, *int) {
	t.Helper()
	m := newServerMaintenance(t.TempDir(), "/etc/hostlier/config.yaml")
	m.executable = filepath.Join(t.TempDir(), "source")
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err := os.WriteFile(m.executable, []byte("original executable"), 0700); err != nil {
		t.Fatal(err)
	}
	// Keep this fixture callback bound to the controlled maintenance outcome.
	m.checkHost = func(string) error { return nil }
	// Keep this fixture callback bound to the controlled maintenance outcome.
	m.active = func(context.Context, string) (bool, error) { return true, nil }
	calls := new(int)
	// Keep this fixture callback bound to the controlled maintenance outcome.
	m.schedule = func(context.Context, string, string, string) error { *calls++; return nil }
	// Keep this fixture callback bound to the controlled maintenance outcome.
	m.prepare = func(_ context.Context, r *controllerUpdateRecord, dir string) error {
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if err := os.WriteFile(filepath.Join(dir, "hostlier-controller"), []byte("verified release"), 0700); err != nil {
			return err
		}
		r.TargetSHA256, _ = maintenanceHash(filepath.Join(dir, "hostlier-controller"))
		r.Plan.Version = "v1.2.3"
		r.Plan.Runtime = "docker"
		r.InstallerFingerprint = "reviewed-installer"
		return nil
	}
	return m, calls
}

// reviewedUpdate creates a concrete actor-bound fixture review using production staging logic.
func reviewedUpdate(t *testing.T, m *serverMaintenance) model.ControllerUpdateRequest {
	t.Helper()
	plan, err := m.PreviewUpdate(context.Background(), "owner", 7)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err != nil {
		t.Fatal(err)
	}
	return model.ControllerUpdateRequest{ID: plan.ID, Fingerprint: plan.Fingerprint, RequestID: client.RequestID()}
}

// TestRemoteUpdateReviewBinding rejects every substitution before scheduling privileged work.
func TestRemoteUpdateReviewBinding(t *testing.T) {
	cases := []string{"actor", "generation", "fingerprint", "expired", "request-id", "payload", "record"}
	// Check each maintenance case without sharing its accepted execution state.
	for _, kind := range cases {
		// Keep this fixture callback bound to the controlled maintenance outcome.
		t.Run(kind, func(t *testing.T) {
			m, calls := maintenanceFixture(t)
			input := reviewedUpdate(t, m)
			actor, generation := "owner", int64(7)
			dir, _ := m.stagePath(input.ID)
			actor, generation = substituteUpdateReview(t, m, dir, kind, &input, actor, generation)
			// Assert the expected maintenance receipt, authorization or recovery behavior.
			if _, err := m.StartUpdate(context.Background(), actor, generation, input); err == nil {
				t.Fatal("accepted substituted update")
			}
			// Assert the expected maintenance receipt, authorization or recovery behavior.
			if *calls != 0 {
				t.Fatal("scheduled rejected update")
			}
		})
	}
}

// substituteUpdateReview alters one independent trust binding before the scheduling attempt.
func substituteUpdateReview(t *testing.T, m *serverMaintenance, dir, kind string, input *model.ControllerUpdateRequest, actor string, generation int64) (string, int64) {
	t.Helper()
	// Alter only the trust binding selected by this fixture.
	switch kind {
	// Exercise this independent maintenance outcome with the same retained request.
	case "actor":
		actor = "other"
	// Exercise this independent maintenance outcome with the same retained request.
	case "generation":
		generation++
	// Exercise this independent maintenance outcome with the same retained request.
	case "fingerprint":
		input.Fingerprint = "substituted"
	// Exercise this independent maintenance outcome with the same retained request.
	case "expired":
		// Keep this fixture callback bound to the controlled maintenance outcome.
		m.now = func() time.Time { return time.Now().Add(time.Hour) }
	// Exercise this independent maintenance outcome with the same retained request.
	case "request-id":
		input.RequestID = "../../unsafe"
	// Exercise this independent maintenance outcome with the same retained request.
	case "payload":
		_ = os.WriteFile(filepath.Join(dir, "hostlier-controller"), []byte("changed"), 0700)
	// Exercise this independent maintenance outcome with the same retained request.
	case "record":
		r, _ := readControllerUpdate(dir)
		r.Plan.Runtime = "k3s"
		_ = saveControllerUpdate(dir, r)
	}
	return actor, generation
}

// TestRemoteUpdateDurableReceipt verifies a consumed plan cannot launch twice after daemon restart.
func TestRemoteUpdateDurableReceipt(t *testing.T) {
	m, calls := maintenanceFixture(t)
	input := reviewedUpdate(t, m)
	job, err := m.StartUpdate(context.Background(), "owner", 7, input)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err != nil || job.Status != "queued" || *calls != 1 {
		t.Fatal(job, err, *calls)
	}
	restarted := newServerMaintenance(m.stateDir, m.configPath)
	restarted.active = m.active
	// Keep this fixture callback bound to the controlled maintenance outcome.
	restarted.schedule = func(context.Context, string, string, string) error { t.Fatal("replayed schedule"); return nil }
	again, err := restarted.StartUpdate(context.Background(), "owner", 99, input)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err != nil || !reflect.DeepEqual(job, again) {
		t.Fatal(again, err)
	}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if _, err := restarted.UpdateStatus(context.Background(), "other", input.ID); err == nil {
		t.Fatal("foreign receipt disclosed")
	}
	input.RequestID = client.RequestID()
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if _, err := restarted.StartUpdate(context.Background(), "owner", 7, input); err == nil {
		t.Fatal("consumed plan accepted new intent")
	}
}

// TestRemoteUpdateLostRunnerNeverReplays verifies queued jobs become explicit interrupted receipts.
func TestRemoteUpdateLostRunnerNeverReplays(t *testing.T) {
	m, calls := maintenanceFixture(t)
	input := reviewedUpdate(t, m)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if _, err := m.StartUpdate(context.Background(), "owner", 7, input); err != nil {
		t.Fatal(err)
	}
	// Keep this fixture callback bound to the controlled maintenance outcome.
	m.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	// Keep this fixture callback bound to the controlled maintenance outcome.
	m.active = func(context.Context, string) (bool, error) { return false, nil }
	job, err := m.UpdateStatus(context.Background(), "owner", input.ID)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err != nil || job.Status != "interrupted" || job.Error == nil || *calls != 1 {
		t.Fatal(job, err)
	}
}

// TestRemoteUpdateSchedulingFailureIsRetained verifies a failed launch cannot be implicitly retried.
func TestRemoteUpdateSchedulingFailureIsRetained(t *testing.T) {
	m, _ := maintenanceFixture(t)
	input := reviewedUpdate(t, m)
	// Keep this fixture callback bound to the controlled maintenance outcome.
	m.schedule = func(context.Context, string, string, string) error { return errors.New("private diagnostics") }
	job, err := m.StartUpdate(context.Background(), "owner", 7, input)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err != nil || job.Status != "failed" || strings.Contains(job.Error.Message, "private diagnostics") {
		t.Fatal(job, err)
	}
	again, err := m.StartUpdate(context.Background(), "owner", 7, input)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err != nil || !reflect.DeepEqual(job, again) {
		t.Fatal(again, err)
	}
}

// TestControllerUpdateRunnerRecovery verifies stop/install/start ordering and independent cleanup after cancellation.
func TestControllerUpdateRunnerRecovery(t *testing.T) {
	// Check each maintenance case without sharing its accepted execution state.
	for _, kind := range []string{"success", "installer-failure", "stale", "cancelled"} {
		// Keep this fixture callback bound to the controlled maintenance outcome.
		t.Run(kind, func(t *testing.T) { testUpdateRunner(t, kind) })
	}
}

// testUpdateRunner drives a real durable job across the fixed service boundary with harmless fakes.
func testUpdateRunner(t *testing.T, kind string) {
	m, _ := maintenanceFixture(t)
	input := reviewedUpdate(t, m)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if _, err := m.StartUpdate(context.Background(), "owner", 7, input); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := []string{}
	deps := fixtureUpdateRunner(t, kind, input.ID, cancel, &calls)
	dir, _ := m.stagePath(input.ID)
	err := executeControllerUpdate(ctx, m.stateDir, dir, deps)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if (err == nil) != (kind == "success") {
		t.Fatal(err)
	}
	want := []string{"stop", "install", "start"}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if kind == "stale" {
		want = []string{"stop", "start"}
	}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if !reflect.DeepEqual(calls, want) {
		t.Fatal(calls)
	}
	receipt, readErr := m.UpdateStatus(context.Background(), "owner", input.ID)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if readErr != nil || updatePending(receipt.Status) {
		t.Fatal(receipt, readErr)
	}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if _, err := os.Stat(filepath.Join(dir, "runner")); err != nil {
		t.Fatal("old runner lost", err)
	}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err := executeControllerUpdate(context.Background(), m.stateDir, dir, deps); err == nil {
		t.Fatal("terminal runner replayed")
	}
}

// fixtureUpdateRunner isolates controlled service and installer outcomes from receipt assertions.
func fixtureUpdateRunner(t *testing.T, kind, id string, cancel context.CancelFunc, calls *[]string) updateRunnerDependencies {
	t.Helper()
	deps := updateRunnerDependencies{}
	// Keep this fixture callback bound to the controlled maintenance outcome.
	deps.service = func(ctx context.Context, action string) error {
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if action == "start" && ctx.Err() != nil {
			t.Fatal("recovery used cancelled context")
		}
		*calls = append(*calls, action)
		return nil
	}
	// Keep this fixture callback bound to the controlled maintenance outcome.
	deps.snapshot = func(_ string, state *controller.State) error {
		state.Generation = 7
		state.ControllerUpdateID = id
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if kind == "stale" {
			state.Generation++
		}
		return nil
	}
	// Keep this fixture callback bound to the controlled maintenance outcome.
	deps.install = func(ctx context.Context, _ string, r controllerUpdateRecord, _ io.Writer) error {
		*calls = append(*calls, "install")
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if r.InstallerFingerprint != "reviewed-installer" {
			t.Fatal("unbound installation")
		}
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if kind == "cancelled" {
			cancel()
			return ctx.Err()
		}
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if kind == "installer-failure" {
			return errors.New("secret subprocess diagnostic")
		}
		return nil
	}
	return deps
}

// TestInitReviewedFingerprintRejectsChangedPlan verifies the target release checks before any installation mutation.
func TestInitReviewedFingerprintRejectsChangedPlan(t *testing.T) {
	// Keep this fixture callback bound to the controlled maintenance outcome.
	deps := initDependencies{executable: "/fixture/controller", inspect: func(context.Context, installer.Options) (installer.Plan, error) {
		return installer.Plan{Supported: true, Fingerprint: "new-host-facts"}, nil
		// Keep this fixture callback bound to the controlled maintenance outcome.
	}, apply: func(context.Context, installer.Plan, installer.ApplyOptions) error {
		t.Fatal("stale plan applied")
		return nil
	}}
	err := runInit(context.Background(), []string{"--approved-fingerprint", "previous-review", "--yes"}, strings.NewReader(""), io.Discard, io.Discard, deps)
	assertInitError(t, err, "STALE_PLAN")
}

// TestUpdateNetworkInspectionDoesNotBlockReceipts verifies staging never indirectly locks controller dispatch.
func TestUpdateNetworkInspectionDoesNotBlockReceipts(t *testing.T) {
	m, _ := maintenanceFixture(t)
	input := reviewedUpdate(t, m)
	started, release := make(chan struct{}), make(chan struct{})
	prepare := m.prepare
	// Keep this fixture callback bound to the controlled maintenance outcome.
	m.prepare = func(ctx context.Context, r *controllerUpdateRecord, dir string) error {
		close(started)
		<-release
		return prepare(ctx, r, dir)
	}
	done := make(chan error, 1)
	// Keep this fixture callback bound to the controlled maintenance outcome.
	go func() { _, err := m.PreviewUpdate(context.Background(), "owner", 7); done <- err }()
	<-started
	reviewed := make(chan error, 1)
	// Keep this fixture callback bound to the controlled maintenance outcome.
	go func() { _, err := m.ReviewUpdate(context.Background(), "owner", 7, input); reviewed <- err }()
	// Bound the observation so lock retention becomes an explicit test failure.
	select {
	// Exercise this independent maintenance outcome with the same retained request.
	case err := <-reviewed:
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if err != nil {
			t.Fatal(err)
		}
	// Exercise this independent maintenance outcome with the same retained request.
	case <-time.After(time.Second):
		close(release)
		t.Fatal("release download blocked approval dispatch")
	}
	close(release)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestUpdatePreviewStagingLimit verifies abandoned reviews cannot fill the controller filesystem indefinitely.
func TestUpdatePreviewStagingLimit(t *testing.T) {
	m, _ := maintenanceFixture(t)
	// Check each maintenance case without sharing its accepted execution state.
	for i := 0; i < 3; i++ {
		reviewedUpdate(t, m)
	}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if _, err := m.PreviewUpdate(context.Background(), "owner", 7); err == nil {
		t.Fatal("unbounded staged releases")
	}
	// Keep this fixture callback bound to the controlled maintenance outcome.
	m.now = func() time.Time { return time.Now().Add(time.Hour) }
	reviewedUpdate(t, m)
	entries, err := os.ReadDir(filepath.Join(m.stateDir, "controller-updates"))
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err != nil || len(entries) != 1 {
		t.Fatal("expired reviews not reclaimed", len(entries), err)
	}
}

// TestUnknownRunnerStateRetainsReservation prevents a failed systemd query from
// reporting interruption while the independent installer may still be running.
func TestUnknownRunnerStateRetainsReservation(t *testing.T) {
	m, _ := maintenanceFixture(t)
	input := reviewedUpdate(t, m)
	// Start a real retained acceptance before simulating loss of systemd inspection.
	if _, err := m.StartUpdate(context.Background(), "owner", 7, input); err != nil {
		t.Fatal(err)
	}
	// Advance beyond the startup grace period without expiring the accepted execution.
	m.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	// Simulate a failed manager query without claiming the independent installer stopped.
	m.active = func(context.Context, string) (bool, error) { return false, errors.New("systemd unavailable") }
	job, err := m.UpdateStatus(context.Background(), "owner", input.ID)
	// Unknown unit ownership must retain queued/running status and block new mutations.
	if err != nil || !updatePending(job.Status) {
		t.Fatal(job, err)
	}
}
