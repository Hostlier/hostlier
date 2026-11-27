package controller

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"hostlier/internal/model"
)

// authorizationSwarm resolves a reviewed selector once; grants store its immutable identity.
func (c *Controller) authorizationSwarm(host, selector, actor string) (string, error) {
	if selector == "" {
		return "", nil
	}
	if host == "local" {
		group, err := c.swarm(selector)
		return group.ID, err
	}
	group, err := c.remoteSwarm(host, selector, actor)
	return group.ID, err
}

// remoteSwarm requires the target controller's grouping API, never a name-only fallback.
func (c *Controller) remoteSwarm(host, selector, actor string) (model.Swarm, error) {
	peer, ok := c.state.Hosts[host]
	if !ok {
		return model.Swarm{}, model.Err("NOT_FOUND", "Enrolled host not found")
	}
	result, err := c.fleetRequest(c.ctx, peer, "GET", "/v1/swarms/"+url.PathEscape(selector), nil, actor, nil)
	if err != nil {
		return model.Swarm{}, err
	}
	data, _ := json.Marshal(result)
	var group model.Swarm
	if json.Unmarshal(data, &group) != nil || group.ID == "" || !model.ValidSwarmName(group.Name) || c.state.Hosts[host].Fingerprint != peer.Fingerprint {
		return group, model.Err("HOST_PROTOCOL", "Host swarm identity is unavailable or changed; upgrade the host if needed")
	}
	return group, nil
}

// grantProjectSwarm verifies both the requested group and any existing application's membership.
func (c *Controller) grantProjectSwarm(g *Authorization, p model.Project, remote bool) bool {
	if !containsName(g.Apps, p.Name) && !(g.Scope == "actions" && g.SwarmID != "" && len(g.Apps) == 0) {
		return false
	}
	id := model.DefaultSwarm
	if model.SwarmName(p.Swarm) != model.DefaultSwarm {
		var group model.Swarm
		var err error
		if remote {
			group, err = c.remoteSwarm(g.Host, p.Swarm, g.Device)
		} else {
			group, err = c.swarm(p.Swarm)
		}
		if err != nil {
			return false
		}
		id = group.ID
	}
	if !grantSwarmMatches(g, id) {
		return false
	}
	return remote || c.state.Applications[p.Name] == nil || c.appSwarmID(p.Name) == id
}

// actionSwarmRequest checks group administration against the server-retained review.
func actionSwarmRequest(g *Authorization, a *HumanApproval, request humanRequest) bool {
	_, path, remote := model.FleetPath(request.Path)
	if !remote {
		path = request.Path
	}
	p := strings.Split(strings.Trim(path, "/"), "/")
	if len(p) < 2 || p[1] != "swarms" {
		return false
	}
	if request.Method == "POST" && len(p) == 2 {
		return g.SwarmID == "" && containsName(g.Actions, model.SwarmsManage)
	}
	var review struct {
		Swarm model.Swarm `json:"swarm"`
	}
	if json.Unmarshal(a.Review, &review) != nil || review.Swarm.ID == "" || !grantManagesSwarm(g, review.Swarm.ID) {
		return false
	}
	if request.Method == "DELETE" && len(p) == 3 {
		return true
	}
	if request.Method == "POST" && len(p) == 5 && p[3] == "members" {
		var body struct {
			From string `json:"from"`
		}
		return json.Unmarshal(request.Body, &body) == nil && grantManagesSwarm(g, body.From)
	}
	return false
}

// actionAppSwarm binds lifecycle permission to the app group observed during review.
func actionAppSwarm(g *Authorization, a *HumanApproval, app string) bool {
	if app == "" {
		return true
	}
	var review struct {
		SwarmID string `json:"swarmId"`
		Plan    Plan   `json:"plan"`
	}
	if json.Unmarshal(a.Review, &review) != nil {
		return false
	}
	id := review.SwarmID
	if review.Plan.ID != "" {
		id = review.Plan.SwarmID
	}
	return grantSwarmApp(g, id, app)
}

// swarmReadRoute admits discovery only; the API or fleet response filter narrows collection contents.
func (c *Controller) swarmReadRoute(r *http.Request, g *Authorization, p []string, remote bool) bool {
	if r.Method != "GET" || len(p) > 4 {
		return false
	}
	if len(p) == 2 {
		return true
	}
	var group model.Swarm
	var err error
	if remote {
		group, err = c.remoteSwarm(g.Host, p[2], g.Device)
	} else {
		group, err = c.swarm(p[2])
	}
	if err != nil || (!grantManagesSwarm(g, group.ID) && !grantSwarmMatches(g, group.ID)) {
		return false
	}
	return len(p) == 3 || len(p) == 4 && p[3] == "apps"
}
