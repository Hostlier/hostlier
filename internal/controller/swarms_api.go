package controller

import (
	"net/http"
	"sort"

	"hostlier/internal/model"
)

// apiSwarms exposes grouping through the same authenticated, retained-approval dispatcher as apps.
func (c *Controller) apiSwarms(r *http.Request, p []string, actor string) (any, error) {
	if len(p) == 2 {
		switch r.Method {
		case "GET":
			groups := []model.Swarm{}
			for _, group := range c.state.Swarms {
				if c.swarmVisible(actor, group.ID) {
					groups = append(groups, group)
				}
			}
			sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
			return map[string]any{"swarms": groups}, nil
		case "POST":
			return c.createSwarm(r)
		}
		return endpointNotFound()
	}
	group, err := c.swarm(p[2])
	if err != nil {
		return nil, err
	}
	if r.Method == "GET" && !c.swarmVisible(actor, group.ID) {
		return nil, model.Err("SCOPE_DENIED", "Swarm is outside this login's permissions")
	}
	if len(p) == 3 {
		switch r.Method {
		case "GET":
			return group, nil
		case "DELETE":
			return c.deleteSwarm(r, group)
		}
	}
	if len(p) == 4 && p[3] == "apps" && r.Method == "GET" {
		apps := []*Application{}
		for name, app := range c.state.Applications {
			if app.Status == "deleted" && r.URL.Query().Get("includeDeleted") != "true" {
				continue
			}
			if c.appSwarmID(name) == group.ID && c.swarmAppVisible(actor, group.ID, name) {
				apps = append(apps, app)
			}
		}
		sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
		return map[string]any{"swarm": group, "applications": apps}, nil
	}
	if len(p) == 5 && p[3] == "members" && r.Method == "POST" {
		return c.moveSwarmApp(r, group, p[4])
	}
	return endpointNotFound()
}

// createSwarm allocates an immutable identity and fences binaries that cannot enforce grouping.
func (c *Controller) createSwarm(r *http.Request) (any, error) {
	if err := c.requireSwarmRuntime(); err != nil {
		return nil, err
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	if !model.ValidSwarmName(input.Name) {
		return nil, model.Err("INVALID_SWARM", "Choose a lowercase swarm name with letters, digits and hyphens")
	}
	if _, err := c.swarm(input.Name); err == nil {
		return nil, model.Err("SWARM_EXISTS", "Swarm already exists")
	}
	if len(c.state.Swarms) >= 1000 {
		return nil, model.Err("SWARM_LIMIT", "Swarm limit reached")
	}
	group := model.Swarm{ID: "sw_" + random()[:32], Name: input.Name}
	version := c.state.Version
	c.state.Swarms[group.ID] = group
	c.state.Version = 3
	c.state.Generation++
	if err := c.store.Save(c.state); err != nil {
		delete(c.state.Swarms, group.ID)
		c.state.Version = version
		c.state.Generation--
		return nil, err
	}
	c.log("Swarm created: "+group.Name, "", "info")
	return group, nil
}

// deleteSwarm requires an empty group, including retained deleted apps and interrupted work.
func (c *Controller) deleteSwarm(r *http.Request, group model.Swarm) (any, error) {
	if err := emptyBody(r); err != nil {
		return nil, err
	}
	if err := c.requireSwarmRuntime(); err != nil {
		return nil, err
	}
	if group.ID == model.DefaultSwarm {
		return nil, model.Err("SWARM_PERMANENT", "The default swarm cannot be deleted")
	}
	for name := range c.state.Applications {
		if c.appSwarmID(name) == group.ID {
			return nil, model.Err("SWARM_NOT_EMPTY", "Move or purge all retained applications before deleting the swarm")
		}
	}
	for _, op := range c.state.Operations {
		if model.SwarmName(op.SwarmID) == group.ID && (op.Status == "queued" || op.Status == "running" || op.Status == "interrupted") {
			return nil, model.Err("SWARM_BUSY", "Swarm has accepted or interrupted work")
		}
	}
	delete(c.state.Swarms, group.ID)
	c.state.Generation++
	if err := c.store.Save(c.state); err != nil {
		c.state.Swarms[group.ID] = group
		c.state.Generation--
		return nil, err
	}
	c.log("Swarm deleted: "+group.Name, "", "info")
	return map[string]bool{"deleted": true}, nil
}

// moveSwarmApp changes only metadata after both source and destination were reviewed.
func (c *Controller) moveSwarmApp(r *http.Request, to model.Swarm, name string) (any, error) {
	if err := c.requireSwarmRuntime(); err != nil {
		return nil, err
	}
	var input struct {
		From string `json:"from"`
	}
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	app := c.state.Applications[name]
	if app == nil {
		return endpointNotFound()
	}
	if app.Spec.Kind == "memory" {
		return nil, model.Err("SWARM_RUNTIME", "Named swarms currently require a K3s application")
	}
	from := c.appSwarmID(name)
	if input.From != from {
		return nil, model.Err("STALE_PLAN", "Application membership changed; review the move again")
	}
	if c.swarmAppBusy(name) {
		return nil, model.Err("APP_BUSY", "Resolve accepted or interrupted application work before changing its swarm")
	}
	priorSpec := app.Spec.Swarm
	app.SwarmID = to.ID
	app.Spec.Swarm = to.Name
	c.state.Generation++
	if err := c.store.Save(c.state); err != nil {
		app.SwarmID = from
		app.Spec.Swarm = priorSpec
		c.state.Generation--
		return nil, err
	}
	c.log("Application "+name+" moved to swarm "+to.Name, "", "info")
	return map[string]any{"application": name, "swarm": to, "from": from}, nil
}

// swarmAppBusy fences interrupted work as well as actively running work during a metadata move.
func (c *Controller) swarmAppBusy(name string) bool {
	for _, op := range c.state.Operations {
		if op.Name == name && (op.Status == "queued" || op.Status == "running" || op.Status == "interrupted") {
			return true
		}
	}
	return false
}
