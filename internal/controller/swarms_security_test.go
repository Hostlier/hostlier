package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// TestSwarmPlanLifecycle proves group selection survives plan, approval, deployment and rollback.
func TestSwarmPlanLifecycle(t *testing.T) {
	c := swarmFixture(t)
	group := createSwarmFixture(t, c, "research")
	api, _, _ := pairedScopeFixture(t, c, deploymentScope{Swarm: group.Name, Actions: []string{model.AppsDeploy, model.AppsRollback}, DurationSeconds: 3600, MaxUses: 10})
	spec := project()
	spec.Swarm = group.Name
	var plan Plan
	if err := api.Request(context.Background(), "POST", "/v1/plans", deploymentPlanRequest{Spec: spec}, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SwarmID != group.ID {
		t.Fatal("plan lost group", plan.SwarmID)
	}
	approval, err := actionProposal(api, "/v1/plans/"+plan.ID+"/apply", map[string]any{"fingerprint": plan.Fingerprint, "approved": true, "requestId": random()})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		OperationID string `json:"operationId"`
	}
	if err := api.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result); err != nil {
		t.Fatal(err)
	}
	if op := wait(t, c, result.OperationID); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	c.mu.Lock()
	app := *c.state.Applications[spec.Name]
	op := *c.state.Operations[result.OperationID]
	c.mu.Unlock()
	if app.SwarmID != group.ID || op.SwarmID != group.ID {
		t.Fatal("deployment lost membership")
	}
	// Historic revisions retain the original spec; rollback must use current membership.
	if status, value := humanHTTP(t, c, true, "POST", "/v1/swarms/default/members/"+spec.Name, map[string]string{"from": group.ID}, "", ""); status != 200 {
		t.Fatal(status, value)
	}
	var ignored any
	if err := api.Request(context.Background(), "GET", "/v1/plans/"+plan.ID, nil, &ignored); err == nil {
		t.Fatal("old group read plan after move")
	}
	if err := api.Request(context.Background(), "GET", "/v1/operations/"+result.OperationID, nil, &ignored); err == nil {
		t.Fatal("old group read operation after move")
	}
	current := app.Spec
	current.Swarm = "default"
	status, value := humanHTTP(t, c, true, "POST", "/v1/plans", deploymentPlanRequest{Spec: current, Action: "rollback", Revision: app.Revision}, "", "")
	if status != 200 || value["swarmId"] != "default" {
		t.Fatal("rollback restored historic membership", status, value)
	}
}

// TestSwarmImmutableGrant checks delete/recreate, bounded administration and accepted-work fences.
func TestSwarmImmutableGrant(t *testing.T) {
	c := swarmFixture(t)
	group := createSwarmFixture(t, c, "research")
	admin, _, _ := pairedScopeFixture(t, c, deploymentScope{Swarm: group.Name, Actions: []string{model.SwarmsManage}, DurationSeconds: 3600, MaxUses: 10})
	if _, err := actionProposal(admin, "/v1/swarms", map[string]string{"name": "other"}); err == nil {
		t.Fatal("bound admin created unrelated group")
	}
	swarmAppFixture(t, c, "api", group)
	if _, err := actionProposal(admin, "/v1/swarms/default/members/api", map[string]string{"from": group.ID}); err == nil {
		t.Fatal("bound admin moved outside scope")
	}
	c.mu.Lock()
	c.state.Operations["interrupted"] = &Operation{ID: "interrupted", Name: "api", SwarmID: group.ID, Status: "interrupted"}
	c.mu.Unlock()
	if status, _ := humanHTTP(t, c, true, "POST", "/v1/swarms/default/members/api", map[string]string{"from": group.ID}, "", ""); status == 200 {
		t.Fatal("moved app with interrupted work")
	}
	c.mu.Lock()
	delete(c.state.Operations, "interrupted")
	delete(c.state.Applications, "api")
	c.mu.Unlock()
	allAccessExecute(t, admin, "DELETE", "/v1/swarms/"+group.ID, map[string]any{})
	replacement := createSwarmFixture(t, c, group.Name)
	if replacement.ID == group.ID {
		t.Fatal("identity reused")
	}
	var result any
	if err := admin.Request(context.Background(), "GET", "/v1/swarms/research", nil, &result); err == nil {
		t.Fatal("old grant inherited replacement identity")
	}
	if _, err := actionProposal(admin, "/v1/authorizations", authorizationRequest{Scope: "all"}); err == nil {
		t.Fatal("group admin granted authority")
	}
}

