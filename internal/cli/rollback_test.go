package cli

import (
	"fmt"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// rollbackCase describes retained history and the expected CLI target or diagnostic.
type rollbackCase struct {
	name, current, explicit, want, failure string
	history                                []string
}

// rollbackFixture observes revision selection and the existing exact approval flow.
type rollbackFixture struct {
	t                              *testing.T
	test                           rollbackCase
	reads, plans, applies, prompts int
	stale                          bool
}

// TestRollbackRevisionSelection exercises direct and grouped spellings through plan approval.
func TestRollbackRevisionSelection(t *testing.T) {
	// Selection must fail closed when the active revision has no usable predecessor.
	for _, test := range []rollbackCase{
		{name: "previous", current: "third", history: []string{"first", "second", "third"}, want: "second"},
		{name: "explicit", current: "third", history: []string{"first", "second", "third"}, explicit: "first", want: "first"},
		{name: "explicit without history", explicit: "retained", want: "retained"},
		{name: "after rollback", current: "rollback-operation", history: []string{"first", "second", "rollback-operation"}, want: "second"},
		{name: "active predecessor", current: "second", history: []string{"first", "second", "third"}, want: "first"},
		{name: "first deployment", current: "first", history: []string{"first"}, failure: "No previous revision"},
		{name: "empty history", current: "first", failure: "No previous revision"},
		{name: "missing active revision", current: "missing", history: []string{"first", "second"}, failure: "No previous revision"},
		{name: "unset active revision", history: []string{"first", "second"}, failure: "No previous revision"},
		{name: "empty predecessor", current: "second", history: []string{"", "second"}, failure: "No previous revision"},
	} {
		// Both spellings must select and approve the same immutable target.
		for _, prefix := range [][]string{nil, {"app"}} {
			// Each invocation gets independent history and approval counters.
			t.Run(test.name+strings.Join(prefix, ""), func(t *testing.T) {
				fixture := &rollbackFixture{t: t, test: test}
				fixture.check(fixture.run(prefix), 1)
			})
		}
	}
}

// run invokes the real CLI with an isolated working directory and explicit terminal approval.
func (f *rollbackFixture) run(prefix []string) error {
	args := append(append([]string(nil), prefix...), "rollback", "app", "--detach")
	// An explicitly selected revision must be forwarded without inference.
	if f.test.explicit != "" {
		args = append(args, "--revision", f.test.explicit)
	}
	_, err := invoke(args, "", Options{CWD: f.t.TempDir(), Client: &fakeClient{reply: f.reply}, Prompt: f.approve})
	return err
}

// approve records one affirmative answer for each reviewed plan.
func (f *rollbackFixture) approve(string, bool) (string, error) {
	f.prompts++
	return "yes", nil
}

// reply limits the fixture to history reads, rollback plans and approved application.
func (f *rollbackFixture) reply(c call) (any, error) {
	// Unknown requests would indicate that grouping or rollback changed dispatch.
	switch c.path {
	case "/v1/apps/app": // Return retained IDs without requiring a project file.
		f.reads++
		history := make([]map[string]string, 0, len(f.test.history))
		// Keep the controller's chronological history order.
		for _, id := range f.test.history {
			history = append(history, map[string]string{"id": id})
		}
		return map[string]any{"spec": model.Project{Name: "app", Route: "app.example.com"}, "revision": f.test.current, "revisions": history}, nil
	case "/v1/plans": // Validate that retries keep the same immutable target.
		return f.plan(c)
	case "/v1/plans/plan/apply": // Require exact approval before acceptance.
		return f.apply(c)
	default: // No other controller effects belong to this flow.
		return nil, fmt.Errorf("unexpected request: %s", c.path)
	}
}

// plan rejects a changed target or an accidental ordinary deployment request.
func (f *rollbackFixture) plan(c call) (any, error) {
	f.plans++
	body := c.body.(map[string]any)
	// The controller must always receive an exact retained revision ID.
	if body["action"] != "rollback" || body["revision"] != f.test.want {
		f.t.Fatalf("unexpected rollback intent: %+v", body)
	}
	return map[string]string{"id": "plan", "fingerprint": "exact-plan"}, nil
}

// apply verifies approval and optionally invalidates the first reviewed plan.
func (f *rollbackFixture) apply(c call) (any, error) {
	f.applies++
	body := c.body.(map[string]any)
	// Every attempt must follow a fresh affirmative review of its exact fingerprint.
	if f.prompts != f.applies || body["approved"] != true || body["fingerprint"] != "exact-plan" {
		f.t.Fatalf("applied without exact approval: %+v", body)
	}
	// Stale-plan recovery must review again without selecting a new revision.
	if f.stale && f.applies == 1 {
		return nil, model.Err("STALE_PLAN", "Changed")
	}
	return map[string]string{"operationId": "operation"}, nil
}

// check compares observable calls for successful selection and history failures.
func (f *rollbackFixture) check(err error, attempts int) {
	f.t.Helper()
	// Invalid history must fail before a plan or approval request is created.
	if f.test.failure != "" {
		// A selection failure must name the missing-history condition.
		if err == nil || !strings.Contains(err.Error(), f.test.failure) {
			f.t.Fatalf("expected %s, got %v", f.test.failure, err)
		}
		attempts = 0
	} else if err != nil { // Valid history must reach the approved operation receipt.
		f.t.Fatal(err)
	}
	// Selection reads history once, even if the approved plan becomes stale.
	if f.reads != 1 || f.plans != attempts || f.applies != attempts || f.prompts != attempts {
		f.t.Fatalf("reads=%d plans=%d applies=%d prompts=%d", f.reads, f.plans, f.applies, f.prompts)
	}
}

// TestRollbackDefaultRetainsTargetOnStalePlan prevents retries from choosing a different revision.
func TestRollbackDefaultRetainsTargetOnStalePlan(t *testing.T) {
	fixture := &rollbackFixture{t: t, stale: true, test: rollbackCase{current: "second", history: []string{"first", "second"}, want: "first"}}
	fixture.check(fixture.run([]string{"app"}), 2)
}

// TestRollbackRequiresApplication rejects incomplete commands before contacting the controller.
func TestRollbackRequiresApplication(t *testing.T) {
	f := &fakeClient{}
	_, err := invoke([]string{"rollback"}, "", Options{CWD: t.TempDir(), Client: f})
	// The optional revision flag does not make the application name optional.
	if err == nil || !strings.Contains(err.Error(), "rollback APP [--revision REVISION]") || len(f.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, f.calls)
	}
}
