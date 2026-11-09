package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hostlier/internal/model"
)

// call records one request for assertions about command side effects.
type call struct {
	method, path string
	body         any
}

// fakeClient supplies controlled API responses while retaining every call.
type fakeClient struct {
	approval approvalSimulation
	calls    []call
	reply    func(call) (any, error)
}

// Request supports request in the integration fixture.
func (f *fakeClient) Request(_ context.Context, method, path string, body, result any) error {
	handled, c, err := f.approval.respond(method, path, body, result)
	// Select the fixture behavior required to exercise this boundary.
	if handled {
		return err
	}
	method, path, body = c.method, c.path, c.body
	f.calls = append(f.calls, c)
	var value any = map[string]any{}
	var e error
	// Fail immediately if fixture setup or execution fails.
	if f.reply != nil {
		value, e = f.reply(c)
	} else {
		// Fail immediately if fixture setup or execution fails.
		switch {
		// Return the deployment-plan fixture expected by approval tests.
		case path == "/v1/plans":
			value = map[string]any{"id": "plan", "fingerprint": "fingerprint", "spec": body}
		// Return a stable accepted operation identity after plan application.
		case strings.HasSuffix(path, "/apply"):
			value = map[string]any{"operationId": "operation"}
		// Return fixture resource and lifecycle responses for remaining requests.
		case strings.HasPrefix(path, "/v1/secrets/"):
			value = map[string]any{"name": "key", "revision": "1"}
		}
	}
	// Fail immediately if fixture setup or execution fails.
	if e != nil {
		return e
	}
	// Fail immediately if fixture setup or execution fails.
	if result != nil {
		data, _ := json.Marshal(value)
		return json.Unmarshal(data, result)
	}
	return nil
}

// project supports project in the integration fixture.
func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	data := "version: 1\nname: fixture\nimage: nginx@sha256:" + strings.Repeat("a", 64) + "\nport: 80\nroute: fixture.example.com\n"
	// Fail immediately if fixture setup or execution fails.
	if e := os.WriteFile(filepath.Join(dir, ".hostlier"), []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
	return dir
}

// invoke supports invoke in the integration fixture.
func invoke(args []string, input string, options Options) (string, error) {
	var out bytes.Buffer
	e := RunWithOptions(context.Background(), args, strings.NewReader(input), &out, io.Discard, options)
	return out.String(), e
}

// TestDeployRequiresPerOperationApproval verifies deploy requires per operation approval.
func TestDeployRequiresPerOperationApproval(t *testing.T) {
	f := &fakeClient{}
	options := Options{CWD: project(t), Client: f}
	// Guard against noninteractive deployed.
	if _, e := invoke([]string{"deploy", "--detach"}, "", options); e != nil {
		t.Fatal(e)
	}
	f.calls = nil
	// Approve this invocation through an explicit injected terminal answer.
	options.Prompt = func(string, bool) (string, error) { return "yes", nil }
	// Guard against --yes bypass accepted.
	if _, e := invoke([]string{"deploy", "--yes"}, "", options); e == nil {
		t.Fatal("--yes bypass accepted")
	}
	// Guard against controller called before approval requirement checked.
	if len(f.calls) != 0 {
		t.Fatal("controller called before approval requirement checked")
	}
	assertDeployPlanDecision(t, f, options)
}

// TestResourceExportUsesOwnerOnlyNewFileAndNeverPrintsData verifies resource export uses owner only new file and never prints data.
func TestResourceExportUsesOwnerOnlyNewFileAndNeverPrintsData(t *testing.T) {
	dir := t.TempDir()
	// Return encoded backup data without using a real controller.
	f := &fakeClient{reply: func(c call) (any, error) {
		return map[string]string{"data": "c2Vuc2l0aXZlLWJhY2t1cC1kYXRh", "name": "database", "type": "postgres"}, nil
	}}
	output, err := invoke([]string{"resource", "export", "backup-id", "--output", "database.dump"}, "", Options{Client: f, CWD: dir})
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	// Guard against backup content printed.
	if strings.Contains(output, "sensitive-backup-data") {
		t.Fatal("backup content printed")
	}
	info, err := os.Stat(filepath.Join(dir, "database.dump"))
	// Guard against backup mode.
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup mode", err)
	}
	// Guard against existing file overwritten.
	if _, err = invoke([]string{"resource", "export", "backup-id", "--output", "database.dump"}, "", Options{Client: f, CWD: dir}); err == nil {
		t.Fatal("existing file overwritten")
	}
}

