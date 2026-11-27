package controller

import "hostlier/internal/model"

// initializeSwarms commits metadata migration before listeners or reconciliation start.
func (c *Controller) initializeSwarms() error {
	if c.state.SwarmVersion > 1 {
		return model.Err("INCOMPATIBLE_STATE", "Unsupported swarm metadata version")
	}
	if c.state.Swarms == nil {
		c.state.Swarms = map[string]model.Swarm{}
	}
	if _, ok := c.state.Swarms[model.DefaultSwarm]; !ok {
		c.state.Swarms[model.DefaultSwarm] = model.Swarm{ID: model.DefaultSwarm, Name: model.DefaultSwarm}
	}
	if len(c.state.Swarms) > 1 && c.state.Version != 3 {
		return model.Err("INCOMPATIBLE_STATE", "Named swarms require controller state version 3")
	}
	seen := map[string]bool{}
	for id, swarm := range c.state.Swarms {
		if id != swarm.ID || !model.ValidSwarmName(swarm.Name) || seen[swarm.Name] || (id == model.DefaultSwarm) != (swarm.Name == model.DefaultSwarm) {
			return model.Err("INCOMPATIBLE_STATE", "Invalid swarm identity index")
		}
		seen[swarm.Name] = true
	}
	for _, app := range c.state.Applications {
		if app.SwarmID == "" {
			app.SwarmID = model.DefaultSwarm
		}
		if _, ok := c.state.Swarms[app.SwarmID]; !ok {
			return model.Err("INCOMPATIBLE_STATE", "Invalid swarm application membership")
		}
	}
	c.state.SwarmVersion = 1
	return c.store.Save(c.state)
}

// swarm resolves an exact display name or immutable ID, never an implicit creation.
func (c *Controller) swarm(selector string) (model.Swarm, error) {
	selector = model.SwarmName(selector)
	if group, ok := c.state.Swarms[selector]; ok {
		return group, nil
	}
	for _, group := range c.state.Swarms {
		if group.Name == selector {
			return group, nil
		}
	}
	return model.Swarm{}, model.Err("SWARM_NOT_FOUND", "Swarm not found")
}

// appSwarmID leaves runtime names, namespaces and volume ownership untouched.
func (c *Controller) appSwarmID(name string) string {
	if app := c.state.Applications[name]; app != nil {
		return model.SwarmName(app.SwarmID)
	}
	return model.DefaultSwarm
}

// requireSwarmRuntime gates new grouping behavior without changing legacy runtime migration.
func (c *Controller) requireSwarmRuntime() error {
	if c.opts.Runtime.Name() != "k3s" {
		return model.Err("SWARM_RUNTIME", "Named swarms require K3s; complete the runtime migration first")
	}
	return nil
}

// planSwarm binds current membership before dependency or runtime preparation.
func (c *Controller) planSwarm(spec *model.Project, intent deploymentPlanRequest) (string, error) {
	if intent.Action == "rollback" {
		spec.Swarm = intent.Spec.Swarm
	}
	group, err := c.swarm(spec.Swarm)
	if err != nil {
		return "", err
	}
	if group.ID != model.DefaultSwarm {
		if err := c.requireSwarmRuntime(); err != nil {
			return "", err
		}
	}
	if c.state.Applications[spec.Name] != nil && c.appSwarmID(spec.Name) != group.ID {
		return "", model.Err("SWARM_MEMBERSHIP", "Application belongs to another swarm; use swarm move to change membership")
	}
	return group.ID, nil
}

// validatePlanSwarm catches deletion, recreation and moves after plan preparation.
func (c *Controller) validatePlanSwarm(plan *Plan) error {
	id := model.SwarmName(plan.SwarmID)
	if _, ok := c.state.Swarms[id]; !ok {
		return model.Err("STALE_PLAN", "Plan swarm no longer exists")
	}
	if c.state.Applications[plan.Spec.Name] != nil && c.appSwarmID(plan.Spec.Name) != id {
		return model.Err("STALE_PLAN", "Application swarm changed; prepare a new plan")
	}
	if id != model.DefaultSwarm {
		return c.requireSwarmRuntime()
	}
	return nil
}

// recordSwarmDeployment changes membership only after the accepted deployment is recorded.
func (c *Controller) recordSwarmDeployment(op *Operation) {
	c.state.Applications[op.Name].SwarmID = model.SwarmName(op.SwarmID)
}

// operationSwarmID retains failed-new-app receipts while fencing receipts after an app moves.
func (c *Controller) operationSwarmID(op *Operation) string {
	if c.state.Applications[op.Name] != nil {
		return c.appSwarmID(op.Name)
	}
	return model.SwarmName(op.SwarmID)
}
