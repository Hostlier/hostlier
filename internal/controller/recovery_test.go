package controller

import (
	"bytes"
	"context"
	"errors"
	"hostlier/internal/model"
	"hostlier/internal/recovery"
	"sync"
	"testing"
	"time"
)

// memoryObjects injects remote corruption and upload failure without delivering data outside the fixture.
type memoryObjects struct {
	mu               sync.Mutex
	data             map[string][]byte
	failPut, corrupt bool
	deletes          int
}

// Put implements the isolated recovery test fixture contract.
func (m *memoryObjects) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Simulate unavailable storage without replacing an existing recovery point.
	if m.failPut {
		return errors.New("upload unavailable")
	}
	// Fail the fixture if the recovery invariant is violated.
	if m.data[key] != nil {
		return errors.New("immutable object exists")
	}
	m.data[key] = append([]byte{}, data...)
	return nil
}

// Get implements the isolated recovery test fixture contract.
func (m *memoryObjects) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data := append([]byte{}, m.data[key]...)
	// Fail the fixture if the recovery invariant is violated.
	if len(data) == 0 {
		return nil, errors.New("missing")
	}
	// Inject corruption only after the object has been uploaded.
	if m.corrupt {
		data[len(data)-1] ^= 1
	}
	return data, nil
}

// Delete implements the isolated recovery test fixture contract.
func (m *memoryObjects) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes++
	delete(m.data, key)
	return nil
}

// List implements the isolated recovery test fixture contract.
func (m *memoryObjects) List(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := []string{}
	// Exercise each native recovery case independently.
	for key := range m.data {
		keys = append(keys, key)
	}
	return keys, nil
}

// recoveryRuntime models independent native data stores, versions and durable fencing decisions.
type recoveryRuntime struct {
	mu           sync.Mutex
	records      map[string]model.Resource
	specs        map[string]model.ResourceSpec
	data         map[string][]byte
	fences       map[string]bool
	restoreFails bool
}

// Name implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Name() string { return "docker" }

// Plan implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Plan(_ context.Context, s model.ResourceSpec) (model.ResourcePlan, error) {
	// Fail the fixture if the recovery invariant is violated.
	if s.Name == "" {
		return model.ResourcePlan{}, errors.New("missing name")
	}
	// Fail the fixture if the recovery invariant is violated.
	if s.Image == "" {
		s.Image = "source-pinned"
	}
	return model.ResourcePlan{Spec: s}, nil
}

// Provision implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Provision(_ context.Context, s model.ResourceSpec, c model.ResourceContext) (model.Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := model.Resource{Name: s.Name, Type: s.Type, RuntimeID: s.Name, Status: "running"}
	m.records[s.Name] = r
	m.specs[s.Name] = s
	m.data[s.Name] = []byte("valid:empty")
	return r, nil
}

// Action implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Action(_ context.Context, name, action string, c model.ResourceContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.records[name]
	// Fail the fixture if the recovery invariant is violated.
	if action == "delete" {
		delete(m.records, name)
		delete(m.data, name)
		delete(m.fences, name)
		return nil
	}
	r.Status = "running"
	// Fail the fixture if the recovery invariant is violated.
	if action == "stop" {
		r.Status = "stopped"
	}
	m.records[name] = r
	return nil
}

// Status implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Status(_ context.Context, name string) (model.Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[name]
	// Fail the fixture if the recovery invariant is violated.
	if !ok {
		return r, errors.New("missing resource")
	}
	return r, nil
}

// Connection implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Connection(context.Context, string, model.ResourceCredentials) (map[string]string, error) {
	return map[string]string{"url": "private"}, nil
}

// Logs implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Logs(context.Context, string) (string, error) { return "", nil }

// Backup implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Backup(_ context.Context, name string, c model.ResourceContext) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte{}, m.data[name]...), nil
}

// Restore implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Restore(_ context.Context, name string, data []byte, c model.ResourceContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Fail the fixture if the recovery invariant is violated.
	if m.restoreFails {
		return errors.New("restore failed")
	}
	m.data[name] = append([]byte{}, data...)
	return nil
}

// Evidence implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Evidence(_ context.Context, name string, c model.ResourceContext) (model.DatabaseEvidence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	version := "16.1"
	// Fail the fixture if the recovery invariant is violated.
	if model.ApprovedDatabaseImage("postgres", m.specs[name].Image) {
		version = "17.1"
	}
	return model.DatabaseEvidence{Engine: m.records[name].Type, Version: version, Digest: recovery.Digest(m.data[name])}, nil
}

