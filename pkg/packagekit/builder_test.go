package packagekit

import (
	"context"

	"hostlier/pkg/workflow"
	"testing"
)

// testConfig is deliberately strict so schema drift is observable.
type testConfig struct {
	Name string `json:"name"`
}

// TestDefinition validates typed dispatch, strict config, secret aliases and secret output mapping.
func TestDefinition(t *testing.T) {
	// Exercise each controlled fixture path without real provider mutations.
	d, err := Define(Definition[testConfig, string, string, string]{Name: "fixture", Config: Codec[testConfig]{Schema: map[string]any{"type": "object"}}, Outputs: OutputCodec[string]{Codec: Codec[string]{Schema: map[string]any{"type": "string"}}, Names: []string{"credential", "status"}, Encode: func(v string) (map[string]string, map[string]string, error) {
		return map[string]string{"status": "ready"}, map[string]string{"credential": v}, nil
		// Exercise each controlled fixture path without real provider mutations.
	}}, Plan: func(context.Context, Call[testConfig, string]) (Plan[string], error) {
		return Plan[string]{Prepared: "saved", Summary: []string{"one change"}}, nil
		// Exercise each controlled fixture path without real provider mutations.
	}, Apply: func(context.Context, Call[testConfig, string], string) (Result[string, string], error) {
		return Result[string, string]{State: "receipt", Outputs: "secret-sentinel"}, nil
	}})
	// Verify the expected preparation and approval boundary.
	if err != nil {
		t.Fatal(err)
	}
	r := workflow.Request{Version: 1, Key: "operation", Action: "plan", Step: workflow.Step{Config: map[string]any{"name": "fixture"}}}
	response, err := d.Handle(context.Background(), r)
	// Verify the expected preparation and approval boundary.
	if err != nil || response.Prepared == nil {
		t.Fatal(err)
	}
	r.Action = "apply"
	r.Prepared = response.Prepared.Payload
	response, err = d.Handle(context.Background(), r)
	// Verify the expected preparation and approval boundary.
	if err != nil {
		t.Fatal(err)
	}
	// Verify the expected preparation and approval boundary.
	if response.Result.Outputs["credential"] != "" || response.Result.PrivateOutputs["credential"] != "secret-sentinel" {
		t.Fatal("private output was published")
	}
	r.Secrets = map[string]string{"unknown": "sentinel"}
	// Verify the expected preparation and approval boundary.
	if _, err = d.Handle(context.Background(), r); err == nil {
		t.Fatal("undeclared secret accepted")
	}
	r.Secrets = nil
	r.Step.Config["unknown"] = true
	// Verify the expected preparation and approval boundary.
	if _, err = d.Handle(context.Background(), r); err == nil {
		t.Fatal("unknown config accepted")
	}
}

// TestContractNames rejects duplicate and nonbinding capability aliases.
func TestContractNames(t *testing.T) {
	// Exercise each controlled fixture path without real provider mutations.
	for _, names := range [][]string{{"same", "same"}, {"BAD=VALUE"}, {"_leading"}} {
		// Verify the expected preparation and approval boundary.
		if validNames(names) {
			t.Fatal("invalid names accepted", names)
		}
	}
	// Verify the expected preparation and approval boundary.
	if !validNames([]string{"DB_URL", "endpoint"}) {
		t.Fatal("valid aliases rejected")
	}
}

// TestDeployHookDeferredInputs binds producer declarations and the immutable project
// while accepting newly provisioned values for exactly the reviewed producer aliases.
func TestDeployHookDeferredInputs(t *testing.T) {
	req := workflow.Request{Action: "plan", Key: "original", Project: []byte(`{"image":"pinned"}`), Step: workflow.Step{Hook: workflow.ProjectDeployHook, Phase: "beforeDeploy", Inputs: map[string]string{"token": "runner.credential", "cache": "redis.url"}}, Inputs: map[string]string{"cache": "known"}}
	deferred := deferredHookInputs(req)
	original := requestIdentity(req, deferred)
	req.Action = "apply"
	req.Inputs["token"] = "newly-provisioned"
	// A newly resolved value must not invalidate approval or mutate the request.
	if requestIdentity(req, deferred) != original || req.Inputs["token"] == "" {
		t.Fatal("late input did not remain bound to its reviewed producer")
	}
	req.Inputs["cache"] = "changed-known-value"
	// A known private value cannot change after the user reviewed it.
	if requestIdentity(req, deferred) == original {
		t.Fatal("known input value was not pinned")
	}
	req.Inputs["cache"] = "known"
	req.Step.Inputs["token"] = "other.credential"
	// Substituting a producer must invalidate the prepared identity.
	if requestIdentity(req, deferred) == original {
		t.Fatal("changed producer accepted")
	}
}

// TestOutputSchemaBindings proves the same output definition drives discovery and runtime enforcement.
func TestOutputSchemaBindings(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{"url": map[string]any{"type": "string"}}}
	// Encode typed output values without duplicating property names in an allowlist.
	d, err := Define(Definition[string, string, string, map[string]string]{Name: "fixture", Config: Codec[string]{Schema: map[string]any{}}, Outputs: OutputCodec[map[string]string]{Codec: Codec[map[string]string]{Schema: schema}, Encode: func(o map[string]string) (map[string]string, map[string]string, error) { return o, nil, nil }},
		// This test exercises result validation directly, without provider side effects.
		Plan: func(context.Context, Call[string, string]) (Plan[string], error) { return Plan[string]{}, nil },
		// Define requires both lifecycle methods.
		Apply: func(context.Context, Call[string, string], string) (Result[string, map[string]string], error) {
			return Result[string, map[string]string]{}, nil
		}})
	// The schema alone advertises the allowed output.
	if err != nil || len(d.Capability().Outputs) != 1 || d.Capability().Outputs[0] != "url" {
		t.Fatal(err)
	}
	_, err = d.result(Result[string, map[string]string]{State: "receipt", Outputs: map[string]string{"url": "ready"}})
	// A declared output is publishable.
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.result(Result[string, map[string]string]{State: "receipt", Outputs: map[string]string{"surprise": "no"}})
	// Undeclared results still fail before they reach a consumer.
	if err == nil {
		t.Fatal("undeclared output was accepted")
	}
}
