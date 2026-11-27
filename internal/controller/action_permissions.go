package controller

import (
	"encoding/json"
	"net/http"
	"strings"

	"hostlier/internal/model"
)

// validActionLimits accepts exact capabilities with finite lifetime and use limits.
func validActionLimits(input authorizationRequest) bool {
	if len(input.Actions) == 0 || len(input.Actions) > 20 || len(input.Apps) > 20 || len(input.Secrets) > 20 || input.AppPrefix != "" || input.MaxApps != 0 || input.RouteDomain != "" || input.KeepApps || input.DurationSeconds < 60 || input.DurationSeconds > 28800 || input.MaxUses < 1 || input.MaxUses > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, action := range input.Actions {
		if !model.ValidPermission(action) || seen[action] {
			return false
		}
		seen[action] = true
		if action == model.SwarmsManage && len(input.Apps) != 0 {
			return false
		}
		if (strings.HasPrefix(action, "apps.") || strings.HasPrefix(action, "sidecars.")) && len(input.Apps) == 0 && input.Swarm == "" {
			return false
		}
		if strings.HasPrefix(action, "servers.") && (input.Host != "local" || input.Swarm != "") {
			return false
		}
	}
	return true
}

// deploymentActions records semantic changes in the immutable plan, including indirect removals.
func deploymentActions(action string, spec model.Project, prior *Application) []string {
	permission := model.AppsDeploy
	switch action {
	case "rollback":
		permission = model.AppsRollback
	case "scale":
		permission = model.AppsScale
	}
	actions := []string{permission}
	if model.DesiredReplicas(spec) == 0 {
		actions = append(actions, model.AppsStop)
	}
	if prior != nil {
		for name := range prior.Spec.Sidecars {
			if _, retained := spec.Sidecars[name]; !retained {
				actions = append(actions, model.SidecarsRemove)
				break
			}
		}
	}
	return actions
}

// hasActions requires one grant to cover the complete change without combining partial authority.
func hasActions(g *Authorization, required []string) bool {
	if len(required) == 0 {
		return false
	}
	for _, action := range required {
		if !containsName(g.Actions, action) {
			return false
		}
	}
	return true
}

// actionIntent matches known mutations only; future APIs remain denied until explicitly mapped.
func (c *Controller) actionIntent(a *HumanApproval, request humanRequest) (string, string, []string, bool) {
	host, path, remote := model.FleetPath(request.Path)
	if !remote {
		host, path = "local", request.Path
	}
	p := strings.Split(strings.Trim(path, "/"), "/")
	if request.Method == "POST" && len(p) == 4 && p[1] == "plans" && p[3] == "apply" {
		plan, _, ok := c.scopedPlan(a, request)
		if !ok {
			return host, "", nil, false
		}
		return host, plan.Spec.Name, plan.RequiredActions, true
	}
	if request.Method == "POST" && len(p) == 4 && p[1] == "apps" && p[3] == "action" {
		var body actionRequest
		if json.Unmarshal(request.Body, &body) != nil || !validAction(body) {
			return host, "", nil, false
		}
		action := "apps." + body.Action
		if body.Action == "delete" {
			action = model.AppsRemove
		}
		actions := []string{action}
		if body.Purge {
			actions = append(actions, model.AppsPurge)
		}
		return host, p[2], actions, true
	}
	if len(p) >= 2 && p[1] == "swarms" && (request.Method == "POST" || request.Method == "DELETE") {
		return host, "", []string{model.SwarmsManage}, true
	}
	if !remote {
		if request.Method == "POST" && (path == "/v1/hosts" || len(p) == 4 && p[1] == "fleet-joins" && p[3] == "approve") {
			return host, "", []string{model.ServersAdd}, true
		}
		if request.Method == "DELETE" && len(p) == 3 && p[1] == "hosts" {
			return host, "", []string{model.ServersRemove}, true
		}
	}
	return host, "", nil, false
}

// actionGrantAllows checks exact host, app, secret and capability boundaries for a retained request.
func (c *Controller) actionGrantAllows(g *Authorization, a *HumanApproval, request humanRequest) bool {
	host, app, actions, ok := c.actionIntent(a, request)
	if !ok || g.Host != host || !hasActions(g, actions) || !actionAppSwarm(g, a, app) {
		return false
	}
	if request.Target != nil && g.HostFingerprint != request.Target.Fingerprint {
		return false
	}
	_, inner, remote := model.FleetPath(request.Path)
	if !remote {
		inner = request.Path
	}
	if strings.HasPrefix(inner, "/v1/swarms") {
		return actionSwarmRequest(g, a, request)
	}
	if plan, _, ok := c.scopedPlan(a, request); ok {
		return grantAllowsPlan(g, plan, request)
	}
	return true
}