// Fence implements the isolated recovery test fixture contract.
func (m *recoveryRuntime) Fence(_ context.Context, name string, on bool, c model.ResourceContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fences[name] = on
	return nil
}

// recoveryFixture builds a real durable controller around deterministic storage and native-engine fault injectors.
func recoveryFixture(t *testing.T) (*humanFixture, *recoveryRuntime, *memoryObjects, *recovery.Destination) {
	t.Helper()
	manager := &recoveryRuntime{records: map[string]model.Resource{}, specs: map[string]model.ResourceSpec{}, data: map[string][]byte{}, fences: map[string]bool{}}
	objects := &memoryObjects{data: map[string][]byte{}}
	destination := &recovery.Destination{ID: "offsite", Endpoint: "https://s3.example", Region: "fixture", Bucket: "backups", Prefix: "database", Key: bytes.Repeat([]byte{7}, 32), Objects: objects}
	c, err := New(Options{StateDir: t.TempDir(), Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: &recordingRuntime{}, Hostname: "controller.example.com", ResourceManager: manager, RecoveryDestinations: map[string]*recovery.Destination{"offsite": destination}})
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal(err)
	}
	// Keep the callback bound to this fixture and its cleanup lifetime.
	t.Cleanup(func() { closeController(t, c) })
	spec := model.ResourceSpec{Name: "db", Type: "postgres", Image: "source-pinned", Storage: "1Gi"}
	// Stop the fixture on an unexpected operation result.
	if _, _, err = c.provisionResource(context.Background(), spec, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	manager.data["db"] = []byte("valid:original durable data")
	return attachHumanFixture(t, c), manager, objects, destination
}

// recoveryApproved uses an independent real cryptographic proof for every test mutation.
func recoveryApproved(t *testing.T, f *humanFixture, path string, body any) map[string]any {
	t.Helper()
	a := f.propose(t, "POST", path, body)
	status, result := approveFixtureRequest(t, f, a)
	// Fail the fixture if the recovery invariant is violated.
	if status != 200 {
		t.Fatal(status, result)
	}
	return result
}