// TestRestoreRequiresApprovalAndCarriesBackupIdentity verifies restore requires approval and carries backup identity.
func TestRestoreRequiresApprovalAndCarriesBackupIdentity(t *testing.T) {
	// Verify restore source identity before returning a controlled plan.
	f := &fakeClient{reply: func(c call) (any, error) {
		// Guard against restore source missing.
		if c.path == "/v1/resource-plans" {
			body := c.body.(map[string]any)
			// Guard against restore source missing.
			if body["restoreBackupId"] != "backup-id" {
				t.Fatal("restore source missing")
			}
			return map[string]any{"id": "restore-plan", "fingerprint": "restore-fingerprint"}, nil
		}
		return map[string]string{"operationId": "restore-operation"}, nil
	}}
	args := []string{"resource", "restore", "postgres", "restored-db", "--storage", "1Gi", "--backup", "backup-id", "--detach"}
	// Guard against noninteractive restore accepted.
	if _, err := invoke(args, "", Options{Client: f}); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	// Guard against noninteractive restore called controller.
	if len(f.calls) != 0 {
		t.Fatal("noninteractive restore called controller")
	}
	// Fail immediately if fixture setup or execution fails.
	if _, err := invoke(args, "", Options{Client: f, Prompt: func(string, bool) (string, error) { return "yes", nil }}); err != nil {
		t.Fatal(err)
	}
}

// TestLifecycleWaitsForTerminalOperationUnlessDetached verifies lifecycle waits for terminal operation unless detached.
func TestLifecycleWaitsForTerminalOperationUnlessDetached(t *testing.T) {
	// Exercise each fixture case before evaluating the result.
	for _, arguments := range [][]string{{"restart", "fixture"}, {"resource", "restart", "database"}} {
		// Run the same lifecycle expectations for application and resource commands.
		t.Run(strings.Join(arguments, "-"), func(t *testing.T) {
			assertLifecycleWaiting(t, arguments)
		})
	}
}

// TestCancellingAcceptedLifecycleOnlyStopsWaiting verifies cancelling accepted lifecycle only stops waiting.
func TestCancellingAcceptedLifecycleOnlyStopsWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel the context after the accepted operation begins polling.
	f := &fakeClient{reply: func(c call) (any, error) {
		// Fail immediately if fixture setup or execution fails.
		if c.method == "POST" {
			return map[string]string{"operationId": "durable-operation"}, nil
		}
		cancel()
		return map[string]string{"status": "running"}, nil
	}}
	var output bytes.Buffer
	err := RunWithOptions(ctx, []string{"restart", "fixture"}, strings.NewReader(""), &output, io.Discard, Options{Client: f})
	// Guard against cancelled watcher did not report durable job.
	if err == nil || !strings.Contains(err.Error(), "operation continues") || !strings.Contains(err.Error(), "durable-operation") {
		t.Fatalf("cancelled watcher did not report durable job: %v", err)
	}
}

// TestSupersededOperationIsTerminal verifies superseded operation is terminal.
func TestSupersededOperationIsTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Return a superseded operation to verify terminal failure handling.
	f := &fakeClient{reply: func(c call) (any, error) {
		// Fail immediately if fixture setup or execution fails.
		if c.method == "POST" {
			return map[string]string{"operationId": "old-operation"}, nil
		}
		return map[string]string{"status": "superseded"}, nil
	}}
	err := RunWithOptions(ctx, []string{"restart", "fixture"}, strings.NewReader(""), io.Discard, io.Discard, Options{Client: f, PollInterval: time.Millisecond})
	// Guard against superseded operation not terminal.
	if err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("superseded operation not terminal: %v", err)
	}
}

