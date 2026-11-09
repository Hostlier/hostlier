package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// TestConfigDomainsAuthoritativeAcrossRestartAndAPIPersist verifies config domains authoritative across restart and api persist.
func TestConfigDomainsAuthoritativeAcrossRestartAndAPIPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("version: 1\ndomains:\n  - example.com\n"), 0644)
	c, err := controllerFromConfigFile(dir, path)
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	_, oldPlan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": project()}, "")
	status, out := call(t, c, true, "POST", "/v1/domains", map[string]any{"domain": "tools.test"}, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(out)
	}
	cfg, err := config.ReadServer(path)
	// Fail immediately if fixture setup or execution fails.
	if err != nil || len(cfg.Domains) != 2 {
		t.Fatal(cfg, err)
	}
	closeController(t, c)
	os.WriteFile(path, []byte("version: 1\ndomains:\n  - example.com\n  - tools.test\n  - manual.test\n"), 0644)
	c, err = controllerFromConfigFile(dir, path)
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	_, domains := call(t, c, true, "GET", "/v1/domains", nil, "")
	// Fail immediately if fixture setup or execution fails.
	if len(domains["domains"].([]any)) != 3 {
		t.Fatal(domains)
	}
	status, out = call(t, c, true, "POST", "/v1/plans/"+oldPlan["id"].(string)+"/apply", map[string]any{"approved": true, "fingerprint": oldPlan["fingerprint"], "requestId": "old"}, "")
	// Require the expected HTTP status for this request.
	if status != 409 || code(out) != "STALE_PLAN" {
		t.Fatal(status, out)
	}
	assertActiveDomainRemovalAndLockRecovery(t, c, dir, path)
}

// TestSecretWriteMetadataUsesKeyedVerifierAndMigrates verifies secret write metadata uses keyed verifier and migrates.
func TestSecretWriteMetadataUsesKeyedVerifierAndMigrates(t *testing.T) {
	dir := t.TempDir()
	c := newTestController(t, dir, &recordingRuntime{}, nil)
	value := "low-entropy-password"
	_, uploaded := call(t, c, true, "PUT", "/v1/secrets/key", map[string]any{"value": value, "requestId": "upload"}, "")
	raw := controllerSnapshot(t, c)
	// Guard against offline guessing verifier exposed.
	if strings.Contains(string(raw), verifier("key|"+value)) {
		t.Fatal("offline guessing verifier exposed")
	}
	c.mu.Lock()
	// Exercise each fixture case before evaluating the result.
	for key, write := range c.state.Writes {
		write.Signature = verifier("key|" + value)
		c.state.Writes[key] = write
	}
	c.store.Save(c.state)
	c.mu.Unlock()
	closeController(t, c)
	c = newTestController(t, dir, &recordingRuntime{}, nil)
	defer closeController(t, c)
	_, retried := call(t, c, true, "PUT", "/v1/secrets/key", map[string]any{"value": value, "requestId": "upload"}, "")
	// Guard against migration broke durable upload replay.
	if retried["revision"] != uploaded["revision"] {
		t.Fatal("migration broke durable upload replay", retried)
	}
	raw = controllerSnapshot(t, c)
	// Guard against old metadata guessing verifier retained.
	if strings.Contains(string(raw), verifier("key|"+value)) {
		t.Fatal("old metadata guessing verifier retained")
	}
}

// TestPendingResourceDeletionCannotRaceDependencyApproval verifies pending resource deletion cannot race dependency approval.
func TestPendingResourceDeletionCannotRaceDependencyApproval(t *testing.T) {
	manager := &recordingResources{}
	c := resourceController(t, t.TempDir(), &recordingRuntime{}, manager)
	defer closeController(t, c)
	c.mu.Lock()
	c.state.Resources["shared"] = &ResourceRecord{Resource: model.Resource{Name: "shared", Type: "postgres", Status: "running"}, Spec: model.ResourceSpec{Name: "shared", Type: "postgres", Storage: "1Gi"}}
	c.state.Operations["deleting"] = &Operation{ID: "deleting", Kind: "resource-action", Name: "shared", Action: "delete", Purge: true, Status: "queued"}
	c.mu.Unlock()
	spec := project()
	spec.Dependencies = map[string]model.Dependency{"db": {Type: "postgres", Source: "existing", Instance: "shared"}}
	spec.Bindings = map[string]string{"DATABASE_URL": "db.url"}
	status, out := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Require the expected HTTP status for this request.
	if status != 409 || code(out) != "RESOURCE_BUSY" {
		t.Fatal(status, out)
	}
	status, out = call(t, c, true, "POST", "/v1/resource-plans", map[string]any{"spec": model.ResourceSpec{Name: "shared", Type: "postgres", Storage: "1Gi"}}, "")
	// Guard against resource plan ignored accepted deletion.
	if status == 200 {
		t.Fatal("resource plan ignored accepted deletion", out)
	}
}