// waitRecovery observes durable job state instead of assuming worker scheduling order.
func waitRecovery(t *testing.T, c *Controller, id string, want string) *RecoveryJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	// Exercise each native recovery case independently.
	for time.Now().Before(deadline) {
		c.mu.Lock()
		copy := *c.state.RecoveryJobs[id]
		c.mu.Unlock()
		// Fail the fixture if the recovery invariant is violated.
		if copy.Status != "queued" && copy.Status != "running" {
			// Fail the fixture if the recovery invariant is violated.
			if copy.Status != want {
				t.Fatalf("job=%+v want=%s", copy, want)
			}
			return &copy
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("recovery timeout")
	return nil
}

// TestRecoveryBackupImportRetentionAndUploadFailure exercises complete authenticated publication and conservative deletion.
func TestRecoveryBackupImportRetentionAndUploadFailure(t *testing.T) {
	f, _, objects, d := recoveryFixture(t)
	recoveryApproved(t, f, "/v1/backup-policies", map[string]any{"name": "daily", "resource": "db", "destination": "offsite", "destinationFingerprint": d.Fingerprint(), "everySeconds": 3600, "retain": 1})
	first := recoveryApproved(t, f, "/v1/backup-policies/daily/run", map[string]any{})
	waitRecovery(t, f.c, first["id"].(string), "succeeded")
	second := recoveryApproved(t, f, "/v1/backup-policies/daily/run", map[string]any{})
	job := waitRecovery(t, f.c, second["id"].(string), "succeeded")
	objects.mu.Lock()
	count, deletes := len(objects.data), objects.deletes
	objects.mu.Unlock()
	// Fail the fixture if the recovery invariant is violated.
	if count != 1 || deletes != 1 {
		t.Fatal("retention", count, deletes)
	}
	imported := recoveryApproved(t, f, "/v1/backup-imports", map[string]any{"destination": "offsite", "destinationFingerprint": d.Fingerprint(), "objectKey": job.ObjectKey})
	// Fail the fixture if the recovery invariant is violated.
	if imported["type"] != "postgres" {
		t.Fatal(imported)
	}
	objects.mu.Lock()
	objects.failPut = true
	objects.mu.Unlock()
	failed := recoveryApproved(t, f, "/v1/backup-policies/daily/run", map[string]any{})
	waitRecovery(t, f.c, failed["id"].(string), "failed")
	objects.mu.Lock()
	defer objects.mu.Unlock()
	// Fail the fixture if the recovery invariant is violated.
	if objects.deletes != 1 || len(objects.data) != 1 {
		t.Fatal("failed upload deleted a recovery point")
	}
}

// TestRecoveryUpgradeRequiresSeparateCutoverAndLeavesSourceStopped verifies the two human authority boundaries.
func TestRecoveryUpgradeRequiresSeparateCutoverAndLeavesSourceStopped(t *testing.T) {
	f, manager, _, d := recoveryFixture(t)
	accepted := recoveryApproved(t, f, "/v1/upgrades", map[string]any{"resource": "db", "target": "next", "to": "17", "destination": "offsite", "destinationFingerprint": d.Fingerprint()})
	id := accepted["id"].(string)
	waitRecovery(t, f.c, id, "awaiting-cutover")
	status, _ := humanHTTP(t, f.c, false, "POST", "/v1/recovery-jobs/"+id+"/cutover", map[string]any{}, f.token, "")
	// Fail the fixture if the recovery invariant is violated.
	if status != 403 {
		t.Fatal("bearer cutover accepted")
	}
	manager.mu.Lock()
	fenced := manager.fences["db"] && manager.fences["next"]
	manager.mu.Unlock()
	// Fail the fixture if the recovery invariant is violated.
	if !fenced {
		t.Fatal("writers not fenced")
	}
	recoveryApproved(t, f, "/v1/recovery-jobs/"+id+"/cutover", map[string]any{})
	waitRecovery(t, f.c, id, "succeeded")
	manager.mu.Lock()
	defer manager.mu.Unlock()
	// Fail the fixture if the recovery invariant is violated.
	if manager.records["db"].Status != "stopped" || manager.fences["next"] {
		t.Fatal("unsafe cutover")
	}
}

// TestRecoveryFailureRequiresAbortBeforeSourceReopens ensures failed verification cannot reconnect either writer implicitly.
func TestRecoveryFailureRequiresAbortBeforeSourceReopens(t *testing.T) {
	f, manager, _, d := recoveryFixture(t)
	manager.restoreFails = true
	accepted := recoveryApproved(t, f, "/v1/upgrades", map[string]any{"resource": "db", "target": "next", "to": "17", "destination": "offsite", "destinationFingerprint": d.Fingerprint()})
	id := accepted["id"].(string)
	waitRecovery(t, f.c, id, "failed")
	f.c.mu.Lock()
	reserved := f.c.resourceBusy("db")
	f.c.mu.Unlock()
	// Fail the fixture if the recovery invariant is violated.
	if !reserved {
		t.Fatal("failure released source")
	}
	recoveryApproved(t, f, "/v1/recovery-jobs/"+id+"/abort", map[string]any{})
	waitRecovery(t, f.c, id, "aborted")
	manager.mu.Lock()
	defer manager.mu.Unlock()
	// Fail the fixture if the recovery invariant is violated.
	if _, exists := manager.records["next"]; exists || manager.records["db"].Status != "running" || manager.fences["db"] {
		t.Fatal("abort did not restore single writer")
	}
}

// TestRecoveryRestartAndRevokedScheduleFailClosed keeps interrupted jobs reserved and prevents revoked scheduling authority.
func TestRecoveryRestartAndRevokedScheduleFailClosed(t *testing.T) {
	f, _, _, d := recoveryFixture(t)
	recoveryApproved(t, f, "/v1/backup-policies", map[string]any{"name": "daily", "resource": "db", "destination": "offsite", "destinationFingerprint": d.Fingerprint(), "everySeconds": 3600, "retain": 1})
	f.c.mu.Lock()
	policy := f.c.state.BackupPolicies["daily"]
	policy.NextRun = 0
	key := f.c.state.Human.Keys[policy.KeyFingerprint]
	key.Revoked = true
	f.c.state.Human.Keys[policy.KeyFingerprint] = key
	f.c.mu.Unlock()
	f.c.scheduleBackups()
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	// Fail the fixture if the recovery invariant is violated.
	if len(f.c.state.RecoveryJobs) != 0 {
		t.Fatal("revoked authority scheduled")
	}
	f.c.state.RecoveryJobs["interrupted"] = &RecoveryJob{ID: "interrupted", Status: "running", Source: "db"}
	f.c.initializeRecovery()
	// Stop the fixture on an unexpected operation result.
	if f.c.state.RecoveryJobs["interrupted"].Status != "uncertain" || !f.c.resourceBusy("db") {
		t.Fatal("interrupted work resumed")
	}
}

// TestRecoveryCorruptReadbackNeverRetainsOrDeletes proves that successful upload alone is not a verified recovery point.
func TestRecoveryCorruptReadbackNeverRetainsOrDeletes(t *testing.T) {
	f, _, objects, d := recoveryFixture(t)
	recoveryApproved(t, f, "/v1/backup-policies", map[string]any{"name": "daily", "resource": "db", "destination": "offsite", "destinationFingerprint": d.Fingerprint(), "everySeconds": 3600, "retain": 1})
	first := recoveryApproved(t, f, "/v1/backup-policies/daily/run", map[string]any{})
	waitRecovery(t, f.c, first["id"].(string), "succeeded")
	objects.mu.Lock()
	objects.corrupt = true
	objects.mu.Unlock()
	second := recoveryApproved(t, f, "/v1/backup-policies/daily/run", map[string]any{})
	waitRecovery(t, f.c, second["id"].(string), "failed")
	f.c.mu.Lock()
	points := len(f.c.state.RecoveryPoints)
	f.c.mu.Unlock()
	objects.mu.Lock()
	defer objects.mu.Unlock()
	// Fail the fixture if the recovery invariant is violated.
	if points != 1 || objects.deletes != 0 {
		t.Fatal("corrupt readback became verified or deleted surviving data")
	}
}

// TestRecoveryCutoverUncertaintyNeverPermitsAbort protects target writes after an interrupted network reconnection.
func TestRecoveryCutoverUncertaintyNeverPermitsAbort(t *testing.T) {
	job := &RecoveryJob{Kind: "upgrade", Status: "uncertain", CutoverStarted: true}
	// Stop the fixture on an unexpected operation result.
	if err := validateFinalization(job, "abort"); err == nil {
		t.Fatal("unsafe rollback after possible target writes")
	}
	// Stop the fixture on an unexpected operation result.
	if err := validateFinalization(job, "cutover"); err != nil {
		t.Fatal("cannot reconcile uncertain cutover", err)
	}
}

// TestRecoveryWrongKeyImportCreatesNoLocalBackup rejects even an approved import when its independent recovery key is unavailable.
func TestRecoveryWrongKeyImportCreatesNoLocalBackup(t *testing.T) {
	f, _, _, d := recoveryFixture(t)
	recoveryApproved(t, f, "/v1/backup-policies", map[string]any{"name": "daily", "resource": "db", "destination": "offsite", "destinationFingerprint": d.Fingerprint(), "everySeconds": 3600, "retain": 1})
	accepted := recoveryApproved(t, f, "/v1/backup-policies/daily/run", map[string]any{})
	job := waitRecovery(t, f.c, accepted["id"].(string), "succeeded")
	d.Key = bytes.Repeat([]byte{8}, 32)
	proposed := f.propose(t, "POST", "/v1/backup-imports", map[string]any{"destination": "offsite", "destinationFingerprint": d.Fingerprint(), "objectKey": job.ObjectKey})
	status, _ := approveFixtureRequest(t, f, proposed)
	// Human approval never substitutes for authenticated recovery data.
	if status == 200 {
		t.Fatal("wrong recovery key accepted")
	}
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	// Failed authentication must not publish an import identity.
	if len(f.c.state.Backups) != 0 {
		t.Fatal("invalid import was retained")
	}
}

// TestRecoveryRetentionReauthenticatesSurvivor refuses deletion when the surviving remote copy cannot be authenticated.
func TestRecoveryRetentionReauthenticatesSurvivor(t *testing.T) {
	f, _, objects, d := recoveryFixture(t)
	recoveryApproved(t, f, "/v1/backup-policies", map[string]any{"name": "daily", "resource": "db", "destination": "offsite", "destinationFingerprint": d.Fingerprint(), "everySeconds": 3600, "retain": 2})
	first := recoveryApproved(t, f, "/v1/backup-policies/daily/run", map[string]any{})
	waitRecovery(t, f.c, first["id"].(string), "succeeded")
	second := recoveryApproved(t, f, "/v1/backup-policies/daily/run", map[string]any{})
	job := waitRecovery(t, f.c, second["id"].(string), "succeeded")
	f.c.mu.Lock()
	f.c.state.BackupPolicies["daily"].Retain = 1
	f.c.mu.Unlock()
	objects.mu.Lock()
	objects.corrupt = true
	objects.mu.Unlock()
	// Inject the failure specifically at the retention boundary after successful earlier publication.
	if err := f.c.retainRecovery(context.Background(), job, d); err == nil {
		t.Fatal("unavailable survivor accepted")
	}
	objects.mu.Lock()
	defer objects.mu.Unlock()
	// Neither older copy may be deleted to satisfy a count after the survivor check fails.
	if objects.deletes != 0 || len(objects.data) != 2 {
		t.Fatal("retention removed a copy without a valid survivor")
	}
}