// TestSwarmFleetPermissions uses an enrolled K3s member over its outbound mutual-TLS gRPC session.
func TestSwarmFleetPermissions(t *testing.T) {
	f := newHumanFixture(t)
	host := swarmFixture(t)
	listener := fleetMasterFixture(t, f.c)
	pin, err := host.FleetFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	proposal := f.propose(t, "POST", "/v1/hosts", map[string]string{"name": "worker", "url": "https://unreachable.invalid:7464", "fingerprint": pin})
	if status, result := approveFixtureRequest(t, f, proposal); status != 200 {
		t.Fatal(status, result)
	}
	connectFleetFixture(t, host, f.c, listener.URL)
	group := createSwarmFixture(t, host, "research")
	hidden := createSwarmFixture(t, host, "private")
	swarmAppFixture(t, host, "api", group)
	swarmAppFixture(t, host, "hidden", hidden)
	owner, _, _ := pairedScopeFixture(t, f.c, deploymentScope{})
	api, actor, _ := pairedScopeFixture(t, f.c, deploymentScope{Apps: []string{"local-only"}, DurationSeconds: 3600, MaxUses: 10})
	allAccessExecute(t, owner, "POST", "/v1/authorizations", authorizationRequest{Device: actor, Host: "worker", Swarm: group.Name, Scope: "actions", Actions: []string{model.SwarmsView, model.AppsStop, model.AppsDeploy}, DurationSeconds: 3600, MaxUses: 10})
	prefix := "/v1/hosts/worker/proxy"
	var groups struct {
		Swarms []model.Swarm `json:"swarms"`
	}
	if err := api.Request(context.Background(), "GET", prefix+"/swarms", nil, &groups); err != nil || len(groups.Swarms) != 1 || groups.Swarms[0].ID != group.ID {
		t.Fatal(groups, err)
	}
	var apps struct {
		Applications []Application `json:"applications"`
	}
	if err := api.Request(context.Background(), "GET", prefix+"/swarms/research/apps", nil, &apps); err != nil || len(apps.Applications) != 1 || apps.Applications[0].Name != "api" {
		t.Fatal(apps, err)
	}
	var result any
	for _, path := range []string{"/apps/hidden", "/swarms/private", "/apps", "/logs", "/status"} {
		if err := api.Request(context.Background(), "GET", prefix+path, nil, &result); err == nil {
			t.Fatal("remote scope leaked", path)
		}
	}
	spec := project()
	spec.Name = "new-app"
	spec.Swarm = group.Name
	var plan Plan
	if err := api.Request(context.Background(), "POST", prefix+"/plans", deploymentPlanRequest{Spec: spec}, &plan); err != nil {
		t.Fatal(err)
	}
	if err := api.Request(context.Background(), "GET", prefix+"/plans/"+plan.ID, nil, &result); err != nil {
		t.Fatal("remote own group plan denied", err)
	}
	approval, err := actionProposal(api, prefix+"/apps/api/action", map[string]string{"action": "stop"})
	if err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	host.state.Operations["receipt"] = &Operation{ID: "receipt", Name: "api", SwarmID: group.ID, Status: "succeeded", Actor: "controller:" + strings.TrimPrefix(f.c.store.Authenticate("fleet-actor|"+actor), "hmac:")}
	host.mu.Unlock()
	if err := api.Request(context.Background(), "GET", prefix+"/operations/receipt", nil, &result); err != nil {
		t.Fatal(err)
	}
	if status, value := humanHTTP(t, host, true, "POST", "/v1/swarms/"+hidden.ID+"/members/api", map[string]string{"from": group.ID}, "", ""); status != 200 {
		t.Fatal(status, value)
	}
	for _, path := range []string{prefix + "/apps/api", prefix + "/operations/receipt", "/v1/approvals/" + approval["id"].(string)} {
		if err := api.Request(context.Background(), "GET", path, nil, &result); err == nil {
			t.Fatal("remote moved app leaked", path)
		}
	}
	if err := api.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result); err == nil {
		t.Fatal("remote stale approval executed")
	}
}

// TestSwarmLegacyReadBoundary keeps pre-group exact-app grants in default, including event reads.
func TestSwarmLegacyReadBoundary(t *testing.T) {
	c := swarmFixture(t)
	group := createSwarmFixture(t, c, "research")
	swarmAppFixture(t, c, "api", group)
	api, actor, _ := pairedScopeFixture(t, c, deploymentScope{Apps: []string{"api"}, DurationSeconds: 3600, MaxUses: 10})
	c.mu.Lock()
	c.state.Operations["old"] = &Operation{ID: "old", Name: "api", Actor: actor, Status: "succeeded"}
	c.mu.Unlock()
	var result any
	for _, path := range []string{"/v1/apps/api", "/v1/apps/api/logs", "/v1/operations/old"} {
		if err := api.Request(context.Background(), "GET", path, nil, &result); err == nil {
			t.Fatal("legacy grant crossed group", path)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, _ := http.NewRequest("GET", "/v1/operations/old/events", nil)
	if err := c.checkScopedRoute(r, c.state.Clients[actor]); err == nil {
		t.Fatal("legacy event stream crossed group")
	}
	// Preserve the exact legacy plan bytes: migration never rewrites fingerprints.
	data, _ := json.Marshal(c.state.Operations["old"])
	if strings.Contains(string(data), "swarmId") {
		t.Fatal("legacy operation rewritten")
	}
}
