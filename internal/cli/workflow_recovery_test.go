package cli

import (
	"context"
	"encoding/json"
	"errors"
	"hostlier/internal/controller"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recoveryPackageFixture simulates a completed mutation with a lost apply response and authoritative private discovery.
type recoveryPackageFixture struct {
	engine             *workflowEngine
	status             workflow.InspectStatus
	observationVersion string
	inspections        int
}

// Plan returns protected approval state without provisioning any fixture resource.
func (*recoveryPackageFixture) Plan(context.Context, workflow.Request) (workflow.Prepared, error) {
	return workflow.Prepared{Digest: strings.Repeat("a", 64), Summary: []string{"Provision owned local database"}, Payload: json.RawMessage(`{"approved":"private-original-payload"}`)}, nil
}

// Apply records the sole external mutation before deliberately losing its response.
func (p *recoveryPackageFixture) Apply(_ context.Context, req workflow.Request, _ string) (workflow.Result, error) {
	// Approved secret revisions and prepared credentials must reach the first effect.
	if req.Secrets["executionToken"] != "private-fused-token" || string(req.Prepared) != `{"approved":"private-original-payload"}` {
		return workflow.Result{}, errors.New("private-input-mismatch")
	}
	p.engine.packageCalls++
	return workflow.Result{}, errors.New("private-provider-diagnostic")
}

// Inspect reads the original approval credentials and reports authoritative private outcome evidence.
func (p *recoveryPackageFixture) Inspect(_ context.Context, req workflow.Request) (workflow.Inspection, error) {
	p.inspections++
	// Inspection must restore original credentials instead of manufacturing a new plan payload.
	if req.Secrets["executionToken"] != "private-fused-token" || string(req.Prepared) != `{"approved":"private-original-payload"}` {
		return workflow.Inspection{}, errors.New("private-inspection-input-mismatch")
	}
	result := workflow.Inspection{Status: p.status}
	// Only verified completed observations may publish the retained private connection.
	if p.status == workflow.Completed {
		state, _ := json.Marshal(map[string]string{"owned": req.Key, "observation": p.observationVersion})
		result.Result = &workflow.Result{State: state, Outputs: map[string]string{"url": "private-app-url"}, PrivateOutputs: map[string]string{"admin": "private-admin-url"}}
	}
	return result, nil
}

// newRecoveryCLI creates an actual failed controller operation through CLI approval and the real HTTP handler path.
func newRecoveryCLI(t *testing.T) (Options, *recoveryPackageFixture) {
	t.Helper()
	dir := t.TempDir()
	engine := &workflowEngine{}
	pkg := &recoveryPackageFixture{engine: engine, status: workflow.Uncertain, observationVersion: "first"}
	server, err := controller.New(controller.Options{StateDir: filepath.Join(dir, "state"), Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: engine, Packages: pkg})
	// Durable controller initialization precedes package planning and execution.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close(context.Background()) }) // Close only this fixture's temporary controller.
	api := workflowAPI{handler: withFixtureHuman(t, server, server.LocalHandler())}
	// Store the original credential revision through the real protected secret API.
	if err = api.Request(context.Background(), "PUT", "/v1/secrets/fused-token", map[string]any{"value": "private-fused-token", "requestId": "recovery-secret"}, nil); err != nil {
		t.Fatal(err)
	}
	// This local product expands to an installed package invocation under the ordinary approval workflow.
	if err = os.WriteFile(filepath.Join(dir, ".hostlier"), []byte(productFixture), 0600); err != nil {
		t.Fatal(err)
	}
	// Supply the fixture's explicit interactive approval response.
	options := Options{CWD: dir, Home: filepath.Join(dir, "home"), Client: api, PollInterval: time.Millisecond, Prompt: func(string, bool) (string, error) { return "yes", nil }}
	output, err := invoke([]string{"product", "deploy"}, "", options)
	// The lost response leaves one uncertain effect and no application deployment.
	if err == nil || engine.packageCalls != 1 || engine.deployed {
		t.Fatal("fixture did not retain uncertain mutation", err)
	}
	assertRecoveryPrivate(t, output)
	assertRecoveryPrivate(t, err.Error())
	return options, pkg
}

// TestWorkflowRecoveryCLIApprovalAndReuse verifies inspection, explicit reconciliation and duplicate-free redeployment.
func TestWorkflowRecoveryCLIApprovalAndReuse(t *testing.T) {
	options, pkg := newRecoveryCLI(t)
	readonly := options
	readonly.Prompt = nil
	output, err := invoke([]string{"workflow", "inspect", "sample", "database"}, "", readonly)
	// Read-only inspection is noninteractive and cannot infer successful mutation from a missing response.
	if err != nil || !strings.Contains(output, `"status":"uncertain"`) {
		t.Fatal(output, err)
	}
	_, err = invoke([]string{"workflow", "reconcile", "sample", "database"}, "", options)
	// An approval cannot turn uncertain evidence into completed resources.
	if err == nil {
		t.Fatal("uncertain outcome reconciled")
	}
	pkg.status = workflow.Completed
	assertRecoveryAdmission(t, options, pkg)
	output, err = invoke([]string{"workflow", "reconcile", "sample", "database"}, "", options)
	// Explicit approval commits independently verified output without calling Apply again.
	if err != nil {
		t.Fatal(output, err)
	}
	assertRecoveryPrivate(t, output)
	output, err = invoke([]string{"product", "deploy"}, "", options)
	// The ordinary next deployment reuses protected completed outputs and starts its app exactly once.
	if err != nil || pkg.engine.packageCalls != 1 || !pkg.engine.deployed {
		t.Fatal("recovery replayed apply or lost binding", err)
	}
	assertRecoveryPrivate(t, output)
}

// assertRecoveryAdmission verifies no terminal, --yes and declined approval cannot alter retained uncertain state.
func assertRecoveryAdmission(t *testing.T, options Options, pkg *recoveryPackageFixture) {
	t.Helper()
	var err error
	_, err = invoke([]string{"workflow", "reconcile", "sample", "database", "--yes"}, "", options)
	// A generic bypass flag cannot stand in for reviewing the current evidence.
	if err == nil {
		t.Fatal("--yes reconciliation accepted")
	}
	declined := options
	// Decline this exact recovery observation at the actual CLI prompt.
	declined.Prompt = func(string, bool) (string, error) { return "no", nil }
	_, err = invoke([]string{"workflow", "reconcile", "sample", "database"}, "", declined)
	// Declined review leaves the external mutation count and application state unchanged.
	if err == nil || pkg.engine.packageCalls != 1 || pkg.engine.deployed {
		t.Fatal("declined reconciliation changed effects")
	}
}

// TestWorkflowRecoveryCLIRejectsChangedObservation checks approval binds the exact authoritative private result.
func TestWorkflowRecoveryCLIRejectsChangedObservation(t *testing.T) {
	options, pkg := newRecoveryCLI(t)
	pkg.status = workflow.Completed
	// Simulate observation drift between public review and the approval request.
	options.Prompt = func(string, bool) (string, error) { pkg.observationVersion = "changed-after-review"; return "yes", nil }
	output, err := invoke([]string{"workflow", "reconcile", "sample", "database"}, "", options)
	// A changed result must require a new reviewed fingerprint before metadata recovery.
	if err == nil || pkg.engine.packageCalls != 1 || pkg.engine.deployed {
		t.Fatal("changed observation accepted")
	}
	assertRecoveryPrivate(t, output)
	// Approve only after the current public plan has been displayed.
	options.Prompt = func(string, bool) (string, error) { return "yes", nil }
	_, err = invoke([]string{"workflow", "reconcile", "sample", "database"}, "", options)
	// A fresh explicit review of the new observation can recover the retained output.
	if err != nil {
		t.Fatal(err)
	}
}

// assertRecoveryPrivate ensures public recovery review never reveals protected payloads or output classes.
func assertRecoveryPrivate(t *testing.T, output string) {
	t.Helper()
	// None of the original credential, execution state, diagnostics or recovered bindings belongs in public output.
	for _, private := range []string{"private-original-payload", "private-fused-token", "private-provider-diagnostic", "private-app-url", "private-admin-url"} {
		// Check each protected value independently to keep failures specific without printing secrets.
		if strings.Contains(output, private) {
			t.Fatal("recovery exposed protected value")
		}
	}
}
