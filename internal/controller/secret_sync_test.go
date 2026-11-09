package controller

import (
	"testing"
	"time"
)

// TestSecretSynchronizationReusesUnchangedRevision verifies ordinary API writes and durable idempotency.
func TestSecretSynchronizationReusesUnchangedRevision(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	first := secretSyncWrite(t, c, "same-private-value", "first")
	generation := c.state.Generation
	second := secretSyncWrite(t, c, "same-private-value", "second")
	// Unchanged desired state must not grow revision history or invalidate prepared deployments.
	if first != second || len(c.state.Secrets["sync-token"].Revisions) != 1 || c.state.Generation != generation {
		t.Fatal("unchanged synchronization created another revision")
	}
	third := secretSyncWrite(t, c, "changed-private-value", "third")
	// Changed values still create immutable revisions for future deployment and rollback.
	if third == first || len(c.state.Secrets["sync-token"].Revisions) != 2 {
		t.Fatal("changed value did not create a revision")
	}
	// Replaying the unchanged write returns its original revision after later updates.
	if replay := secretSyncWrite(t, c, "same-private-value", "second"); replay != second {
		t.Fatal("idempotent replay changed revision")
	}
}

// secretSyncWrite exercises the production encrypted secret endpoint using fixture administrator authority.
func secretSyncWrite(t *testing.T, c *Controller, value, requestID string) string {
	t.Helper()
	status, result := humanHTTP(t, c, true, "PUT", "/v1/secrets/sync-token", map[string]string{"value": value, "requestId": requestID}, "", "")
	// No fixture can claim success when the controller rejects the secret write.
	if status != 200 {
		t.Fatal(status, result)
	}
	return result["revision"].(string)
}
