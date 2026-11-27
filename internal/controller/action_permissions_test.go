package controller

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"hostlier/internal/cli"
	"hostlier/internal/client"
	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// actionProposal uses the signed public protocol, including immutable review creation.
func actionProposal(api *client.Client, path string, body any) (map[string]any, error) {
	var result map[string]any
	err := api.Request(context.Background(), "POST", "/v1/approvals", map[string]any{"method": "POST", "path": path, "body": body}, &result)
	return result, err
}

// TestActionPermissionEnrollment exercises the main CLI using an actual permission-scoped login.
func TestActionPermissionEnrollment(t *testing.T) {
	master := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	member := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, master)
	defer closeController(t, member)
	prepared := prepareJoinFixture(t, member, "worker")
	status, join := humanHTTP(t, master, false, "POST", "/v1/fleet-joins/request", prepared.Request, "", "")
	if status != 200 {
		t.Fatal(status, join)
	}
	path := "/v1/fleet-joins/" + join["code"].(string)
	denied, _, _ := pairedScopeFixture(t, master, deploymentScope{Actions: []string{model.AppsDeploy}, Apps: []string{"api"}, DurationSeconds: 3600, MaxUses: 2})
	var result any
	if err := denied.Request(context.Background(), "GET", path, nil, &result); err == nil {
		t.Fatal("deploy permission disclosed enrollment")
	}
	if _, err := actionProposal(denied, path+"/approve", fleet.JoinDecision{Fingerprint: join["fingerprint"].(string)}); err == nil {
		t.Fatal("deploy permission approved a server")
	}
	api, _, grantID := pairedScopeFixture(t, master, deploymentScope{Actions: []string{model.ServersAdd}, DurationSeconds: 3600, MaxUses: 1})
	if err := api.Request(context.Background(), "POST", path+"/approve", fleet.JoinDecision{Fingerprint: join["fingerprint"].(string)}, &result); err == nil {
		t.Fatal("direct mutation bypassed retained receipt")
	}
	options := cli.Options{Client: api, Home: t.TempDir(), Prompt: func(string, bool) (string, error) { return "yes", nil }, OpenBrowser: func(string) error { t.Fatal("approval opened browser"); return nil }}
	if err := cli.RunWithOptions(context.Background(), []string{"server", "approve", join["code"].(string), "--server", "master"}, strings.NewReader("yes\n"), io.Discard, io.Discard, options); err != nil {
		t.Fatal(err)
	}
	master.mu.Lock()
	defer master.mu.Unlock()
	if !master.state.FleetJoins[join["code"].(string)].Approved || master.state.Authorizations[grantID].Uses != 1 {
		t.Fatal("approval or use count missing")
	}
	if master.canAddServer(master.state.Authorizations[grantID].Device) {
		t.Fatal("exhausted grant still adds servers")
	}
}

// TestActionPermissionLifecycle checks independent lifecycle, purge, app and administration boundaries.
func TestActionPermissionLifecycle(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, _, grantID := pairedScopeFixture(t, c, deploymentScope{Actions: []string{model.AppsStop, model.AppsRemove}, Apps: []string{"api"}, DurationSeconds: 3600, MaxUses: 10})
	for _, tc := range []struct {
		app, action    string
		purge, allowed bool
	}{
		{"api", "stop", false, true}, {"api", "delete", false, true}, {"other", "stop", false, false},
		{"api", "start", false, false}, {"api", "restart", false, false}, {"api", "delete", true, false},
	} {
		approval, err := actionProposal(api, "/v1/apps/"+tc.app+"/action", map[string]any{"action": tc.action, "purge": tc.purge})
		if (err == nil && approval["status"] == "approved") != tc.allowed {
			t.Fatalf("%+v: %v %v", tc, approval, err)
		}
	}
	if _, err := actionProposal(api, "/v1/authorizations", map[string]any{"scope": "all"}); err == nil {
		t.Fatal("action grant issued authority")
	}
	approval, err := actionProposal(api, "/v1/apps/api/action", map[string]string{"action": "stop"})
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.state.Authorizations[grantID].Actions = []string{model.AppsRemove}
	c.mu.Unlock()
	var result any
	if err := api.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result); err == nil {
		t.Fatal("changed permission executed retained approval")
	}
}