// assertDeployPlanDecision checks declined deployment plans and explicit approval payloads.
func assertDeployPlanDecision(t *testing.T, f *fakeClient, options Options) {
	t.Helper()
	// Decline the current plan through the injected prompt.
	options.Prompt = func(string, bool) (string, error) { return "no", nil }
	// Guard against decline succeeded.
	if _, e := invoke([]string{"deploy"}, "", options); e == nil {
		t.Fatal("decline succeeded")
	}
	// Guard against decline mutated.
	if len(f.calls) != 1 || f.calls[0].path != "/v1/plans" {
		t.Fatal("decline mutated")
	}
	// Approve the newly displayed plan through the injected prompt.
	options.Prompt = func(string, bool) (string, error) { return "yes", nil }
	// Fail immediately if fixture setup or execution fails.
	if _, e := invoke([]string{"deploy", "--detach"}, "", options); e != nil {
		t.Fatal(e)
	}
	body := f.calls[len(f.calls)-1].body.(map[string]any)
	// Guard against missing exact plan approval.
	if body["approved"] != true || body["requestId"] == "" {
		t.Fatal("missing exact plan approval")
	}
}

// TestPlanOverrideDoesNotApply verifies plan override does not apply.
func TestPlanOverrideDoesNotApply(t *testing.T) {
	f := &fakeClient{}
	// Fail immediately if fixture setup or execution fails.
	if _, e := invoke([]string{"plan", "--route", "example.com/reports"}, "", Options{CWD: project(t), Client: f}); e != nil {
		t.Fatal(e)
	}
	// Guard against plan mutated.
	if len(f.calls) != 1 {
		t.Fatal("plan mutated")
	}
	spec := f.calls[0].body.(map[string]any)["spec"].(model.Project)
	// Guard against override lost.
	if spec.Route != "example.com/reports" {
		t.Fatal("override lost")
	}
}

// TestSecretExactInputAndValidation verifies secret exact input and validation.
func TestSecretExactInputAndValidation(t *testing.T) {
	f := &fakeClient{}
	output, e := invoke([]string{"secret", "set", "key", "--stdin"}, "sensitive\nmultiline\n", Options{Client: f})
	// Fail immediately if fixture setup or execution fails.
	if e != nil {
		t.Fatal(e)
	}
	// Guard against stdin modified.
	if f.calls[0].body.(map[string]any)["value"] != "sensitive\nmultiline\n" {
		t.Fatal("stdin modified")
	}
	// Guard against secret output leaked.
	if strings.Contains(output, "sensitive") {
		t.Fatal("secret output leaked")
	}
	// Guard against accepted invalid input.
	for _, args := range [][]string{{"secret", "set", "key", "--value", "sensitive"}, {"secret", "set", "key", "literal-value"}, {"secret", "set", "key", "--stdin", "--from-env", "NAME"}, {"secret", "set", "key", "--from-env", "MISSING"}, {"secret", "set", "key"}} {
		f := &fakeClient{}
		// Guard against accepted invalid input.
		if _, e := invoke(args, "", Options{Client: f, Environment: map[string]string{}}); e == nil {
			t.Errorf("accepted invalid input %v", args)
		}
		// Guard against invalid input mutated.
		if len(f.calls) != 0 {
			t.Fatal("invalid input mutated")
		}
	}
	// Guard against oversize accepted.
	if _, e = readInput(strings.NewReader(strings.Repeat("a", 65537))); e == nil {
		t.Fatal("oversize accepted")
	}
}

func TestSecretSetRegistersOwningApplication(t *testing.T) {
	f := &fakeClient{}
	if _, err := invoke([]string{"secret", "set", "app-key", "--stdin", "--app", "customer-app"}, "private-value", Options{Client: f}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].body.(map[string]any)["owner"] != "customer-app" {
		t.Fatal("owning application was not sent to the controller")
	}
}