// slowBackupResources exposes a start signal and a deliberately delayed backup.
type slowBackupResources struct {
	*recordingResources
	started chan struct{}
}

// Backup supports backup in the integration fixture.
func (m *slowBackupResources) Backup(ctx context.Context, _ string, _ model.ResourceContext) ([]byte, error) {
	close(m.started)
	// Wait for completion or cancellation without blocking indefinitely.
	select {
	// Return bytes after work outlasts the ordinary write deadline.
	case <-time.After(350 * time.Millisecond):
		return []byte("slow-private-backup"), nil
	// Stop the delayed fixture when the request context is cancelled.
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestRealTLSBackupExtendsDeadlineAndDoesNotBlockStatus verifies real tls backup extends deadline and does not block status.
func TestRealTLSBackupExtendsDeadlineAndDoesNotBlockStatus(t *testing.T) {
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := certServer.TLS.Certificates[0]
	certServer.Close()
	pool := x509.NewCertPool()
	parsed, _ := x509.ParseCertificate(certificate.Certificate[0])
	pool.AddCert(parsed)
	manager := &slowBackupResources{recordingResources: &recordingResources{}, started: make(chan struct{})}
	c, err := New(Options{StateDir: t.TempDir(), Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: &recordingRuntime{}, ResourceManager: manager, WriteTimeout: 100 * time.Millisecond, LongOperationTimeout: 2 * time.Second})
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	defer closeController(t, c)
	_, plan := call(t, c, true, "POST", "/v1/resource-plans", map[string]any{"spec": model.ResourceSpec{Name: "source", Type: "postgres", Storage: "1Gi"}}, "")
	resourceApply(t, c, plan, "create")
	_, issued := call(t, c, true, "POST", "/v1/pairing/tokens", map[string]any{"name": "tls"}, "")
	_, paired := call(t, c, false, "POST", "/v1/pairing/redeem", map[string]any{"token": issued["token"], "name": "tls"}, "")
	// Fail immediately if fixture setup or execution fails.
	if err = c.Listen(ListenOptions{TCP: true, TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	request := authenticatedTLSRequest(client, c.Address(), paired["token"].(string))
	done := make(chan error, 1)
	f := attachHumanFixture(t, c)
	f.token = paired["token"].(string)
	approval := f.propose(t, "POST", "/v1/resources/source/backups", map[string]any{})
	signPendingFixture(t, f, approval)
	// Keep this callback bounded to the current request and its captured state.
	wrappedRequest := func(method, path string) (int, map[string]any, error) {
		// Select the fixture behavior required to exercise this boundary.
		if isFixtureBackup(method, path) {
			path = "/v1/approvals/" + approval["id"].(string) + "/execute"
		}
		return request(method, path)
	}
	go requestBackupResult(wrappedRequest, done)
	// Guard against backup did not start.
	select {
	// Observe the resource-manager start before querying controller status.
	case <-manager.started:
	// Fail promptly if the background backup never begins.
	case <-time.After(time.Second):
		t.Fatal("backup did not start")
	}
	start := time.Now()
	status, out, err := request("GET", "/v1/status")
	// Guard against status blocked by backup.
	if err != nil || status != 200 || time.Since(start) > 200*time.Millisecond {
		t.Fatal("status blocked by backup", status, out, err)
	}
	// Guard against backup exceeded default write deadline.
	if err = <-done; err != nil {
		t.Fatal("backup exceeded default write deadline", err)
	}
}

// TestDomainAPIRejectsEmptyConfigAndPendingExternalEdits verifies domain api rejects empty config and pending external edits.
func TestDomainAPIRejectsEmptyConfigAndPendingExternalEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := "version: 1\ndomains:\n  - example.com\n"
	os.WriteFile(path, []byte(initial), 0644)
	c, err := New(Options{StateDir: filepath.Join(dir, "state"), ConfigPath: path, Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: &recordingRuntime{}})
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	defer closeController(t, c)
	status, _ := call(t, c, true, "DELETE", "/v1/domains/example.com", nil, "")
	// Guard against last-domain removal creates invalid config.
	if status == 200 {
		t.Fatal("last-domain removal creates invalid config")
	}
	raw, _ := os.ReadFile(path)
	// Guard against invalid domain update modified config.
	if string(raw) != initial {
		t.Fatal("invalid domain update modified config")
	}
	edited := "version: 1\ndomains:\n  - example.com\n  - pending.test\n"
	os.WriteFile(path, []byte(edited), 0644)
	status, out := call(t, c, true, "POST", "/v1/domains", map[string]any{"domain": "api.test"}, "")
	// Require the expected HTTP status for this request.
	if status != 409 || code(out) != "CONFIG_CHANGED" {
		t.Fatal(status, out)
	}
	raw, _ = os.ReadFile(path)
	// Guard against aPI overwrote pending manual config change.
	if string(raw) != edited {
		t.Fatal("API overwrote pending manual config change")
	}
}

// TestPendingEnrollmentCanPollBeyondOneMinuteWithoutConsumingIssuanceQuota verifies pending enrollment can poll beyond one minute without consuming issuance quota.
func TestPendingEnrollmentCanPollBeyondOneMinuteWithoutConsumingIssuanceQuota(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	_, req := call(t, c, false, "POST", "/v1/pairing/requests", map[string]any{"name": "patient"}, "")
	// Exercise each fixture case before evaluating the result.
	for i := 0; i < 65; i++ {
		status, out := call(t, c, false, "POST", "/v1/pairing/poll", map[string]any{"requestId": req["requestId"], "requestSecret": req["requestSecret"]}, "")
		// Require the expected HTTP status for this request.
		if status != 200 {
			t.Fatal(i, status, out)
		}
	}
	status, out := call(t, c, false, "POST", "/v1/pairing/requests", map[string]any{"name": "next"}, "")
	// Guard against polling exhausted issuance budget.
	if status != 200 {
		t.Fatal("polling exhausted issuance budget", out)
	}
}

// TestProvisionedDependencyIdentityCollisionRequiresExplicitSharing verifies provisioned dependency identity collision requires explicit sharing.
func TestProvisionedDependencyIdentityCollisionRequiresExplicitSharing(t *testing.T) {
	manager := &recordingResources{}
	c := resourceController(t, t.TempDir(), &recordingRuntime{}, manager)
	defer closeController(t, c)
	first := project()
	first.Name = "a-b"
	first.Route = "first.example.com"
	first.Dependencies = map[string]model.Dependency{"c": {Type: "postgres", Source: "provision", Storage: "1Gi"}}
	first.Bindings = map[string]string{"DATABASE_URL": "c.url"}
	status, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": first}, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(plan)
	}
	// Require the expected operation state.
	if op := resourceApply(t, c, plan, "first"); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	second := project()
	second.Name = "a"
	second.Route = "second.example.com"
	second.Dependencies = map[string]model.Dependency{"b-c": {Type: "postgres", Source: "provision", Storage: "1Gi"}}
	second.Bindings = map[string]string{"DATABASE_URL": "b-c.url"}
	status, out := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": second}, "")
	// Guard against unintended resource sharing allowed.
	if status != 409 || code(out) != "RESOURCE_CONFLICT" {
		t.Fatal("unintended resource sharing allowed", status, out)
	}
	second.Dependencies["b-c"] = model.Dependency{Type: "postgres", Source: "existing", Instance: "a-b-c"}
	status, out = call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": second}, "")
	// Guard against explicit sharing rejected.
	if status != 200 {
		t.Fatal("explicit sharing rejected", out)
	}
	status, out = call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": first}, "")
	// Guard against owner cannot redeploy.
	if status != 200 {
		t.Fatal("owner cannot redeploy", out)
	}
}

// TestSuccessfulReplacementReleasesInterruptedRouteReservations verifies successful replacement releases interrupted route reservations.
func TestSuccessfulReplacementReleasesInterruptedRouteReservations(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	old := project()
	old.Route = "old.example.com"
	c.mu.Lock()
	c.state.Operations["interrupted"] = &Operation{ID: "interrupted", Kind: "deploy", Name: old.Name, Status: "interrupted", Spec: old}
	c.mu.Unlock()
	replacement := project()
	replacement.Route = "new.example.com"
	_, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": replacement}, "")
	succeeded := resourceApply(t, c, plan, "replace")
	// Require the expected operation state.
	if succeeded["status"] != "succeeded" {
		t.Fatal(succeeded)
	}
	_, oldOp := call(t, c, true, "GET", "/v1/operations/interrupted", nil, "")
	// Require the expected operation state.
	if oldOp["status"] != "superseded" || oldOp["supersededBy"] != succeeded["id"] {
		t.Fatal(oldOp)
	}
	other := project()
	other.Name = "other"
	other.Route = old.Route
	status, out := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": other}, "")
	// Guard against superseded route still reserved.
	if status != 200 {
		t.Fatal("superseded route still reserved", out)
	}
	c.mu.Lock()
	c.state.Operations["delete-interrupted"] = &Operation{ID: "delete-interrupted", Kind: "deploy", Name: replacement.Name, Status: "interrupted", Spec: replacement}
	c.mu.Unlock()
	_, deletion := call(t, c, true, "POST", "/v1/apps/sample/action", map[string]any{"action": "delete"}, "")
	// Require the expected operation state.
	if result := wait(t, c, deletion["operationId"].(string)); result["status"] != "succeeded" {
		t.Fatal(result)
	}
	_, prior := call(t, c, true, "GET", "/v1/operations/delete-interrupted", nil, "")
	// Require the expected operation state.
	if prior["status"] != "superseded" {
		t.Fatal(prior)
	}
	status, out = call(t, c, true, "POST", "/v1/domains", map[string]any{"domain": "remaining.test"}, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(out)
	}
	status, out = call(t, c, true, "DELETE", "/v1/domains/example.com", nil, "")
	// Guard against deleted app's interrupted route blocks domain removal.
	if status != 200 {
		t.Fatal("deleted app's interrupted route blocks domain removal", out)
	}
}

// controllerFromConfigFile loads the authoritative config before constructing a controller.
func controllerFromConfigFile(dir, path string) (*Controller, error) {
	cfg, err := config.ReadServer(path)
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		return nil, err
	}
	return New(Options{StateDir: filepath.Join(dir, "state"), ConfigPath: path, Config: cfg, Runtime: &recordingRuntime{}})
}

// authenticatedTLSRequest builds a request helper that verifies TLS and sends the paired credential.
func authenticatedTLSRequest(client *http.Client, address, token string) func(string, string) (int, map[string]any, error) {
	// Decode the successful response body after its transport read completes.
	return func(method, path string) (int, map[string]any, error) {
		req, _ := http.NewRequest(method, "https://"+address+path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+token)
		res, e := client.Do(req)
		// Fail immediately if fixture setup or execution fails.
		if e != nil {
			return 0, nil, e
		}
		defer res.Body.Close()
		raw, e := io.ReadAll(res.Body)
		out := map[string]any{}
		// Fail immediately if fixture setup or execution fails.
		if e == nil {
			e = json.Unmarshal(raw, &out)
		}
		return res.StatusCode, out, e
	}
}

// assertActiveDomainRemovalAndLockRecovery checks active domain validation and lock release after rejected startup.
func assertActiveDomainRemovalAndLockRecovery(t *testing.T, c *Controller, dir, path string) {
	t.Helper()
	c.mu.Lock()
	c.state.Applications["sample"] = &Application{Deployment: model.Deployment{Name: "sample", Status: "running"}, Spec: project()}
	c.store.Save(c.state)
	c.mu.Unlock()
	closeController(t, c)
	os.WriteFile(path, []byte("version: 1\ndomains:\n  - manual.test\n"), 0644)
	// Guard against active-domain removal accepted.
	if invalid, err := controllerFromConfigFile(dir, path); err == nil {
		closeController(t, invalid)
		t.Fatal("active-domain removal accepted")
	}
	os.WriteFile(path, []byte("version: 1\ndomains:\n  - example.com\n"), 0644)
	c, err := controllerFromConfigFile(dir, path)
	// Guard against startup rejection leaked lock.
	if err != nil {
		t.Fatal("startup rejection leaked lock", err)
	}
	closeController(t, c)
}

// requestBackupResult reports both transport failures and invalid backup responses to the waiting test.
func requestBackupResult(request func(string, string) (int, map[string]any, error), done chan<- error) {
	status, out, e := request("POST", "/v1/resources/source/backups")
	// Fail immediately if fixture setup or execution fails.
	if e == nil && (status != 200 || out["backupId"] == nil) {
		e = model.Err("BAD_RESPONSE", "Backup response missing")
	}
	done <- e
}

// signPendingFixture grants test authority without executing so the real TLS path remains under test.
func signPendingFixture(t *testing.T, f *humanFixture, approval map[string]any) {
	t.Helper()
	c := f.c
	startProof := f.beginDecision(t, approval, "approve")
	ceremony := f.session(t, startProof)
	statusProof, resultProof := humanHTTP(t, c, false, "POST", "/v1/human/decision/finish", map[string]any{"ceremony": startProof["ceremony"], "signature": f.signature(t, ceremony.Message, 5)}, "", "")
	// Stop when fixture setup or the exercised operation fails.
	if statusProof != 200 {
		t.Fatal(statusProof, resultProof)
	}
}

// isFixtureBackup maps just the backup submission onto its already approved intent.
func isFixtureBackup(method, path string) bool {
	return method == "POST" && path == "/v1/resources/source/backups"
}
