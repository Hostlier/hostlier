package controller

import (
	"context"
	"testing"
	"time"

	"hostlier/internal/client"
)

func TestRecentInstanceSecretRegistrationReusesCompletedApproval(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, actor, _ := pairedScopeFixture(t, c, deploymentScope{})
	propose := func(api *client.Client, value, requestID string) map[string]any {
		t.Helper()
		var approval map[string]any
		body := map[string]any{"value": value, "requestId": requestID, "owner": "margin-test"}
		if err := api.Request(context.Background(), "POST", "/v1/approvals", map[string]any{"method": "PUT", "path": "/v1/secrets/margin-test-key", "body": body}, &approval); err != nil {
			t.Fatal(err)
		}
		return approval
	}
	execute := func(api *client.Client, approval map[string]any) (map[string]any, error) {
		var result map[string]any
		err := api.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result)
		return result, err
	}
	first := propose(api, "private-one", "first")
	firstResult, err := execute(api, first)
	if err != nil {
		t.Fatal(err)
	}
	approvalCount := len(c.state.HumanApprovals)
	repeated := propose(api, "private-one", "second")
	if repeated["id"] != first["id"] || repeated["status"] != "completed" || len(c.state.HumanApprovals) != approvalCount {
		t.Fatal("unchanged instance secret created another approval")
	}
	repeatedResult, err := execute(api, repeated)
	if err != nil || repeatedResult["revision"] != firstResult["revision"] || len(c.state.Secrets["margin-test-key"].Revisions) != 1 {
		t.Fatal("unchanged instance secret created another revision", err)
	}
	if c.state.Writes[verifier(actor+"|second")].Revision != firstResult["revision"] {
		t.Fatal("new request ID did not retain its idempotency receipt")
	}
	otherAPI, _, _ := pairedScopeFixture(t, c, deploymentScope{})
	if other := propose(otherAPI, "private-one", "other-actor"); other["id"] == first["id"] {
		t.Fatal("another controller identity reused the approval")
	}
	changed := propose(api, "private-two", "changed")
	if changed["id"] == first["id"] {
		t.Fatal("a different value reused the approval")
	}
	changedResult, err := execute(api, changed)
	if err != nil || changedResult["revision"] == firstResult["revision"] {
		t.Fatal("changed secret did not publish a new revision", err)
	}
	conflict := propose(api, "private-two", "second")
	if conflict["id"] == first["id"] {
		t.Fatal("a reused request ID bypassed its original value")
	}
	if _, err := execute(api, conflict); err == nil {
		t.Fatal("a reused request ID changed meaning without an idempotency conflict")
	}
	changedID := changed["id"].(string)
	c.mu.Lock()
	c.state.HumanApprovals[changedID].ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	c.mu.Unlock()
	if expired := propose(api, "private-two", "after-expiry"); expired["id"] == changed["id"] {
		t.Fatal("an expired approval was reused")
	}
}
