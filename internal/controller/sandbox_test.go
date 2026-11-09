package controller

import (
	"context"
	"errors"
	"testing"

	"hostlier/internal/client"
)

// errNotDelegated marks a request left for owner approval.
var errNotDelegated = errors.New("not delegated")

// sandboxGrantFixture obtains a genuinely approved prefix window for a signed device.
func sandboxGrantFixture(t *testing.T, f *humanFixture, device string, extra map[string]any) *Authorization {
	t.Helper()
	body := map[string]any{"device": device, "host": "local", "appPrefix": "try-", "maxApps": 2, "scope": "deploy", "durationSeconds": 3600, "maxUses": 20}
	// Merge case-specific sandbox options into the default window.
	for key, value := range extra {
		body[key] = value
	}
	a := f.propose(t, "POST", "/v1/authorizations", body)
	status, result := approveFixtureRequest(t, f, a)
	// Stop when fixture setup fails.
	if status != 200 {
		t.Fatal(status, result)
	}
	return f.c.state.Authorizations[result["id"].(string)]
}

// sandboxDeploy plans and applies one app through the device's delegated window.
func sandboxDeploy(t *testing.T, f *humanFixture, api *client.Client, name, route string) (string, error) {
	t.Helper()
	spec := project()
	spec.Name, spec.Route = name, route
	var plan map[string]any
	// Planning is the first point where prefix and capacity are enforced.
	if err := api.Request(context.Background(), "POST", "/v1/plans", map[string]any{"spec": spec}, &plan); err != nil {
		return "", err
	}
	a := deviceCall(t, api, "POST", "/v1/approvals", map[string]any{"method": "POST", "path": "/v1/plans/" + plan["id"].(string) + "/apply", "body": map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": random()}})
	// Without delegated approval the request waits for the owner; report that as refusal.
	if a["status"] != "approved" {
		return "", errNotDelegated
	}
	result := deviceCall(t, api, "POST", "/v1/approvals/"+a["id"].(string)+"/execute", map[string]any{})
	operation := wait(t, f.c, result["operationId"].(string))
	// Deployment success, not acceptance, proves the window reached the runtime.
	if operation["status"] != "succeeded" {
		t.Fatal(operation)
	}
	return result["operationId"].(string), nil
}

// sandboxAction proposes one lifecycle action and returns the approval view or the rejection.
func sandboxAction(api *client.Client, name string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	err := api.Request(context.Background(), "POST", "/v1/approvals", map[string]any{"method": "POST", "path": "/v1/apps/" + name + "/action", "body": body}, &result)
	// A pending request means the owner, not the window, must decide.
	if err == nil && result["status"] != "approved" {
		err = errNotDelegated
	}
	return result, err
}

// TestSandboxRequestValidation rejects unbounded or ambiguous sandbox windows.
func TestSandboxRequestValidation(t *testing.T) {
	valid := authorizationRequest{Host: "local", AppPrefix: "try-", MaxApps: 3}
	// Each case changes one field of an otherwise valid request.
	for name, input := range map[string]authorizationRequest{
		"no hyphen":   {Host: "local", AppPrefix: "try", MaxApps: 3},
		"uppercase":   {Host: "local", AppPrefix: "Try-", MaxApps: 3},
		"no capacity": {Host: "local", AppPrefix: "try-", MaxApps: 0},
		"too many":    {Host: "local", AppPrefix: "try-", MaxApps: 21},
		"remote host": {Host: "edge", AppPrefix: "try-", MaxApps: 3},
		"bad domain":  {Host: "local", AppPrefix: "try-", MaxApps: 3, RouteDomain: "*.try.example.com"},
		"exact extra": {Host: "local", Apps: []string{"api"}, KeepApps: true},
	} {
		// Every invalid input must fail before a grant is recorded.
		if validateSandboxRequest(input) == nil {
			t.Fatal("accepted", name)
		}
	}
	// The valid baseline confirms the cases fail for their own reason.
	if err := validateSandboxRequest(valid); err != nil {
		t.Fatal(err)
	}
	// A prefix window needs no exact app list.
	if !validAuthorizationLimits(authorizationRequest{Scope: "deploy", AppPrefix: "try-", DurationSeconds: 60, MaxUses: 1}) {
		t.Fatal("prefix window rejected")
	}
}

// TestSandboxPrefixCapacityAndRoutes covers new names, the app cap, the prefix and the route domain.
func TestSandboxPrefixCapacityAndRoutes(t *testing.T) {
	f := newHumanFixture(t)
	api, actor, _, _ := deviceFixture(t, f.c)
	grant := sandboxGrantFixture(t, f, actor, map[string]any{"routeDomain": "try.example.com"})
	// Names outside the prefix are refused at planning.
	if _, err := sandboxDeploy(t, f, api, "api", "api.try.example.com"); err == nil {
		t.Fatal("name outside prefix accepted")
	}
	// Routes outside the delegated domain are refused at planning.
	if _, err := sandboxDeploy(t, f, api, "try-a", "try-a.example.com"); err == nil {
		t.Fatal("route outside domain accepted")
	}
	// Two new apps fit within the cap.
	for _, name := range []string{"try-a", "try-b"} {
		// Each first deployment runs without another owner ceremony.
		if _, err := sandboxDeploy(t, f, api, name, name+".try.example.com"); err != nil {
			t.Fatal(name, err)
		}
	}
	// A third concurrent app exceeds the cap.
	if _, err := sandboxDeploy(t, f, api, "try-c", "try-c.try.example.com"); err == nil {
		t.Fatal("capacity exceeded")
	}
	// Updating an owned app does not need new capacity.
	if _, err := sandboxDeploy(t, f, api, "try-a", "try-a.try.example.com"); err != nil {
		t.Fatal(err)
	}
	assertSandboxOwnership(t, api, grant)
}

// assertSandboxOwnership checks recorded ownership and the device's status access.
func assertSandboxOwnership(t *testing.T, api *client.Client, grant *Authorization) {
	t.Helper()
	// Ownership is recorded on the grant that created each app.
	if len(grant.SandboxApps) != 2 || !sandboxOwned(grant, "try-a") || !sandboxOwned(grant, "try-b") {
		t.Fatal(grant.SandboxApps)
	}
	// The device can read status for apps its sandbox created.
	if status := deviceCall(t, api, "GET", "/v1/apps/try-a", nil); status["name"] != "try-a" {
		t.Fatal(status)
	}
}

// TestSandboxCannotAdoptOwnerApps keeps prefix authority away from apps the sandbox did not create.
func TestSandboxCannotAdoptOwnerApps(t *testing.T) {
	f := newHumanFixture(t)
	api, actor, _, _ := deviceFixture(t, f.c)
	sandboxGrantFixture(t, f, actor, nil)
	spec := project()
	spec.Name, spec.Route = "try-owner", "try-owner.example.com"
	f.c.mu.Lock()
	f.c.state.Applications["try-owner"] = &Application{Spec: spec}
	f.c.mu.Unlock()
	// An existing app with a matching prefix is not adoptable.
	if _, err := sandboxDeploy(t, f, api, "try-owner", "try-owner.example.com"); err == nil {
		t.Fatal("owner app adopted")
	}
	// Lifecycle actions on it are refused too.
	if _, err := sandboxAction(api, "try-owner", map[string]any{"action": "stop"}); err == nil {
		t.Fatal("owner app stopped")
	}
}

// TestSandboxLifecycleActions lets the device stop and delete its own apps, never purge them.
func TestSandboxLifecycleActions(t *testing.T) {
	f := newHumanFixture(t)
	api, actor, _, _ := deviceFixture(t, f.c)
	sandboxGrantFixture(t, f, actor, nil)
	// Create one app through the window.
	if _, err := sandboxDeploy(t, f, api, "try-a", "try-a.example.com"); err != nil {
		t.Fatal(err)
	}
	// Purge stays with the owner.
	if _, err := sandboxAction(api, "try-a", map[string]any{"action": "delete", "purge": true}); err == nil {
		t.Fatal("purge delegated")
	}
	// Each ordinary action is approved, executed and observable by the device.
	for _, action := range []string{"stop", "start", "delete"} {
		a, err := sandboxAction(api, "try-a", map[string]any{"action": action})
		// Owned apps receive delegated approval.
		if err != nil || a["status"] != "approved" {
			t.Fatal(action, a, err)
		}
		result := deviceCall(t, api, "POST", "/v1/approvals/"+a["id"].(string)+"/execute", map[string]any{})
		operation := deviceCall(t, api, "GET", "/v1/operations/"+result["operationId"].(string), nil)
		// The device can follow its own operation.
		if operation["name"] != "try-a" {
			t.Fatal(operation)
		}
		// Completion proves the action reached the runtime.
		if done := wait(t, f.c, result["operationId"].(string)); done["status"] != "succeeded" {
			t.Fatal(action, done)
		}
	}
}

// endSandboxFixture deploys one app, revokes the window and runs one teardown sweep.
func endSandboxFixture(t *testing.T, keep bool) (*humanFixture, *Authorization, string) {
	t.Helper()
	f := newHumanFixture(t)
	api, actor, _, _ := deviceFixture(t, f.c)
	grant := sandboxGrantFixture(t, f, actor, map[string]any{"keepApps": keep})
	// Create one app through the window.
	if _, err := sandboxDeploy(t, f, api, "try-a", "try-a.example.com"); err != nil {
		t.Fatal(err)
	}
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	grant.Revoked = true
	f.c.teardownSandboxes()
	// Teardown operations are attributed to the ending grant.
	for id, o := range f.c.state.Operations {
		// Return the one queued teardown delete.
		if o.Actor == "sandbox:"+grant.ID {
			return f, grant, id
		}
	}
	return f, grant, ""
}

// TestSandboxTeardownOnEnd deletes owned apps, without purging, when a window ends.
func TestSandboxTeardownOnEnd(t *testing.T) {
	f, grant, pending := endSandboxFixture(t, false)
	// Default sandboxes queue a delete and record completion.
	if pending == "" || !grant.TornDown {
		t.Fatal("teardown not queued", grant)
	}
	// The queued delete must finish and leave a retained deleted record.
	if done := wait(t, f.c, pending); done["status"] != "succeeded" || f.c.state.Applications["try-a"].Status != "deleted" {
		t.Fatal(done)
	}
}

// TestSandboxKeepAppsSkipsTeardown leaves apps running when the owner chose to keep them.
func TestSandboxKeepAppsSkipsTeardown(t *testing.T) {
	_, grant, pending := endSandboxFixture(t, true)
	// Neither deletion nor completion may be recorded.
	if pending != "" || grant.TornDown {
		t.Fatal("kept app torn down")
	}
}

// TestSandboxWidensRestrictedCredential lets a restricted device use a sandbox window granted to it.
func TestSandboxWidensRestrictedCredential(t *testing.T) {
	f := newHumanFixture(t)
	api, actor, _, _ := deviceFixture(t, f.c)
	exact := grantFixture(t, f, actor, 5)
	f.c.mu.Lock()
	f.c.state.Clients[actor].DeploymentGrant = exact.ID
	f.c.mu.Unlock()
	// Without a sandbox window, prefixed names stay outside the restricted credential.
	if _, err := sandboxDeploy(t, f, api, "try-a", "try-a.example.com"); err == nil {
		t.Fatal("restricted credential planned outside scope")
	}
	sandboxGrantFixture(t, f, actor, nil)
	// The live window now admits planning, approval and status for its own apps.
	if _, err := sandboxDeploy(t, f, api, "try-a", "try-a.example.com"); err != nil {
		t.Fatal(err)
	}
	// Status uses the same widened route check.
	if status := deviceCall(t, api, "GET", "/v1/apps/try-a", nil); status["name"] != "try-a" {
		t.Fatal(status)
	}
	// Names outside both grants remain refused.
	if _, err := sandboxDeploy(t, f, api, "other", "other.example.com"); err == nil {
		t.Fatal("unrelated name accepted")
	}
}