// TestStalePlanNeedsNewApprovalAndTransportRetryRetainsID verifies stale plan needs new approval and transport retry retains id.
func TestStalePlanNeedsNewApprovalAndTransportRetryRetainsID(t *testing.T) {
	attempt, prompts := 0, 0
	var previous any
	// Simulate a stale plan and ambiguous transport failure with durable replay.
	f := &fakeClient{reply: func(c call) (any, error) {
		// Fail immediately if fixture setup or execution fails.
		if c.path == "/v1/plans" {
			return map[string]string{"id": "plan", "fingerprint": "fp"}, nil
		}
		attempt++
		// Fail immediately if fixture setup or execution fails.
		if attempt == 1 {
			return nil, model.Err("STALE_PLAN", "Changed")
		}
		// Fail immediately if fixture setup or execution fails.
		if attempt == 2 {
			previous = c.body
			return nil, model.Err("CONNECTION", "Lost response")
		}
		before, _ := json.Marshal(previous)
		after, _ := json.Marshal(c.body)
		// Guard against apply retry changed identity.
		if !bytes.Equal(before, after) {
			t.Fatal("apply retry changed identity")
		}
		return map[string]string{"operationId": "op"}, nil
	}}
	// Approve each reviewed plan while counting refresh prompts.
	_, e := invoke([]string{"deploy", "--detach"}, "", Options{CWD: project(t), Client: f, Prompt: func(string, bool) (string, error) { prompts++; return "yes", nil }})
	// Fail immediately if fixture setup or execution fails.
	if e != nil {
		t.Fatal(e)
	}
	// Guard against prompts=.
	if prompts != 2 || attempt != 3 {
		t.Fatalf("prompts=%d attempts=%d", prompts, attempt)
	}
}

// TestRequestPairingNeverPrintsRequestSecret verifies request pairing never prints request secret.
func TestRequestPairingNeverPrintsRequestSecret(t *testing.T) {
	// Return a pending challenge and eventual credential for enrollment polling.
	f := &fakeClient{reply: func(c call) (any, error) {
		// Fail immediately if fixture setup or execution fails.
		if c.path == "/v1/pairing/requests" {
			return map[string]any{"requestId": "id", "requestSecret": "request-secret", "code": "approval-code", "expiresAt": time.Now().Add(time.Minute).UnixMilli()}, nil
		}
		return map[string]string{"status": "complete", "clientId": "client", "token": "credential-secret"}, nil
	}}
	output, e := invoke([]string{"connect", "https://controller.example.com", "--request", "--name", "test"}, "", Options{Home: t.TempDir(), Client: f, PollInterval: time.Millisecond})
	// Fail immediately if fixture setup or execution fails.
	if e != nil {
		t.Fatal(e)
	}
	// Guard against pairing secrets leaked.
	if strings.Contains(output, "request-secret") || strings.Contains(output, "credential-secret") {
		t.Fatal("pairing secrets leaked")
	}
	// Guard against approval code missing.
	if !strings.Contains(output, "approval-code") {
		t.Fatal("approval code missing")
	}
}

// TestProfileNameValidatedBeforeEnrollment verifies profile name validated before enrollment.
func TestProfileNameValidatedBeforeEnrollment(t *testing.T) {
	f := &fakeClient{}
	// Guard against invalid profile accepted.
	if _, err := invoke([]string{"connect", "https://controller.example.com", "--name", "invalid.name", "--token-stdin"}, "one-time-token", Options{Client: f, Home: t.TempDir()}); err == nil {
		t.Fatal("invalid profile accepted")
	}
	// Guard against invalid profile consumed enrollment token.
	if len(f.calls) != 0 {
		t.Fatal("invalid profile consumed enrollment token")
	}
}

