package iac

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/packagekit/protected"
	"hostlier/pkg/workflow"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeEngine proves failure recovery without making native-engine conformance claims.
type fakeEngine struct {
	fail    bool
	applies int
}

// Name selects an isolated fixture store.
func (*fakeEngine) Name() string { return "fixture" }

// Guarantee advertises only fixture semantics.
func (*fakeEngine) Guarantee() string { return "Fixture: exact saved input" }

// Plan returns deliberately private artifact bytes.
func (*fakeEngine) Plan(context.Context, Execution) ([]byte, []string, error) {
	return []byte("private-saved-plan"), []string{"One local fixture mutation."}, nil
}

// Apply records a native side effect before the simulated lost response.
func (f *fakeEngine) Apply(ctx context.Context, e Execution, raw []byte) error {
	f.applies++
	// Reject an identity or visibility mismatch before publishing the operation result.
	if string(raw) != "private-saved-plan" {
		return errors.New("changed saved plan")
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err := os.WriteFile(filepath.Join(e.Backend, "native"), []byte(e.Operation), 0600); err != nil {
		return err
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if f.fail {
		return errors.New("lost native response")
	}
	return nil
}

// Outputs publishes a single explicitly declared public binding.
func (*fakeEngine) Outputs(context.Context, Execution) (Outputs, error) {
	return Outputs{Public: map[string]string{"value": "success"}}, nil
}

// Revision observes only the authoritative native fixture checkpoint.
func (*fakeEngine) Revision(e Execution) (string, error) {
	raw, err := os.ReadFile(filepath.Join(e.Backend, "native"))
	// Stop this phase when private storage or native execution cannot be verified.
	if os.IsNotExist(err) {
		return "absent", nil
	}
	return string(raw), err
}

// fakePackage creates one pinned administrator-installed fixture program.
func fakePackage(t *testing.T, f *fakeEngine) (packagekit.Handler, workflow.Request, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(root, 0700)
	bundle := filepath.Join(root, "bundle")
	os.Mkdir(bundle, 0700)
	os.WriteFile(filepath.Join(bundle, "program"), []byte("pinned program"), 0600)
	sum := sha256.Sum256([]byte("pinned program"))
	installed := Installed{Bundle: protected.Bundle{Root: bundle, Platform: runtime.GOOS + "/" + runtime.GOARCH, Files: map[string]string{"program": hex.EncodeToString(sum[:])}}, Outputs: []string{"value"}}
	handler, err := New(f, map[string]Installed{"fixture": installed})
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	req := workflow.Request{Version: 1, Action: "plan", Key: "namespace/app/step", Step: workflow.Step{ID: "step", Package: "fixture", Config: map[string]any{"bundle": "fixture"}}, Host: &workflow.HostContext{StateDir: filepath.Join(root, "state")}}
	return handler, req, bundle
}

// prepare performs only the existing package plan exchange.
func prepare(t *testing.T, h packagekit.Handler, r workflow.Request) workflow.Request {
	t.Helper()
	plan, err := h.Handle(context.Background(), r)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	r.Prepared = plan.Prepared.Payload
	return r
}

// TestUncertainNativeFailureCannotReplay preserves a durable uncertain marker after a native effect.
func TestUncertainNativeFailureCannotReplay(t *testing.T) {
	engine := &fakeEngine{fail: true}
	handler, req, _ := fakePackage(t, engine)
	req = prepare(t, handler, req)
	req.Action = "apply"
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err := handler.Handle(context.Background(), req); err == nil {
		t.Fatal("expected lost response")
	}
	req.Action = "inspect"
	observed, err := handler.Handle(context.Background(), req)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil || observed.Inspection.Status != workflow.Uncertain {
		t.Fatal("unproven result", err)
	}
	req.Action = "apply"
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err = handler.Handle(context.Background(), req); err == nil {
		t.Fatal("replayed uncertain apply")
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if engine.applies != 1 {
		t.Fatal("native apply repeated")
	}
	req.Action = "plan"
	req.Prepared = nil
	req.Step.Config["variables"] = map[string]string{"changed": "intent"}
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err = handler.Handle(context.Background(), req); err == nil {
		t.Fatal("uncertain owner accepted a replacement plan")
	}

}

// TestArtifactToolchainAndOwnershipBinding rejects changed executable inputs and owner crossover.
func TestArtifactToolchainAndOwnershipBinding(t *testing.T) {
	engine := &fakeEngine{}
	handler, req, bundle := fakePackage(t, engine)
	req = prepare(t, handler, req)
	req.Action = "inspect"
	observed, err := handler.Handle(context.Background(), req)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil || observed.Inspection.Status != workflow.NotStarted {
		t.Fatal("untouched operation", err)
	}
	changed := req
	changed.Key = "namespace/other/step"
	changed.Action = "apply"
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err = handler.Handle(context.Background(), changed); err == nil {
		t.Fatal("cross owner artifact")
	}
	os.WriteFile(filepath.Join(bundle, "program"), []byte("changed"), 0600)
	req.Action = "apply"
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err = handler.Handle(context.Background(), req); err == nil {
		t.Fatal("changed toolchain")
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if engine.applies != 0 {
		t.Fatal("unapproved native effects")
	}
}

// TestCompletedReceiptSurvivesRestart adopts a proven result without native replay.
func TestCompletedReceiptSurvivesRestart(t *testing.T) {
	engine := &fakeEngine{}
	handler, req, _ := fakePackage(t, engine)
	req = prepare(t, handler, req)
	req.Action = "apply"
	applied, err := handler.Handle(context.Background(), req)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	req.Action = "inspect"
	observed, err := handler.Handle(context.Background(), req)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil || observed.Inspection.Status != workflow.Completed {
		t.Fatal("durable receipt", err)
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if observed.Inspection.Result.Outputs["value"] != applied.Result.Outputs["value"] {
		t.Fatal("receipt changed")
	}
	req.Action = "apply"
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err = handler.Handle(context.Background(), req); err == nil {
		t.Fatal("completed native operation replayed")
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if engine.applies != 1 {
		t.Fatal("native replay")
	}
}
