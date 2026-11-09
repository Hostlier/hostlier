package controller

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestManagedRevocationSurvivesPruning prevents unrelated manual issuance from reviving a managed checkpoint.
func TestManagedRevocationSurvivesPruning(t *testing.T) {
	c, runtime, _ := managedFixture(t)
	// Establish the private active checkpoint and its persisted public verifier.
	if err := c.reconcileManagedCollector(context.Background()); err != nil {
		t.Fatal(err)
	}
	oldToken := runtime.attempts[0].Token
	credentials, err := c.readManagedCredentials()
	// Inspect only the fixture's own private credential metadata.
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, revokeErr := c.revokeTelemetryCollector(credentials.Active.ID)
	_, mintErr := c.createTelemetryCollector(httptest.NewRequest("POST", "/v1/telemetry/collectors", strings.NewReader(`{"name":"manual"}`)))
	c.mu.Unlock()
	// Both operations must complete before the background repair path is exercised.
	if revokeErr != nil || mintErr != nil {
		t.Fatalf("fixture credential administration: %v / %v", revokeErr, mintErr)
	}
	// A healthy replacement must use new authority despite intervening verifier pruning.
	if err = c.reconcileManagedCollector(context.Background()); err != nil {
		t.Fatal(err)
	}
	newToken := runtime.attempts[len(runtime.attempts)-1].Token
	// The old secret must remain unusable even though its checkpoint existed on disk.
	if newToken == oldToken || managedTokenValid(c, oldToken) || !managedTokenValid(c, newToken) {
		t.Fatal("revoked managed authority was resurrected")
	}
}
