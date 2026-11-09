package controller

import (
	"context"
	"encoding/json"
	"errors"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/workflow"
	"strings"
	"sync/atomic"
	"testing"
)

// managedConfig models a desired infrastructure value with strict typed decoding.
type managedConfig struct {
	Value string `json:"value"`
}

// managedState identifies the retained owner independently of its latest desired configuration.
type managedState struct{ Owner, Value string }

// managedRunner exercises the same typed identity envelope used by real IaC executables.
type managedRunner struct {
	definition *packagekit.Definition[managedConfig, string, managedState, string]
	calls      atomic.Int32
	fail       atomic.Bool
}

// newManagedRunner supplies a deterministic external receipt model for controller contract tests.
func newManagedRunner(t *testing.T) *managedRunner {
	t.Helper()
	r := &managedRunner{}
	// Fixture callbacks validate exact previous ownership and record only explicit apply calls.
	d := packagekit.Definition[managedConfig, string, managedState, string]{Name: "managed", Update: true, KnownInputs: true,
		Config: packagekit.Codec[managedConfig]{Schema: map[string]any{"type": "object"}},
		Outputs: packagekit.OutputCodec[string]{
			Codec: packagekit.Codec[string]{Schema: map[string]any{"type": "object"}},
			Names: []string{"value"},
			// Expose only the fixture value as an application-safe output.
			Encode: func(value string) (map[string]string, map[string]string, error) {
				return map[string]string{"value": value}, nil, nil
			},
		},
		// Freeze the desired value without applying it.
		Plan: func(_ context.Context, call packagekit.Call[managedConfig, managedState]) (packagekit.Plan[string], error) {
			return packagekit.Plan[string]{Prepared: call.Config.Value, Summary: []string{"Review owned value update"}}, nil
		},
		Apply: r.apply,
		// Failed fixture applies leave no external effect.
		Inspect: func(_ context.Context, _ packagekit.Call[managedConfig, managedState], _ string) (packagekit.Observation[managedState, string], error) {
			return packagekit.Observation[managedState, string]{Status: workflow.NotStarted}, nil
		},
	}
	var err error
	r.definition, err = packagekit.Define(d)
	// An invalid fixture definition cannot test controller semantics.
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// apply verifies the owner was not replaced and simulates an observable external change.
func (r *managedRunner) apply(_ context.Context, call packagekit.Call[managedConfig, managedState], value string) (packagekit.Result[managedState, string], error) {
	// Fail before mutation so the fixture can prove not-started reconciliation restores ownership.
	if r.fail.Load() {
		return packagekit.Result[managedState, string]{}, errors.New("unavailable")
	}
	// An update must receive the private original receipt rather than silently creating a new owner.
	if call.Previous.Owner != "" && call.Previous.Owner != call.Request.Key {
		return packagekit.Result[managedState, string]{}, errors.New("ownership")
	}
	r.calls.Add(1)
	return packagekit.Result[managedState, string]{State: managedState{Owner: call.Request.Key, Value: value}, Outputs: value}, nil
}

// Describe exposes explicit same-owner update capability without touching external state.
func (r *managedRunner) Describe(context.Context, string) (workflow.Capability, error) {
	return r.definition.Capability(), nil
}

// Plan prepares a typed immutable request identity through the production builder.
func (r *managedRunner) Plan(ctx context.Context, req workflow.Request) (workflow.Prepared, error) {
	req.Action = "plan"
	response, err := r.definition.Handle(ctx, req)
	// Invalid preparation cannot return an executable approval payload.
	if err != nil {
		return workflow.Prepared{}, err
	}
	response.Prepared.Digest = strings.Repeat("a", 64)
	return *response.Prepared, nil
}

// Apply executes the reviewed envelope and never reconstructs a plan from mutable inputs.
func (r *managedRunner) Apply(ctx context.Context, req workflow.Request, _ string) (workflow.Result, error) {
	req.Action = "apply"
	response, err := r.definition.Handle(ctx, req)
	// An uncertain package result must not expose a successful receipt.
	if err != nil {
		return workflow.Result{}, err
	}
	return *response.Result, nil
}

// Inspect checks the original approved envelope before attesting that a failed update never began.
func (r *managedRunner) Inspect(ctx context.Context, req workflow.Request) (workflow.Inspection, error) {
	req.Action = "inspect"
	response, err := r.definition.Handle(ctx, req)
	// Recovery cannot substitute different previous state or resolved inputs.
	if err != nil {
		return workflow.Inspection{}, err
	}
	return *response.Inspection, nil
}

// TestManagedWorkflowUpdateAndRecovery retains ownership through exact updates and not-started recovery.
func TestManagedWorkflowUpdateAndRecovery(t *testing.T) {
	r := newManagedRunner(t)
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	c.opts.Packages = r
	spec := project()
	spec.Workflow = &workflow.Spec{Steps: []workflow.Step{{ID: "infra", Package: "managed", Lifecycle: "managed", Config: map[string]any{"value": "first"}}}}
	status, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Planning must not invoke any native mutation.
	if status != 200 || r.calls.Load() != 0 {
		t.Fatal(status, plan, r.calls.Load())
	}
	// The same managed plan is reused by later deployment phases without applying twice.
	if op := resourceApply(t, c, plan, "first"); op["status"] != "succeeded" || r.calls.Load() != 1 {
		t.Fatal(op, r.calls.Load())
	}
	key := c.state.WorkflowNamespace + "/sample/infra"
	first := c.state.WorkflowRecords[key]
	spec.Workflow.Steps[0].Config["value"] = "second"
	status, plan = call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Changed desired state is reviewable under the original stable owner.
	if status != 200 {
		t.Fatal(status, plan)
	}
	r.fail.Store(true)
	// A failed call retains the prior successful receipt while marking this attempt uncertain.
	if op := resourceApply(t, c, plan, "failure"); op["status"] != "failed" {
		t.Fatal(op)
	}
	verifyManagedWorkflowRecovery(t, c, r, key, first)
	r.fail.Store(false)
	status, plan = call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Replanning requires a fresh explicit approval after the original owner was restored.
	if status != 200 || resourceApply(t, c, plan, "retry")["status"] != "succeeded" || r.calls.Load() != 2 {
		t.Fatal(status, plan, r.calls.Load())
	}
}

// verifyManagedWorkflowRecovery proves that not-started reconciliation restores the prior owner without applying.
func verifyManagedWorkflowRecovery(t *testing.T, c *Controller, r *managedRunner, key string, first *WorkflowRecord) {
	t.Helper()
	c.mu.Lock()
	retained := c.state.WorkflowRecords[key]
	c.mu.Unlock()
	// The original receipt must survive for an explicitly proven not-started reconciliation.
	if retained.Prior != first || retained.Status != "uncertain" {
		t.Fatal("lost prior owner")
	}
	status, review := call(t, c, true, "GET", "/v1/workflows/sample/infra", nil, "")
	// The builder verifies recovery includes the exact original previous state.
	if status != 200 || review["status"] != string(workflow.NotStarted) {
		t.Fatal(status, review)
	}
	status, body := call(t, c, true, "POST", "/v1/workflows/sample/infra", map[string]any{"approved": true, "fingerprint": review["fingerprint"]}, "")
	// Recovery changes only durable metadata and never executes a native update.
	if status != 200 || r.calls.Load() != 1 {
		t.Fatal(status, body, r.calls.Load())
	}
}

// TestManagedWorkflowRejectsUnresolvedInputs prevents approval of unknown future infrastructure values.
func TestManagedWorkflowRejectsUnresolvedInputs(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	c.opts.Packages = newManagedRunner(t)
	spec := project()
	spec.Workflow = &workflow.Spec{Steps: []workflow.Step{
		{ID: "producer", Package: "managed", Config: map[string]any{"value": "input"}},
		{ID: "infra", Package: "managed", Lifecycle: "managed", Needs: []string{"producer"}, Inputs: map[string]string{"input": "producer.value"}, Config: map[string]any{"value": "output"}},
	}}
	status, body := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// A newly planned producer cannot supply an exact downstream plan before it has executed.
	if status == 200 || !strings.Contains(managedTestJSON(body), "WORKFLOW_INPUTS_UNRESOLVED") {
		t.Fatal(status, body)
	}
}

// TestWorkflowRetainsPriorSecretReference prevents orphaning a restorable native checkpoint.
func TestWorkflowRetainsPriorSecretReference(t *testing.T) {
	record := &WorkflowRecord{SecretRefs: map[string]SecretRef{"token": {Name: "new"}}, Prior: &WorkflowRecord{SecretRefs: map[string]SecretRef{"token": {Name: "old"}}}}
	// Both generations remain necessary until the update outcome is known.
	if !workflowRecordReferences(record, "new") || !workflowRecordReferences(record, "old") || workflowRecordReferences(record, "unused") {
		t.Fatal("invalid credential retention")
	}
}

// managedTestJSON encodes public API evidence without revealing private fixture state.
func managedTestJSON(value any) string { raw, _ := json.Marshal(value); return string(raw) }
