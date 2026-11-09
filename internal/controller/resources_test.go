package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"hostlier/internal/model"
)

// recordingResources captures credentials and simulates provisioning or backup behavior.
type recordingResources struct {
	mu          sync.Mutex
	calls       int
	credentials map[string]model.ResourceCredentials
	fail        bool
	digest      string
	failCode    string
}

// Name supports name in the integration fixture.
func (r *recordingResources) Name() string { return "docker" }

// Plan supports plan in the integration fixture.
func (r *recordingResources) Plan(_ context.Context, s model.ResourceSpec) (model.ResourcePlan, error) {
	s.Image = "postgres@sha256:" + strings.Repeat("b", 64)
	s.PackageDigest = r.digest
	return model.ResourcePlan{Spec: s, Changes: map[string]any{"storageQuotaEnforced": false}}, nil
}

// Provision supports provision in the integration fixture.
func (r *recordingResources) Provision(_ context.Context, s model.ResourceSpec, c model.ResourceContext) (model.Resource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	// Fail immediately if fixture setup or execution fails.
	if r.credentials == nil {
		r.credentials = map[string]model.ResourceCredentials{}
	}
	r.credentials[s.Name] = c.Credentials
	// Fail immediately if fixture setup or execution fails.
	if r.fail {
		return model.Resource{}, r.provisionError(c.Credentials.Password)
	}
	return model.Resource{Name: s.Name, Type: s.Type, Status: "running", RuntimeID: s.Name, Network: "network-" + s.Name}, nil
}

// Action supports action in the integration fixture.
func (r *recordingResources) Action(context.Context, string, string, model.ResourceContext) error {
	return nil
}

// Status supports status in the integration fixture.
func (r *recordingResources) Status(_ context.Context, name string) (model.Resource, error) {
	return model.Resource{Name: name, Status: "running"}, nil
}

// Connection supports connection in the integration fixture.
func (r *recordingResources) Connection(_ context.Context, name string, c model.ResourceCredentials) (map[string]string, error) {
	return map[string]string{"url": "postgres://" + c.Username + ":" + c.Password + "@" + name + "/app"}, nil
}

// Logs supports logs in the integration fixture.
func (r *recordingResources) Logs(_ context.Context, name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return "diagnostic " + r.credentials[name].Password, nil
}

// SupportsDependencies supports supports dependencies in the integration fixture.
func (r *recordingRuntime) SupportsDependencies() bool { return true }

