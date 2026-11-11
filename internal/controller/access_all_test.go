package controller

import (
	"context"
	"hostlier/internal/client"
	"net/http"
	"testing"
	"time"
)

// allAccessExecute exercises both encrypted proposal and authenticated execution over HTTPS.
func allAccessExecute(t *testing.T, api *client.Client, method, path string, body any) {
	t.Helper()
	var approval map[string]any
	// Normal remote mutation receipts must work without another passkey ceremony.
	if err := api.Request(context.Background(), "POST", "/v1/approvals", map[string]any{"method": method, "path": path, "body": body}, &approval); err != nil {
		t.Fatal(err)
	}
	var result any
	// Execution still verifies the grant and the retained request's fingerprint and generation.
	if err := api.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result); err != nil {
		t.Fatal(err)
	}
}

// TestEmptyPairingGrantsAllAccess verifies actual issuance, arbitrary apps, secrets and administration.
func TestEmptyPairingGrantsAllAccess(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, actor, id := pairedScopeFixture(t, c, deploymentScope{})
	assertAllAccessGrant(t, c, actor, id)
	// Administrative reads are no longer restricted to one application's receipts.
	for _, path := range []string{"/v1/clients", "/v1/secrets", "/v1/status", "/v1/authorizations"} {
		var result any
		// A valid all-access credential must be admitted by the actual route guard.
		if err := api.Request(context.Background(), "GET", path, nil, &result); err != nil {
			t.Fatal(path, err)
		}
	}
	// Any secret can be created and subsequently updated through the normal receipt path.
	for _, revision := range []string{"first", "second"} {
		allAccessExecute(t, api, "PUT", "/v1/secrets/arbitrary-key", map[string]string{"value": revision, "requestId": revision})
	}
	// Overwrites must actually publish a second encrypted revision.
	if len(c.state.Secrets["arbitrary-key"].Revisions) != 2 {
		t.Fatal("secret overwrite did not execute")
	}
	approved := scopedProposalFixture(t, &humanFixture{c: c}, api, actor, "any-new-app", "arbitrary-key")
	// App and secret names are unrestricted without weakening scoped fixtures.
	if approved["status"] != "approved" || approved["authorizationId"] != id {
		t.Fatal("arbitrary app was not authorized", approved)
	}
}

// TestAllAccessCanPurgeWithoutPerInstanceApproval verifies an operator-approved
// service credential can complete scheduled trial cleanup unattended.
func TestAllAccessCanPurgeWithoutPerInstanceApproval(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, _, _ := pairedScopeFixture(t, c, deploymentScope{})
	spec := project()
	spec.Name = "expired-trial"
	c.mu.Lock()
	app := &Application{Spec: spec}
	app.Name = spec.Name
	c.state.Applications[spec.Name] = app
	c.mu.Unlock()
	var approval map[string]any
	if err := api.Request(context.Background(), "POST", "/v1/approvals", map[string]any{"method": "POST", "path": "/v1/apps/expired-trial/action", "body": map[string]any{"action": "delete", "purge": true}}, &approval); err != nil {
		t.Fatal(err)
	}
	if approval["status"] != "approved" {
		t.Fatal("service credential did not authorize scheduled purge", approval)
	}
	var result map[string]any
	if err := api.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result); err != nil {
		t.Fatal(err)
	}
	operation := wait(t, c, result["operationId"].(string))
	if operation["status"] != "succeeded" || operation["purge"] != true || operation["name"] != spec.Name {
		t.Fatal("scheduled purge was not confirmed", operation)
	}
}

// TestAllAccessRevocationStopsPendingExecution covers grant, device and owner-key revocation.
func TestAllAccessRevocationStopsPendingExecution(t *testing.T) {
	// Each case invalidates authority after a request was already approved.
	for _, kind := range []string{"grant", "device", "owner"} {
		// Isolate each revocation boundary in a real paired controller.
		t.Run(kind, func(t *testing.T) {
			c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
			defer closeController(t, c)
			api, actor, id := pairedScopeFixture(t, c, deploymentScope{})
			approval := scopedProposalFixture(t, &humanFixture{c: c}, api, actor, "new-app", "")
			// Synchronize fixture mutations with the background activity writer.
			c.mu.Lock()
			// Keep generation unchanged to test direct authority revalidation.
			switch kind {
			// Explicit grant revocation is immediate.
			case "grant":
				c.state.Authorizations[id].Revoked = true
			// Device revocation also invalidates its grant.
			case "device":
				c.state.Clients[actor].Revoked = true
			// Rotating the local administrator realm invalidates its issued authority.
			case "owner":
				c.state.HumanRealm = "new-realm"
			}
			c.mu.Unlock()
			var result any
			// Already approved writes cannot outlive their authorizing identity.
			if err := api.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result); err == nil {
				t.Fatal("revoked authority executed")
			}
		})
	}
}

