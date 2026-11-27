package controller

import (
	"encoding/json"
	"net/http"
	"strings"

	"hostlier/internal/model"
)

// swarmAppReadRoute checks named-app reads against current membership on the selected controller.
func (c *Controller) swarmAppReadRoute(r *http.Request, p []string, g *Authorization, remote bool) bool {
	if r.Method != "GET" || !(len(p) == 3 || len(p) == 4 && (p[3] == "logs" || p[3] == "usage")) {
		return false
	}
	if !grantCoversApp(g, p[2]) && !(g.Scope == "actions" && (g.SwarmID != "" || containsName(g.Actions, model.SwarmsView) || containsName(g.Actions, model.SwarmsManage)) && len(g.Apps) == 0) {
		return false
	}
	id := c.appSwarmID(p[2])
	if remote {
		app, err := c.actionRemoteApp(r, g, p[2])
		if err != nil {
			return false
		}
		if app != nil {
			id = model.SwarmName(app.SwarmID)
		} else {
			id = model.DefaultSwarm
		}
	}
	return c.authorizationLive(g) && (grantSwarmApp(g, id, p[2]) || grantManagesSwarm(g, id))
}

// remotePlanRead admits only the selected device's plan within its current swarm boundary.
func (c *Controller) remotePlanRead(r *http.Request, g *Authorization, path string) bool {
	result, err := c.fleetRequest(r.Context(), c.state.Hosts[g.Host], "GET", path, nil, g.Device, nil)
	if err != nil {
		return false
	}
	data, _ := json.Marshal(result)
	var plan Plan
	if json.Unmarshal(data, &plan) != nil || !grantSwarmApp(g, plan.SwarmID, plan.Spec.Name) {
		return false
	}
	expected := "controller:" + strings.TrimPrefix(c.store.Authenticate("fleet-actor|"+g.Device), "hmac:")
	if plan.Actor != expected {
		return false
	}
	app, err := c.actionRemoteApp(r, g, plan.Spec.Name)
	return err == nil && (app == nil || grantSwarmMatches(g, app.SwarmID)) && c.authorizationLive(g)
}

// filterFleetSwarms applies master-side grants after a trusted member returns its group inventory.
func (c *Controller) filterFleetSwarms(actor string, host HostRecord, path string, result any) (any, error) {
	if c.unrestrictedSwarmActor(actor) {
		return result, nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	visible := func(id, name string) bool {
		for _, g := range c.state.Authorizations {
			if g.Device != actor || g.Host != host.Name || g.HostFingerprint != host.Fingerprint || !c.authorizationLive(g) {
				continue
			}
			if grantManagesSwarm(g, id) || grantSwarmMatches(g, id) && (name == "" || grantSwarmApp(g, id, name)) {
				return true
			}
		}
		return false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 2 {
		var envelope struct {
			Swarms []model.Swarm `json:"swarms"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, err
		}
		groups := []model.Swarm{}
		for _, g := range envelope.Swarms {
			if visible(g.ID, "") {
				groups = append(groups, g)
			}
		}
		return map[string]any{"swarms": groups}, nil
	}
	if len(parts) == 3 {
		var group model.Swarm
		if json.Unmarshal(data, &group) != nil || !visible(group.ID, "") {
			return nil, model.Err("SCOPE_DENIED", "Swarm is outside this login's permissions")
		}
		return group, nil
	}
	var envelope struct {
		Swarm        model.Swarm    `json:"swarm"`
		Applications []*Application `json:"applications"`
	}
	if json.Unmarshal(data, &envelope) != nil || !visible(envelope.Swarm.ID, "") {
		return nil, model.Err("SCOPE_DENIED", "Swarm is outside this login's permissions")
	}
	apps := []*Application{}
	for _, app := range envelope.Applications {
		if visible(envelope.Swarm.ID, app.Name) {
			apps = append(apps, app)
		}
	}
	return map[string]any{"swarm": envelope.Swarm, "applications": apps}, nil
}

// approvalSwarmVisible prevents old receipts from exposing an app after it leaves the grant's swarm.
func (c *Controller) approvalSwarmVisible(a *HumanApproval, g *Authorization) bool {
	if a.AuthorizationID == "" {
		return true
	}
	bound := c.state.Authorizations[a.AuthorizationID]
	if !c.authorizationLive(bound) || bound.Device != g.Device {
		return false
	}
	var review struct {
		Plan    Plan   `json:"plan"`
		SwarmID string `json:"swarmId"`
	}
	if json.Unmarshal(a.Review, &review) != nil {
		return false
	}
	_, path, remote := model.FleetPath(a.Path)
	if !remote {
		path = a.Path
	}
	p := strings.Split(strings.Trim(path, "/"), "/")
	app := ""
	if review.Plan.ID != "" {
		app = review.Plan.Spec.Name
	} else if len(p) >= 3 && p[1] == "apps" {
		app = p[2]
	}
	if app == "" {
		return true
	}
	if bound.Scope == "all" {
		return true
	}
	if remote {
		r, _ := http.NewRequestWithContext(c.ctx, "GET", a.Path, nil)
		prior, err := c.actionRemoteApp(r, bound, app)
		return err == nil && (prior == nil || grantSwarmMatches(bound, prior.SwarmID))
	}
	return c.state.Applications[app] == nil || grantSwarmMatches(bound, c.appSwarmID(app))
}
