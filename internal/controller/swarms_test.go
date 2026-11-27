package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/model"
	"hostlier/internal/store"
)

// swarmRuntime exercises controller behavior without changing the K3s adapter or touching a live cluster.
type swarmRuntime struct{ recordingRuntime }

func (r *swarmRuntime) Name() string { return "k3s" }

func swarmFixture(t *testing.T) *Controller {
	t.Helper()
	c := newTestController(t, t.TempDir(), &swarmRuntime{}, time.Now)
	t.Cleanup(func() { closeController(t, c) })
	return c
}

func createSwarmFixture(t *testing.T, c *Controller, name string) model.Swarm {
	t.Helper()
	status, result := humanHTTP(t, c, true, "POST", "/v1/swarms", map[string]string{"name": name}, "", "")
	if status != 200 {
		t.Fatal(status, result)
	}
	data, _ := json.Marshal(result)
	var group model.Swarm
	if err := json.Unmarshal(data, &group); err != nil {
		t.Fatal(err)
	}
	return group
}

func swarmAppFixture(t *testing.T, c *Controller, name string, group model.Swarm) {
	t.Helper()
	spec := project()
	spec.Name = name
	spec.Route = name + ".example.com"
	if group.ID != model.DefaultSwarm {
		spec.Swarm = group.Name
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Applications[name] = &Application{SwarmID: group.ID, Deployment: model.Deployment{Name: name, RuntimeID: "original-" + name, Status: "running", URL: spec.Route}, Spec: spec, Revision: "prior", Revisions: []Revision{{ID: "prior", Spec: spec}}}
	if err := c.store.Save(c.state); err != nil {
		t.Fatal(err)
	}
}

// TestSwarmLifecyclePreservesRuntime covers grouping, atomic moves, permanent default and downgrade fencing.
func TestSwarmLifecyclePreservesRuntime(t *testing.T) {
	c := swarmFixture(t)
	group := createSwarmFixture(t, c, "research")
	swarmAppFixture(t, c, "api", model.Swarm{ID: model.DefaultSwarm, Name: model.DefaultSwarm})
	c.mu.Lock()
	before := *c.state.Applications["api"]
	c.mu.Unlock()
	status, result := humanHTTP(t, c, true, "POST", "/v1/swarms/"+group.ID+"/members/api", map[string]string{"from": "default"}, "", "")
	if status != 200 {
		t.Fatal(status, result)
	}
	c.mu.Lock()
	after := *c.state.Applications["api"]
	version := c.state.Version
	c.mu.Unlock()
	if after.SwarmID != group.ID || after.Spec.Swarm != group.Name || version != 3 || !reflect.DeepEqual(before.Deployment, after.Deployment) || !reflect.DeepEqual(before.Revisions, after.Revisions) {
		t.Fatal("membership changed runtime identity/history", after)
	}
	if status, _ := humanHTTP(t, c, true, "DELETE", "/v1/swarms/"+group.ID, map[string]any{}, "", ""); status == 200 {
		t.Fatal("deleted occupied swarm")
	}
	if status, _ := humanHTTP(t, c, true, "DELETE", "/v1/swarms/default", map[string]any{}, "", ""); status == 200 {
		t.Fatal("deleted default")
	}
	if status, _ := humanHTTP(t, c, true, "POST", "/v1/swarms/default/members/api", map[string]string{"from": "default"}, "", ""); status == 200 {
		t.Fatal("stale source accepted")
	}
	if status, result := humanHTTP(t, c, true, "POST", "/v1/swarms/default/members/api", map[string]string{"from": group.ID}, "", ""); status != 200 {
		t.Fatal(status, result)
	}
	if status, result := humanHTTP(t, c, true, "DELETE", "/v1/swarms/"+group.ID, map[string]any{}, "", ""); status != 200 {
		t.Fatal(status, result)
	}
	if next := createSwarmFixture(t, c, group.Name); next.ID == group.ID {
		t.Fatal("deleted identity reused")
	}
}

// TestSwarmUpgradeBackfill retains legacy specs, receipts and default access across restart and metadata backup.
func TestSwarmUpgradeBackfill(t *testing.T) {
	dir := t.TempDir()
	c := newTestController(t, dir, &swarmRuntime{}, time.Now)
	for _, status := range []string{"running", "stopped", "sleeping", "deleted"} {
		spec := project()
		spec.Name = status
		c.mu.Lock()
		c.state.Applications[status] = &Application{Deployment: model.Deployment{Name: status, RuntimeID: "kept-" + status, Status: status}, Spec: spec}
		c.mu.Unlock()
	}
	c.mu.Lock()
	c.state.Swarms = nil
	c.state.SwarmVersion = 0
	c.state.Plans["old"] = &Plan{ID: "old", Fingerprint: "unchanged", Spec: project()}
	if err := c.store.Save(c.state); err != nil {
		t.Fatal(err)
	}
	c.mu.Unlock()
	closeController(t, c)
	c = newTestController(t, dir, &swarmRuntime{}, time.Now)
	c.mu.Lock()
	for name, app := range c.state.Applications {
		if app.SwarmID != "default" || app.Spec.Swarm != "" || app.RuntimeID != "kept-"+name {
			t.Error("backfill changed app", app)
		}
	}
	if c.state.Plans["old"].Fingerprint != "unchanged" || c.state.Version != 2 || c.state.SwarmVersion != 1 {
		t.Error("backfill changed legacy receipts")
	}
	c.mu.Unlock()
	savedGroup := createSwarmFixture(t, c, "research")
	swarmAppFixture(t, c, "grouped", savedGroup)
	bundle := filepath.Join(t.TempDir(), "state.tar")
	if err := c.store.Export(bundle, "", filepath.Join(t.TempDir(), "settings"), filepath.Join(t.TempDir(), "edge")); err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyExport(bundle); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err := store.Restore(bundle, restoredDir); err != nil {
		t.Fatal(err)
	}
	var restored State
	db, err := store.Open(restoredDir, &restored)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if restored.Version != 3 || restored.Swarms[savedGroup.ID] != savedGroup || restored.Applications["grouped"].SwarmID != savedGroup.ID {
		t.Fatal("restore lost group identity")
	}
	closeController(t, c)
	c = newTestController(t, dir, &swarmRuntime{}, time.Now)
	defer closeController(t, c)
	if _, err := c.swarm("research"); err != nil {
		t.Fatal("named swarm lost on restart", err)
	}
}

// TestSwarmRequiresK3s keeps the runtime migration independent from grouping writes.
func TestSwarmRequiresK3s(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	status, result := humanHTTP(t, c, true, "POST", "/v1/swarms", map[string]string{"name": "research"}, "", "")
	if status == 200 || code(result) != "SWARM_RUNTIME" {
		t.Fatal(status, result)
	}
}

// TestSwarmScopedDiscoveryAndMutation exercises signed credentials through the real dispatcher.
func TestSwarmScopedDiscoveryAndMutation(t *testing.T) {
	c := swarmFixture(t)
	research := createSwarmFixture(t, c, "research")
	private := createSwarmFixture(t, c, "private")
	swarmAppFixture(t, c, "api", research)
	swarmAppFixture(t, c, "hidden", private)
	view, _, _ := pairedScopeFixture(t, c, deploymentScope{Swarm: "research", Actions: []string{model.SwarmsView}, DurationSeconds: 3600, MaxUses: 5})
	var groups struct {
		Swarms []model.Swarm `json:"swarms"`
	}
	if err := view.Request(context.Background(), "GET", "/v1/swarms", nil, &groups); err != nil || len(groups.Swarms) != 1 || groups.Swarms[0].ID != research.ID {
		t.Fatal(groups, err)
	}
	var result any
	for _, path := range []string{"/v1/apps/api", "/v1/swarms/research/apps"} {
		if err := view.Request(context.Background(), "GET", path, nil, &result); err != nil {
			t.Fatal(path, err)
		}
	}
	for _, path := range []string{"/v1/apps/hidden", "/v1/swarms/private", "/v1/swarms/private/apps", "/v1/status", "/v1/apps", "/v1/logs"} {
		if err := view.Request(context.Background(), "GET", path, nil, &result); err == nil {
			t.Fatal("scope leak", path)
		}
	}
	if _, err := actionProposal(view, "/v1/apps/api/action", map[string]string{"action": "stop"}); err == nil {
		t.Fatal("view stopped app")
	}
	operator, _, _ := pairedScopeFixture(t, c, deploymentScope{Swarm: "research", Actions: []string{model.AppsStop}, DurationSeconds: 3600, MaxUses: 5})
	if a, err := actionProposal(operator, "/v1/apps/api/action", map[string]string{"action": "stop"}); err != nil || a["status"] != "approved" {
		t.Fatal(a, err)
	}
	if _, err := actionProposal(operator, "/v1/apps/hidden/action", map[string]string{"action": "stop"}); err == nil {
		t.Fatal("cross-swarm action")
	}
	manager, _, _ := pairedScopeFixture(t, c, deploymentScope{Actions: []string{model.SwarmsManage}, DurationSeconds: 3600, MaxUses: 5})
	allAccessExecute(t, manager, "POST", "/v1/swarms", map[string]string{"name": "ops"})
	if _, err := actionProposal(manager, "/v1/apps/api/action", map[string]string{"action": "stop"}); err == nil {
		t.Fatal("group manager gained workload authority")
	}
	assertSwarmMoveRevokes(t, c, operator, view, manager, research, private)
}

func assertSwarmMoveRevokes(t *testing.T, c *Controller, operator, view, manager *client.Client, from, to model.Swarm) {
	t.Helper()
	approval, err := actionProposal(operator, "/v1/apps/api/action", map[string]string{"action": "stop"})
	if err != nil {
		t.Fatal(err)
	}
	allAccessExecute(t, manager, "POST", "/v1/swarms/"+to.ID+"/members/api", map[string]string{"from": from.ID})
	var result any
	if err := operator.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result); err == nil {
		t.Fatal("pre-move approval executed")
	}
	if err := view.Request(context.Background(), "GET", "/v1/apps/api", nil, &result); err == nil {
		t.Fatal("old swarm retained read access")
	}
	if err := operator.Request(context.Background(), "GET", "/v1/approvals/"+approval["id"].(string), nil, &result); err == nil {
		t.Fatal("old swarm retained receipt access")
	}
}

// TestSwarmLegacyJSONUpgrade exercises the legacy import path without rewriting runtime identity.
func TestSwarmLegacyJSONUpgrade(t *testing.T) {
	dir := t.TempDir()
	initial := State{Version: 2, Runtime: "k3s", Applications: map[string]*Application{}, Plans: map[string]*Plan{}}
	for _, status := range []string{"running", "stopped", "sleeping", "deleted"} {
		spec := project()
		spec.Name = status
		initial.Applications[status] = &Application{Deployment: model.Deployment{Name: status, RuntimeID: "old-" + status, Status: status}, Spec: spec}
	}
	initial.Plans["old"] = &Plan{ID: "old", Fingerprint: "untouched", Spec: project()}
	raw, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	c := newTestController(t, dir, &swarmRuntime{}, time.Now)
	defer closeController(t, c)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Plans["old"].Fingerprint != "untouched" || c.state.Version != 2 {
		t.Fatal("legacy receipts changed")
	}
	for name, app := range c.state.Applications {
		if app.SwarmID != "default" || app.Spec.Swarm != "" || app.RuntimeID != "old-"+name {
			t.Fatal("legacy app changed", app)
		}
	}
}
