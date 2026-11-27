package controller

import (
	"strings"

	"hostlier/internal/model"
)

// unrestrictedSwarmActor preserves local, delegated-controller and existing owner read access.
func (c *Controller) unrestrictedSwarmActor(actor string) bool {
	if actor == "local" || strings.HasPrefix(actor, "controller:") {
		return true
	}
	device := c.state.Clients[actor]
	if device == nil {
		return false
	}
	if device.DeploymentGrant == "" {
		return true
	}
	g := c.state.Authorizations[device.DeploymentGrant]
	return c.authorizationLive(g) && g.Scope == "all"
}

// grantSwarmMatches interprets legacy app grants solely within default.
func grantSwarmMatches(g *Authorization, id string) bool {
	return model.SwarmName(g.SwarmID) == model.SwarmName(id)
}

// grantSwarmApp requires exact app selection unless a whole swarm was explicitly granted.
func grantSwarmApp(g *Authorization, id, name string) bool {
	return grantSwarmMatches(g, id) && (grantCoversApp(g, name) || g.Scope == "actions" && (g.SwarmID != "" || containsName(g.Actions, model.SwarmsView)) && len(g.Apps) == 0)
}

// grantManagesSwarm keeps global administration explicit and a bound grant inside its immutable group.
func grantManagesSwarm(g *Authorization, id string) bool {
	return g.Scope == "actions" && containsName(g.Actions, model.SwarmsManage) && (g.SwarmID == "" || g.SwarmID == id)
}

// swarmVisible reveals only groups covered by a live local grant.
func (c *Controller) swarmVisible(actor, id string) bool {
	if c.unrestrictedSwarmActor(actor) {
		return true
	}
	for _, g := range c.state.Authorizations {
		if g.Device != actor || g.Host != "local" || !c.authorizationLive(g) {
			continue
		}
		if grantManagesSwarm(g, id) || grantSwarmMatches(g, id) && (len(g.Apps) > 0 || containsName(g.Actions, model.SwarmsView) || g.SwarmID != "") {
			return true
		}
	}
	return false
}

// swarmAppVisible prevents a group listing from widening an exact-app grant.
func (c *Controller) swarmAppVisible(actor, id, name string) bool {
	if c.unrestrictedSwarmActor(actor) {
		return true
	}
	for _, g := range c.state.Authorizations {
		if g.Device != actor || g.Host != "local" || !c.authorizationLive(g) {
			continue
		}
		if grantManagesSwarm(g, id) || grantSwarmApp(g, id, name) {
			return true
		}
	}
	return false
}