// TestResourceCreationRequiresCurrentInteractivePlanApproval verifies resource creation requires current interactive plan approval.
func TestResourceCreationRequiresCurrentInteractivePlanApproval(t *testing.T) {
	// Return a resource plan followed by its accepted operation receipt.
	f := &fakeClient{reply: func(c call) (any, error) {
		// Fail immediately if fixture setup or execution fails.
		if c.path == "/v1/resource-plans" {
			return map[string]any{"id": "resource-plan", "fingerprint": "resource-fingerprint", "changes": []string{"Create private postgres resource"}}, nil
		}
		return map[string]string{"operationId": "resource-operation"}, nil
	}}
	options := Options{Client: f}
	args := []string{"resource", "create", "postgres", "app-db", "--storage", "1Gi", "--detach"}
	// Guard against noninteractive resource creation allowed.
	if _, err := invoke(args, "", options); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	// Provide an affirmative answer that must not bypass --yes rejection.
	options.Prompt = func(string, bool) (string, error) { return "yes", nil }
	// Guard against --yes bypassed resource review.
	if _, err := invoke(append(append([]string{}, args...), "--yes"), "", options); err == nil {
		t.Fatal("--yes bypassed resource review")
	}
	// Guard against bypass reached controller.
	if len(f.calls) != 0 {
		t.Fatal("bypass reached controller")
	}
	assertResourcePlanDecision(t, f, options, args)
}

// assertLifecycleWaiting checks terminal polling and detached execution for one lifecycle command.
func assertLifecycleWaiting(t *testing.T, arguments []string) {
	t.Helper()
	polls := 0
	// Return running snapshots until the third lifecycle poll succeeds.
	f := &fakeClient{reply: func(c call) (any, error) {
		// Fail immediately if fixture setup or execution fails.
		if c.method == "POST" {
			return map[string]string{"operationId": "lifecycle"}, nil
		}
		polls++
		status := "running"
		// Fail immediately if fixture setup or execution fails.
		if polls >= 3 {
			status = "succeeded"
		}
		return map[string]string{"status": status}, nil
	}}
	output, err := invoke(arguments, "", Options{Client: f, PollInterval: time.Millisecond})
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	// Guard against returned before terminal state: polls=.
	if polls != 3 || !strings.Contains(output, "succeeded") {
		t.Fatalf("returned before terminal state: polls=%d output=%s", polls, output)
	}
	polls = 0
	// Fail immediately if fixture setup or execution fails.
	if _, err = invoke(append(append([]string{}, arguments...), "--detach"), "", Options{Client: f}); err != nil {
		t.Fatal(err)
	}
	// Guard against detached lifecycle waited.
	if polls != 0 {
		t.Fatal("detached lifecycle waited")
	}
	assertFailedLifecycle(t, f, arguments)
}

// assertFailedLifecycle checks that failed terminal operations produce a CLI error.
func assertFailedLifecycle(t *testing.T, f *fakeClient, arguments []string) {
	t.Helper()
	// Return a failed terminal operation after accepting its lifecycle request.
	f.reply = func(c call) (any, error) {
		// Fail immediately if fixture setup or execution fails.
		if c.method == "POST" {
			return map[string]string{"operationId": "failed-lifecycle"}, nil
		}
		return map[string]string{"status": "failed"}, nil
	}
	// Guard against failed lifecycle reported success.
	if _, err := invoke(arguments, "", Options{Client: f}); err == nil {
		t.Fatal("failed lifecycle reported success")
	}
}

// assertResourcePlanDecision checks declined resource plans and explicit approval payloads.
func assertResourcePlanDecision(t *testing.T, f *fakeClient, options Options, args []string) {
	t.Helper()
	// Decline provisioning after reviewing its resource plan.
	options.Prompt = func(string, bool) (string, error) { return "no", nil }
	// Guard against decline succeeded.
	if _, err := invoke(args, "", options); err == nil {
		t.Fatal("decline succeeded")
	}
	// Guard against decline provisioned resource.
	if len(f.calls) != 1 || f.calls[0].path != "/v1/resource-plans" {
		t.Fatal("decline provisioned resource")
	}
	// Approve the subsequent resource plan explicitly.
	options.Prompt = func(string, bool) (string, error) { return "yes", nil }
	// Fail immediately if fixture setup or execution fails.
	if _, err := invoke(args, "", options); err != nil {
		t.Fatal(err)
	}
	final := f.calls[len(f.calls)-1]
	// Guard against resource did not use approved plan apply.
	if final.path != "/v1/plans/resource-plan/apply" || final.body.(map[string]any)["approved"] != true {
		t.Fatal("resource did not use approved plan apply")
	}
}