// TestActionPermissionPlans verifies semantic permissions are captured by real server plans.
func TestActionPermissionPlans(t *testing.T) {
	for _, tc := range []struct {
		name, action          string
		actions               []string
		remove, zero, allowed bool
	}{
		{"deploy", "deploy", []string{model.AppsDeploy}, false, false, true},
		{"rollback-denied", "rollback", []string{model.AppsDeploy}, false, false, false},
		{"rollback", "rollback", []string{model.AppsRollback}, false, false, true},
		{"detach-denied", "deploy", []string{model.AppsDeploy}, true, false, false},
		{"detach", "deploy", []string{model.AppsDeploy, model.SidecarsRemove}, true, false, true},
		{"stop-denied", "deploy", []string{model.AppsDeploy}, false, true, false},
		{"stop", "deploy", []string{model.AppsDeploy, model.AppsStop}, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
			defer closeController(t, c)
			api, _, _ := pairedScopeFixture(t, c, deploymentScope{Actions: tc.actions, Apps: []string{"api"}, DurationSeconds: 3600, MaxUses: 3})
			spec := project()
			spec.Name = "api"
			prior := &Application{Spec: spec, Revisions: []Revision{{ID: "old", Spec: spec}}}
			prior.Name = "api"
			if tc.remove {
				prior.Spec.Sidecars = map[string]model.Project{"worker": {Image: spec.Image}}
			}
			c.mu.Lock()
			c.state.Applications["api"] = prior
			c.mu.Unlock()
			if tc.zero {
				zero := 0
				spec.Replicas = &zero
			}
			var plan Plan
			err := api.Request(context.Background(), "POST", "/v1/plans", deploymentPlanRequest{Spec: spec, Action: tc.action, Revision: "old"}, &plan)
			if (err == nil) != tc.allowed {
				t.Fatalf("plan allowed=%v: %v", tc.allowed, err)
			}
			if !tc.allowed {
				return
			}
			if len(plan.RequiredActions) == 0 {
				t.Fatal("plan omitted action permissions")
			}
			a, err := actionProposal(api, "/v1/plans/"+plan.ID+"/apply", map[string]any{"fingerprint": plan.Fingerprint, "approved": true, "requestId": random()})
			if err != nil || a["status"] != "approved" {
				t.Fatal(a, err)
			}
		})
	}
}

// TestActionPermissionFleet checks pinned, app-bound preparation and approval through the reverse session.
func TestActionPermissionFleet(t *testing.T) {
	f := newHumanFixture(t)
	member, _ := enrolledHostFixture(t, f, "worker")
	api, actor, grantID := pairedScopeFixture(t, f.c, deploymentScope{Actions: []string{model.AppsDeploy, model.AppsRollback}, Apps: []string{"api"}, DurationSeconds: 3600, MaxUses: 5})
	f.c.mu.Lock()
	g := f.c.state.Authorizations[grantID]
	g.Host = "worker"
	g.HostFingerprint = f.c.state.Hosts["worker"].Fingerprint
	f.c.mu.Unlock()
	spec := project()
	spec.Name = "api"
	path := "/v1/hosts/worker/proxy/plans"
	var plan Plan
	if err := api.Request(context.Background(), "POST", path, deploymentPlanRequest{Spec: spec}, &plan); err != nil {
		t.Fatal(err)
	}
	approval, err := actionProposal(api, path+"/"+plan.ID+"/apply", map[string]any{"fingerprint": plan.Fingerprint, "approved": true, "requestId": random()})
	if err != nil || approval["status"] != "approved" {
		t.Fatal(approval, err)
	}
	var result any
	if err := api.Request(context.Background(), "POST", "/v1/plans", deploymentPlanRequest{Spec: spec}, &result); err == nil {
		t.Fatal("remote grant prepared local plan")
	}
	spec.Name = "other"
	if err := api.Request(context.Background(), "POST", path, deploymentPlanRequest{Spec: spec}, &result); err == nil {
		t.Fatal("remote grant prepared another app")
	}
	member.mu.Lock()
	owner := "controller:" + strings.TrimPrefix(f.c.store.Authenticate("fleet-actor|"+actor), "hmac:")
	member.state.Operations["mine"] = &Operation{ID: "mine", Actor: owner}
	member.state.Operations["other"] = &Operation{ID: "other", Actor: "another-device"}
	member.mu.Unlock()
	for _, id := range []string{"mine", "other"} {
		err := api.Request(context.Background(), "GET", "/v1/hosts/worker/proxy/operations/"+id, nil, &result)
		if (err == nil) != (id == "mine") {
			t.Fatal("remote receipt ownership", id, err)
		}
	}
	// Revocation after proposal must prevent execution on the member.
	f.c.mu.Lock()
	g.Revoked = true
	f.c.mu.Unlock()
	if err := api.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result); err == nil {
		t.Fatal("revoked remote grant executed")
	}
}

