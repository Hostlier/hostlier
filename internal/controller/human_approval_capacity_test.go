package controller

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApprovalCapacityReclaimsOldCompletedReceipts(t *testing.T) {
	f := newHumanFixture(t)
	c := f.c
	now := c.opts.Now()
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("old-%d", i)
		c.state.HumanApprovals[id] = &HumanApproval{ID: id, Status: "completed", ExpiresAt: now.Add(-2 * time.Hour).UnixMilli()}
	}
	for _, item := range []struct {
		id, status string
		expires    time.Time
	}{
		{"recent", "completed", now.Add(-30 * time.Minute)},
		{"pending", "pending", now.Add(-2 * time.Hour)},
		{"executing", "executing", now.Add(-2 * time.Hour)},
	} {
		c.state.HumanApprovals[item.id] = &HumanApproval{ID: item.id, Status: item.status, ExpiresAt: item.expires.UnixMilli()}
	}
	req := httptest.NewRequest("POST", "/v1/approvals", strings.NewReader(`{"method":"PUT","path":"/v1/secrets/capacity-key","body":{"value":"private","requestId":"capacity"}}`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.proposeHumanRequest(req, "capacity-test-actor"); err != nil {
		t.Fatal("old completed approvals blocked new work:", err)
	}
	for _, id := range []string{"recent", "pending", "executing"} {
		if c.state.HumanApprovals[id] == nil {
			t.Fatal("reclaimed a recent or active approval:", id)
		}
	}
	if c.state.HumanApprovals["old-0"] != nil || len(c.state.HumanApprovals) != 4 {
		t.Fatal("old completed approvals were not reclaimed under capacity pressure")
	}
}