// applyActionAuthorization reserves one bounded use and retains the ordinary execution receipt.
func (c *Controller) applyActionAuthorization(a *HumanApproval, request humanRequest) bool {
	for _, g := range c.state.Authorizations {
		if g.Scope != "actions" || g.Device != a.Actor || !c.authorizationLive(g) || g.Uses >= g.MaxUses || !c.actionGrantAllows(g, a, request) {
			continue
		}
		g.Uses++
		c.approveWithGrant(a, g)
		c.log("Request authorized by action grant: "+g.ID, "", "info")
		return true
	}
	return false
}

// validateActionExecution rechecks the retained policy against the original encrypted intent.
func (c *Controller) validateActionExecution(a *HumanApproval, g *Authorization) error {
	data, err := c.store.Decrypt(a.Payload)
	if err != nil {
		return err
	}
	var request humanRequest
	if json.Unmarshal([]byte(data), &request) != nil || !c.actionGrantAllows(g, a, request) {
		return model.Err("SCOPE_DENIED", "Request no longer has the required action permissions")
	}
	return nil
}

// canAddServer restricts join disclosure and approval to an authorized signed-in device.
func (c *Controller) canAddServer(actor string) bool {
	for _, g := range c.state.Authorizations {
		if g.Device == actor && c.authorizationLive(g) && (g.Scope == "all" || g.Scope == "actions" && g.Host == "local" && containsName(g.Actions, model.ServersAdd) && g.Uses < g.MaxUses) {
			return true
		}
	}
	return false
}

// actionRouteAllowed admits bounded preparation and reads, never direct remote mutation.
func (c *Controller) actionRouteAllowed(r *http.Request, actor string) bool {
	for _, g := range c.state.Authorizations {
		if g.Scope == "actions" && g.Device == actor && c.authorizationLive(g) && c.actionGrantRoute(r, g) {
			return true
		}
	}
	return false
}

// actionGrantRoute keeps receipt ownership and exact app selection across local and fleet paths.
func (c *Controller) actionGrantRoute(r *http.Request, g *Authorization) bool {
	path := r.URL.Path
	if path == "/v1/approvals" && r.Method == "POST" || path == "/v1/authorizations/self" && r.Method == "GET" {
		return true
	}
	p := strings.Split(strings.Trim(path, "/"), "/")
	if len(p) >= 3 && p[1] == "approvals" {
		return c.scopedApprovalRoute(r, p, g)
	}
	host, inner, remote := model.FleetPath(path)
	if !remote {
		host, inner = "local", path
	}
	if r.Method == "GET" && g.Host == "local" && (containsName(g.Actions, model.ServersAdd) || containsName(g.Actions, model.ServersRemove)) {
		if !remote && (path == "/v1/hosts" || path == "/v1/hosts/identity") || remote && inner == "/v1/host-state" {
			return true
		}
	}
	if host != g.Host || remote && c.state.Hosts[host].Fingerprint != g.HostFingerprint {
		return false
	}
	p = strings.Split(strings.Trim(inner, "/"), "/")
	if len(p) >= 2 && p[1] == "swarms" {
		return c.swarmReadRoute(r, g, p, remote)
	}
	if inner == "/v1/plans" && r.Method == "POST" && g.Uses < g.MaxUses {
		return c.actionPlanInput(r, g, remote)
	}
	if r.Method == "GET" && inner == "/v1/packages/platform" && containsName(g.Actions, model.AppsDeploy) {
		return true
	}
	if len(p) < 3 {
		return false
	}
	if p[1] == "fleet-joins" && !remote {
		return r.Method == "GET" && len(p) == 3 && containsName(g.Actions, model.ServersAdd) && g.Uses < g.MaxUses
	}
	if p[1] == "apps" {
		return c.swarmAppReadRoute(r, p, g, remote)
	}
	if p[1] == "plans" {
		if remote {
			return r.Method == "GET" && len(p) == 3 && c.remotePlanRead(r, g, inner)
		}
		return c.scopedPlanRead(r, p, g)
	}
	if p[1] == "operations" {
		if remote {
			return r.Method == "GET" && (len(p) == 3 || len(p) == 4 && p[3] == "events") && c.actionRemoteReceipt(r, g, "/v1/operations/"+p[2])
		}
		return c.scopedOperationRoute(r, p, g)
	}
	return containsName(g.Actions, model.AppsDeploy) && scopedArtifactRoute(r, p)
}

// deployOnlyActions keeps legacy deployment windows from bypassing explicit lifecycle and removal permissions.
func deployOnlyActions(actions []string) bool {
	return len(actions) == 1 && actions[0] == model.AppsDeploy
}