// TestActionPermissionValidation rejects partial, unknown and unbounded action grants.
func TestActionPermissionValidation(t *testing.T) {
	valid := authorizationRequest{Scope: "actions", Host: "local", Actions: []string{model.ServersAdd}, DurationSeconds: 60, MaxUses: 1}
	if !validAuthorizationLimits(valid) {
		t.Fatal("valid action grant rejected")
	}
	for _, change := range []func(*authorizationRequest){
		func(g *authorizationRequest) { g.Actions = nil },
		func(g *authorizationRequest) { g.Actions = []string{"*"} },
		func(g *authorizationRequest) { g.Actions = []string{model.AppsDeploy} },
		func(g *authorizationRequest) { g.Actions = []string{model.ServersAdd, model.ServersAdd} },
		func(g *authorizationRequest) { g.MaxUses = 0 },
		func(g *authorizationRequest) { g.DurationSeconds = 0 },
		func(g *authorizationRequest) { g.Host = "worker" },
		func(g *authorizationRequest) { g.Scope = "deploy"; g.Apps = []string{"api"} },
	} {
		g := valid
		change(&g)
		if validAuthorizationLimits(g) {
			t.Fatal("invalid action grant accepted", g)
		}
	}
}

// TestActionPermissionsCannotFallBackToDeploy prevents legacy windows from bypassing explicit removal or stop rights.
func TestActionPermissionsCannotFallBackToDeploy(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, _, _ := pairedScopeFixture(t, c, deploymentScope{Apps: []string{"api"}, DurationSeconds: 3600, MaxUses: 3})
	spec := project()
	spec.Name = "api"
	prior := &Application{Spec: spec}
	prior.Name = "api"
	prior.Spec.Sidecars = map[string]model.Project{"worker": {Image: spec.Image}}
	c.mu.Lock()
	c.state.Applications["api"] = prior
	c.mu.Unlock()
	var result any
	if err := api.Request(context.Background(), "POST", "/v1/plans", deploymentPlanRequest{Spec: spec}, &result); err == nil {
		t.Fatal("legacy window removed sidecar")
	}
	zero := 0
	spec = prior.Spec
	spec.Replicas = &zero
	if err := api.Request(context.Background(), "POST", "/v1/plans", deploymentPlanRequest{Spec: spec}, &result); err == nil {
		t.Fatal("legacy window stopped capacity")
	}
}

// TestActionPermissionDelegation grants an existing login new actions through real owner-authorized execution.
func TestActionPermissionDelegation(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	owner, _, _ := pairedScopeFixture(t, c, deploymentScope{})
	operator, actor, _ := pairedScopeFixture(t, c, deploymentScope{Apps: []string{"api"}, DurationSeconds: 3600, MaxUses: 5})
	_, other, _ := pairedScopeFixture(t, c, deploymentScope{Apps: []string{"api"}, DurationSeconds: 3600, MaxUses: 5})
	allAccessExecute(t, owner, "POST", "/v1/authorizations", authorizationRequest{Device: actor, Host: "local", Scope: "actions", Actions: []string{model.ServersAdd}, DurationSeconds: 3600, MaxUses: 1})
	c.mu.Lock()
	if !c.canAddServer(actor) || c.canAddServer(other) {
		t.Error("grant was not bound to the selected login")
	}
	c.mu.Unlock()
	var result any
	if err := operator.Request(context.Background(), "GET", "/v1/hosts/identity", nil, &result); err != nil {
		t.Fatal("server-add identity read", err)
	}
	if _, err := actionProposal(operator, "/v1/authorizations", authorizationRequest{Device: other, Host: "local", Scope: "actions", Actions: []string{model.ServersAdd}, DurationSeconds: 3600, MaxUses: 1}); err == nil {
		t.Fatal("server-add permission delegated authority")
	}
}