// resourceController supports resource controller in the integration fixture.
func resourceController(t *testing.T, dir string, r *recordingRuntime, m *recordingResources) *Controller {
	t.Helper()
	c, err := New(Options{StateDir: dir, Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: r, ResourceManager: m})
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// resourceApply supports resource apply in the integration fixture.
func resourceApply(t *testing.T, c *Controller, plan map[string]any, key string) map[string]any {
	t.Helper()
	status, op := call(t, c, true, "POST", "/v1/plans/"+plan["id"].(string)+"/apply", map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": key}, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(op)
	}
	return wait(t, c, op["operationId"].(string))
}

// TestResourceApprovalCredentialsDependencyBindingAndRetention verifies resource approval credentials dependency binding and retention.
func TestResourceApprovalCredentialsDependencyBindingAndRetention(t *testing.T) {
	dir := t.TempDir()
	manager := &recordingResources{}
	runtime := &recordingRuntime{}
	c := resourceController(t, dir, runtime, manager)
	status, plan := call(t, c, true, "POST", "/v1/resource-plans", map[string]any{"spec": model.ResourceSpec{Name: "shared", Type: "postgres", Storage: "1Gi"}}, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(plan)
	}
	// Guard against planning generated credentials or mutated runtime.
	if manager.calls != 0 || len(c.state.ResourceCredentials) != 0 {
		t.Fatal("planning generated credentials or mutated runtime")
	}
	path := "/v1/plans/" + plan["id"].(string) + "/apply"
	status, _ = call(t, c, true, "POST", path, map[string]any{"fingerprint": plan["fingerprint"], "requestId": "missing"}, "")
	// Require the expected HTTP status for this request.
	if status != 403 {
		t.Fatal(status)
	}
	// Require the expected operation state.
	if op := resourceApply(t, c, plan, "create"); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	manager.mu.Lock()
	password := manager.credentials["shared"].Password
	manager.mu.Unlock()
	// Guard against insufficient password entropy.
	if len(password) < 64 {
		t.Fatal("insufficient password entropy")
	}
	closeController(t, c)
	c = resourceController(t, dir, runtime, manager)
	defer closeController(t, c)
	_, replayed := call(t, c, true, "POST", path, map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": "create"}, "")
	// Fail immediately if fixture setup or execution fails.
	if replayed["operationId"] == nil {
		t.Fatal(replayed)
	}
	manager.mu.Lock()
	// Guard against replay reprovisioned.
	if manager.calls != 1 {
		t.Fatal("replay reprovisioned")
	}
	manager.mu.Unlock()
	assertResourceBindingAndRedaction(t, c, runtime, password, dir)
	assertResourceCredentialRetention(t, c)
}

// TestResourceProvisionFailureRetainsStableProtectedCredentials verifies resource provision failure retains stable protected credentials.
func TestResourceProvisionFailureRetainsStableProtectedCredentials(t *testing.T) {
	dir := t.TempDir()
	manager := &recordingResources{fail: true}
	c := resourceController(t, dir, &recordingRuntime{}, manager)
	_, plan := call(t, c, true, "POST", "/v1/resource-plans", map[string]any{"spec": model.ResourceSpec{Name: "database", Type: "postgres", Storage: "1Gi"}}, "")
	failed := resourceApply(t, c, plan, "fail")
	// Require the expected operation state.
	if failed["status"] != "failed" {
		t.Fatal(failed)
	}
	manager.mu.Lock()
	password := manager.credentials["database"].Password
	manager.mu.Unlock()
	raw, _ := json.Marshal(failed)
	// Guard against error leaked password.
	if strings.Contains(string(raw), password) {
		t.Fatal("error leaked password")
	}
	closeController(t, c)
	manager.fail = false
	c = resourceController(t, dir, &recordingRuntime{}, manager)
	defer closeController(t, c)
	_, retry := call(t, c, true, "POST", "/v1/resource-plans", map[string]any{"spec": model.ResourceSpec{Name: "database", Type: "postgres", Storage: "1Gi"}}, "")
	resourceApply(t, c, retry, "retry")
	manager.mu.Lock()
	defer manager.mu.Unlock()
	// Guard against retry rotated credentials.
	if manager.credentials["database"].Password != password {
		t.Fatal("retry rotated credentials")
	}
}

// TestControllerStartupLogsAreProtected verifies controller startup logs are protected.
func TestControllerStartupLogsAreProtected(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "hostlier.log")
	c, err := New(Options{StateDir: filepath.Join(dir, "state"), Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: &recordingRuntime{}, LogPath: logPath})
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	closeController(t, c)
	data, err := os.ReadFile(logPath)
	// Fail immediately if fixture setup or execution fails.
	if err != nil || !strings.Contains(string(data), "Controller initialized") || !strings.Contains(string(data), "Controller shutdown") {
		t.Fatal(string(data), err)
	}
	st, _ := os.Stat(logPath)
	// Fail immediately if fixture setup or execution fails.
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}

// Backup supports backup in the integration fixture.
func (r *recordingResources) Backup(context.Context, string, model.ResourceContext) ([]byte, error) {
	return []byte("unique-confidential-backup-data"), nil
}

// Restore supports restore in the integration fixture.
func (r *recordingResources) Restore(_ context.Context, name string, data []byte, c model.ResourceContext) error {
	// Fail immediately if fixture setup or execution fails.
	if c.OperationID == "" || name != "restored" || string(data) != "unique-confidential-backup-data" {
		return model.Err("RESTORE_FAILED", "Invalid approved restore")
	}
	return nil
}

// TestBackupsEncryptedExportAndApprovedSeparateRestore verifies backups encrypted export and approved separate restore.
func TestBackupsEncryptedExportAndApprovedSeparateRestore(t *testing.T) {
	dir := t.TempDir()
	c := resourceController(t, dir, &recordingRuntime{}, &recordingResources{})
	defer closeController(t, c)
	_, plan := call(t, c, true, "POST", "/v1/resource-plans", map[string]any{"spec": model.ResourceSpec{Name: "source", Type: "postgres", Storage: "1Gi"}}, "")
	resourceApply(t, c, plan, "source")
	status, backup := call(t, c, true, "POST", "/v1/resources/source/backups", map[string]any{}, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(backup)
	}
	id := backup["backupId"].(string)
	raw, err := os.ReadFile(filepath.Join(dir, id+".backup"))
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	// Guard against plaintext backup stored.
	if strings.Contains(string(raw), "unique-confidential-backup-data") {
		t.Fatal("plaintext backup stored")
	}
	st, _ := os.Stat(filepath.Join(dir, id+".backup"))
	// Fail immediately if fixture setup or execution fails.
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	assertBackupExportAndRestore(t, c, id)
}

// TestOfflineNeo4jBackupBlocksActiveDependents verifies offline neo4j backup blocks active dependents.
func TestOfflineNeo4jBackupBlocksActiveDependents(t *testing.T) {
	manager := &recordingResources{}
	c := resourceController(t, t.TempDir(), &recordingRuntime{}, manager)
	defer closeController(t, c)
	c.mu.Lock()
	c.state.Resources["graph"] = &ResourceRecord{Resource: model.Resource{Name: "graph", Type: "neo4j", Status: "running"}, Spec: model.ResourceSpec{Name: "graph", Type: "neo4j", Storage: "1Gi"}}
	c.state.Applications["dependent"] = &Application{Deployment: model.Deployment{Name: "dependent", Status: "running"}, ResourceRefs: map[string]string{"graph": "graph"}}
	c.mu.Unlock()
	status, result := call(t, c, true, "POST", "/v1/resources/graph/backups", map[string]any{}, "")
	// Require the expected HTTP status for this request.
	if status != 409 || code(result) != "RESOURCE_IN_USE" {
		t.Fatal(status, result)
	}
}

// assertResourceBindingAndRedaction checks private dependency bindings, deletion protection and credential redaction.
func assertResourceBindingAndRedaction(t *testing.T, c *Controller, runtime *recordingRuntime, password, dir string) {
	t.Helper()
	spec := project()
	spec.Dependencies = map[string]model.Dependency{"db": {Type: "postgres", Source: "existing", Instance: "shared"}}
	spec.Bindings = map[string]string{"DATABASE_URL": "db.url"}
	status, appPlan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(appPlan)
	}
	// Require the expected operation state.
	if op := resourceApply(t, c, appPlan, "app"); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	runtime.mu.Lock()
	// Guard against binding missing protected value.
	if !strings.Contains(runtime.values["DATABASE_URL"], password) {
		t.Fatal("binding missing protected value")
	}
	runtime.mu.Unlock()
	status, blocked := call(t, c, true, "POST", "/v1/resources/shared/action", map[string]any{"action": "delete", "purge": true}, "")
	// Require the expected HTTP status for this request.
	if status != 409 || code(blocked) != "RESOURCE_IN_USE" {
		t.Fatal(status, blocked)
	}
	_, logs := call(t, c, true, "GET", "/v1/resources/shared/logs", nil, "")
	// Guard against resource log leaked password.
	if strings.Contains(logs["text"].(string), password) {
		t.Fatal("resource log leaked password")
	}
	_, listing := call(t, c, true, "GET", "/v1/resources", nil, "")
	raw, _ := json.Marshal(listing)
	// Guard against resource metadata leaked password.
	if strings.Contains(string(raw), password) {
		t.Fatal("resource metadata leaked password")
	}
	data := controllerSnapshot(t, c)
	// Guard against plaintext resource credential persisted.
	if strings.Contains(string(data), password) || strings.Contains(string(data), "postgres://") {
		t.Fatal("plaintext resource credential persisted")
	}
}

