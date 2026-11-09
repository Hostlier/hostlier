package engines

import (
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"hostlier/pkg/packagekit/contracttest"
	"hostlier/pkg/workflow"
	"strings"
	"testing"
)

// TestInspectProtocolRejectsUnapprovedPayload verifies recovery cannot escape reviewed request identity.
func TestInspectProtocolRejectsUnapprovedPayload(t *testing.T) {
	req := remoteRequest("redis")
	req.Action = "inspect"
	req.Prepared = json.RawMessage(`{"Fingerprint":"wrong"}`)
	_, err := (Package{Engine: "redis"}).Handle(context.Background(), req)
	// Bad approval must fail before any network discovery starts.
	if err == nil {
		t.Fatal("unapproved inspection accepted")
	}
	contracttest.RejectMalformed(t, Package{Engine: "redis"})
}

// TestPackageProtocolPlan exercises the shared executable boundary and plan-only response shape.
func TestPackageProtocolPlan(t *testing.T) {
	response := contracttest.Exchange(t, Package{Engine: "postgres"}, remoteRequest("postgres"))
	// Plans must not manufacture final outputs before approval and execution.
	if response.Prepared == nil || response.Result != nil || response.Inspection != nil {
		t.Fatal("invalid plan shape")
	}
}

// TestRedisACLVerificationRejectsScopeDrift tests authoritative ownership evidence without mutations.
func TestRedisACLVerificationRejectsScopeDrift(t *testing.T) {
	req := remoteRequest("redis")
	p := payload{Credentials: mustCredentials(t, req)}
	c := Config{Mode: "existing"}
	// A name or successful login alone cannot establish namespace isolation.
	if validRedisACL([]any{"flags", []any{"on"}, "passwords", []any{}, "keys", "~*", "commands", redisCommands, "channels", "", "selectors", []any{}}, req, c, p) {
		t.Fatal("foreign ACL accepted")
	}
	// Redis command order is an encoding detail, while each command remains required.
	if !sameCommands(strings.Join(reverse(strings.Fields(redisCommands)), " ")) || sameCommands(redisCommands+" +flushall") {
		t.Fatal("command scope mismatch")
	}
}

// mustCredentials constructs the exact generated approved credential shape for protocol fixtures.
func mustCredentials(t *testing.T, req workflow.Request) model.ResourceCredentials {
	t.Helper()
	value, err := credentials(req)
	// Random credential generation is required for a meaningful ownership fixture.
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// reverse returns the command set in a distinct wire order.
func reverse(values []string) []string {
	// Reordering must preserve all permission tokens exactly once.
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
	return values
}