// TestAllAccessHasNoLifetimeOrUseCeiling distinguishes unlimited grants from invalid legacy grants.
func TestAllAccessHasNoLifetimeOrUseCeiling(t *testing.T) {
	now := time.Now()
	// A controlled clock avoids waiting for real credential expiry.
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, func() time.Time { return now })
	defer closeController(t, c)
	_, actor, id := pairedScopeFixture(t, c, deploymentScope{})
	// Grant mutations and checks share the persistence writer's state lock.
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.state.Authorizations[id]
	g.Uses = 1000000
	now = now.AddDate(20, 0, 0)
	// Unlimited authority remains usable long after bounded windows have expired.
	if !c.authorizationLive(g) || !c.hasLiveAuthorization(actor) {
		t.Fatal("all-access credential expired or exhausted")
	}
	g.Scope = "deploy"
	// Existing zero-expiry restricted records must never acquire permanent access.
	if c.authorizationLive(g) {
		t.Fatal("legacy zero expiry gained authority")
	}
}

// TestPartialScopeNeverBecomesAllAccess rejects incomplete and malformed restrictions.
func TestPartialScopeNeverBecomesAllAccess(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	// Every nonempty permission selection must satisfy the bounded contract.
	for _, scope := range []deploymentScope{{Apps: []string{"app"}}, {Secrets: []string{"secret"}}, {CreateSecrets: []string{"secret"}}, {DurationSeconds: 60}, {MaxUses: 1}, {DurationSeconds: -1}, {Apps: []string{""}}} {
		// Neither validation nor mode selection can silently widen a restriction.
		if scope.allAccess() || c.validateDeploymentScope(scope) == nil {
			t.Fatal("partial selection granted authority", scope)
		}
	}
}

// assertAllAccessGrant verifies explicit persistent mode and unlimited limits.
func assertAllAccessGrant(t *testing.T, c *Controller, actor, id string) {
	t.Helper()
	g := c.state.Authorizations[id]
	// Zero values must be accompanied by the explicit all-access scope.
	if g.Scope != "all" || g.ExpiresAt != 0 || g.MaxUses != 0 || !c.hasLiveAuthorization(actor) {
		t.Fatal("all-access grant was not retained explicitly")
	}
}

// TestAllAccessSurvivesNewGrantsAndRestart verifies persistent unlimited grants are never expiry-pruned.
func TestAllAccessSurvivesNewGrantsAndRestart(t *testing.T) {
	dir := t.TempDir()
	c := newTestController(t, dir, &recordingRuntime{}, time.Now)
	// Close the currently open instance after the restart assertion completes.
	defer func() { closeController(t, c) }()
	api, actor, id := pairedScopeFixture(t, c, deploymentScope{})
	allAccessExecute(t, api, "POST", "/v1/authorizations", map[string]any{"device": actor, "host": "local", "scope": "deploy", "apps": []string{"limited-app"}, "durationSeconds": 60, "maxUses": 1})
	// Issuing another grant must retain the original unlimited administrative authority.
	if len(c.state.Authorizations) != 2 || c.state.Authorizations[id] == nil {
		t.Fatal("unlimited grant was pruned when creating a bounded grant")
	}
	closeController(t, c)
	c = newTestController(t, dir, &recordingRuntime{}, time.Now)
	api.HTTP = &http.Client{Transport: &fixtureTransport{handler: c.Handler()}}
	assertAllAccessGrant(t, c, actor, id)
	var result any
	// The saved token and device key must authenticate after a controller restart.
	if err := api.Request(context.Background(), "GET", "/v1/clients", nil, &result); err != nil {
		t.Fatal("persisted credential lost access", err)
	}
}