// assertResourceCredentialRetention checks that deletion retains credentials until explicit purge.
func assertResourceCredentialRetention(t *testing.T, c *Controller) {
	t.Helper()
	_, deleted := call(t, c, true, "POST", "/v1/apps/sample/action", map[string]any{"action": "delete"}, "")
	wait(t, c, deleted["operationId"].(string))
	_, resourceDeleted := call(t, c, true, "POST", "/v1/resources/shared/action", map[string]any{"action": "delete"}, "")
	wait(t, c, resourceDeleted["operationId"].(string))
	c.mu.Lock()
	// Guard against default deletion discarded credentials.
	if c.state.ResourceCredentials["shared"] == "" {
		t.Fatal("default deletion discarded credentials")
	}
	c.mu.Unlock()
	_, purge := call(t, c, true, "POST", "/v1/resources/shared/action", map[string]any{"action": "delete", "purge": true}, "")
	wait(t, c, purge["operationId"].(string))
	c.mu.Lock()
	defer c.mu.Unlock()
	// Guard against purge retained credentials.
	if c.state.ResourceCredentials["shared"] != "" {
		t.Fatal("purge retained credentials")
	}
}

// assertBackupExportAndRestore checks authorized export and approval of restoration into a separate resource.
func assertBackupExportAndRestore(t *testing.T, c *Controller, id string) {
	t.Helper()
	status, _ := call(t, c, false, "GET", "/v1/backups/"+id+"/export", nil, "")
	// Require the expected HTTP status for this request.
	if status != 401 {
		t.Fatal(status)
	}
	status, export := call(t, c, true, "GET", "/v1/backups/"+id+"/export", nil, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(export)
	}
	decoded, err := base64.StdEncoding.DecodeString(export["data"].(string))
	// Fail immediately if fixture setup or execution fails.
	if err != nil || string(decoded) != "unique-confidential-backup-data" {
		t.Fatal(string(decoded), err)
	}
	status, _ = call(t, c, true, "POST", "/v1/resource-plans", map[string]any{"spec": model.ResourceSpec{Name: "source", Type: "postgres", Storage: "1Gi"}, "restoreBackupId": id}, "")
	// Guard against restore over source accepted.
	if status == 200 {
		t.Fatal("restore over source accepted")
	}
	status, restorePlan := call(t, c, true, "POST", "/v1/resource-plans", map[string]any{"spec": model.ResourceSpec{Name: "restored", Type: "postgres", Storage: "1Gi"}, "restoreBackupId": id}, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(restorePlan)
	}
	// Require the expected operation state.
	if op := resourceApply(t, c, restorePlan, "restore"); op["status"] != "succeeded" {
		t.Fatal(op)
	}
}

// provisionError selects a safe failure category without weakening credential redaction tests.
func (r *recordingResources) provisionError(password string) error {
	code := r.failCode
	// Readiness failures remain the default fixture behavior.
	if code == "" {
		code = "READINESS_FAILED"
	}
	return model.Err(code, password)
}
