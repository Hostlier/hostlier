package controller

import (
	"context"
	"testing"
	"time"
)

// TestScopedSecretCreationCannotOverwrite verifies the actual encrypted approval execution path.
func TestScopedSecretCreationCannotOverwrite(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, _, id := pairedFixture(t, c)
	grant := c.state.Authorizations[id]
	grant.Secrets, grant.CreateSecrets = []string{"new-key"}, []string{"new-key"}
	body := map[string]any{"method": "PUT", "path": "/v1/secrets/new-key", "body": map[string]string{"value": "private-test-value", "requestId": "create-once"}}
	var approval map[string]any
	// The public API must approve only the exact creation request.
	if err := api.Request(context.Background(), "POST", "/v1/approvals", body, &approval); err != nil {
		t.Fatal(err)
	}
	var result any
	path := "/v1/approvals/" + approval["id"].(string) + "/execute"
	// Execution creates the secret through the ordinary encrypted secret store.
	if err := api.Request(context.Background(), "POST", path, map[string]any{}, &result); err != nil {
		t.Fatal(err)
	}
	// Receipt replay must not create another secret revision.
	if err := api.Request(context.Background(), "POST", path, map[string]any{}, &result); err != nil {
		t.Fatal(err)
	}
	// Creation rights cannot be reused to overwrite an existing value.
	if err := api.Request(context.Background(), "POST", "/v1/approvals", body, &result); err == nil {
		t.Fatal("overwrite accepted")
	}
	body["path"] = "/v1/secrets/other"
	// Other names require separate owner approval, not a broad write capability.
	if err := api.Request(context.Background(), "POST", "/v1/approvals", body, &result); err == nil {
		t.Fatal("out of scope secret accepted")
	}
	// Secret creation has its own finite name bound rather than consuming deployments.
	if grant.Uses != 0 || len(c.state.Secrets["new-key"].Revisions) != 1 {
		t.Fatal("creation or replay consumed excess authority")
	}
}

// TestCreationRequiresExplicitScope validates absent secrets without implicitly granting writes.
func TestCreationRequiresExplicitScope(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	scope := deploymentScope{Apps: []string{"api"}, Secrets: []string{"new-key"}, DurationSeconds: 60, MaxUses: 1}
	// Use-only permission cannot create a missing secret.
	if err := c.validateDeploymentScope(scope); err == nil {
		t.Fatal("missing creation consent accepted")
	}
	scope.CreateSecrets = []string{"other"}
	// Creation names must also be in the approved use scope.
	if err := c.validateDeploymentScope(scope); err == nil {
		t.Fatal("creation outside use scope accepted")
	}
	scope.CreateSecrets = []string{"new-key"}
	// Explicit first-creation approval is valid before the secret exists.
	if err := c.validateDeploymentScope(scope); err != nil {
		t.Fatal(err)
	}
}

// TestSecretCreationRechecksAuthority rejects intervening creation, revocation and expiry.
func TestSecretCreationRechecksAuthority(t *testing.T) {
	// Each independent fixture starts with an explicitly approved missing name.
	for _, change := range []string{"created", "revoked", "expired"} {
		// Run each invalidation against the real approval execution endpoint.
		t.Run(change, func(t *testing.T) {
			c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
			defer closeController(t, c)
			api, _, id := pairedFixture(t, c)
			grant := c.state.Authorizations[id]
			grant.Secrets, grant.CreateSecrets = []string{"new-key"}, []string{"new-key"}
			body := map[string]any{"method": "PUT", "path": "/v1/secrets/new-key", "body": map[string]string{"value": "private-test-value", "requestId": "create-once"}}
			var approval map[string]any
			// Retain approval before invalidating its execution authority.
			if err := api.Request(context.Background(), "POST", "/v1/approvals", body, &approval); err != nil {
				t.Fatal(err)
			}
			// Simulate changes without advancing generation to isolate the explicit authority checks.
			switch change {
			// Another creator must not turn a first-write capability into overwrite permission.
			case "created":
				c.state.Secrets["new-key"] = &Secret{Current: "existing"}
			// Revocation applies to already prepared approvals too.
			case "revoked":
				grant.Revoked = true
			// Expired grants cannot execute a previously approved secret upload.
			case "expired":
				grant.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
			}
			var result any
			// The pending request must fail before it can publish secret revisions.
			if err := api.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result); err == nil {
				t.Fatal("invalidated creation executed")
			}
		})
	}
}
