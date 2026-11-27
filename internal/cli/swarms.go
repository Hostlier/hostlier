package cli

import (
	"net/url"

	"hostlier/internal/model"
)

const swarmHelp = `Group K3s applications without changing their namespaces, routes or storage.

Usage:
  hostlier swarm list [--server PROFILE] [--on HOST]
  hostlier swarm create NAME [--server PROFILE] [--on HOST]
  hostlier swarm show NAME [--server PROFILE] [--on HOST]
  hostlier swarm members NAME [--server PROFILE] [--on HOST]
  hostlier swarm move APP --from SOURCE --to DESTINATION [--server PROFILE] [--on HOST]
  hostlier swarm delete NAME [--server PROFILE] [--on HOST]

Use swarm: NAME in .hostlier or deploy --swarm NAME. App names remain unique per server.
The default swarm is permanent; other swarms must be empty before deletion.
Grant actions with access authorize --scope actions --swarm NAME --actions PERMISSIONS.
Discovery uses swarms.view; grouping mutations use swarms.manage. Workload actions remain separate.
Creating swarms and moving apps between swarms require an unbound swarms.manage grant or all access.

Full guide: hostlier skills show swarm`

// swarmCommand uses the existing signed connection and retained approval protocol.
func (s *session) swarmCommand() error {
	for flag := range s.flags {
		switch flag {
		case "server", "on", "socket", "json", "toon", "from", "to", "key":
		default:
			return model.Err("ARGUMENTS", "Unsupported swarm option --"+flag)
		}
	}
	if s.sub != "move" && (s.flags["from"] != "" || s.flags["to"] != "") {
		return model.Err("ARGUMENTS", "--from and --to require swarm move")
	}
	if s.sub == "" || s.sub == "list" {
		if len(s.parsed.args) > 2 {
			return model.Err("ARGUMENTS", swarmHelp)
		}
		return s.getPrint("/v1/swarms")
	}
	if len(s.parsed.args) != 3 {
		return model.Err("ARGUMENTS", swarmHelp)
	}
	path := "/v1/swarms/" + url.PathEscape(s.target)
	switch s.sub {
	case "show":
		return s.getPrint(path)
	case "members":
		return s.getPrint(path + "/apps?includeDeleted=true")
	case "create":
		if !model.ValidSwarmName(s.target) {
			return model.Err("ARGUMENTS", "Invalid swarm name")
		}
		if err := s.approve("Create swarm " + s.target + "? [y/N] "); err != nil {
			return err
		}
		return s.swarmMutation("POST", "/v1/swarms", map[string]string{"name": s.target})
	case "delete":
		var group model.Swarm
		if err := s.request("GET", path, nil, &group); err != nil {
			return err
		}
		if group.ID == model.DefaultSwarm {
			return model.Err("SWARM_PERMANENT", "The default swarm cannot be deleted")
		}
		if err := s.approve("Delete empty swarm " + group.Name + "? [y/N] "); err != nil {
			return err
		}
		return s.swarmMutation("DELETE", "/v1/swarms/"+url.PathEscape(group.ID), map[string]any{})
	case "move":
		return s.moveSwarmApp()
	}
	return model.Err("ARGUMENTS", swarmHelp)
}

// swarmMutation prints only the acknowledged controller result.
func (s *session) swarmMutation(method, path string, body any) error {
	var result any
	if err := s.request(method, path, body, &result); err != nil {
		return err
	}
	return s.print(result)
}

// moveSwarmApp pins both reviewed identities before the membership-only mutation.
func (s *session) moveSwarmApp() error {
	if s.flags["from"] == "" || s.flags["to"] == "" {
		return model.Err("ARGUMENTS", swarmHelp)
	}
	var from, to model.Swarm
	if err := s.request("GET", "/v1/swarms/"+url.PathEscape(s.flags["from"]), nil, &from); err != nil {
		return err
	}
	if err := s.request("GET", "/v1/swarms/"+url.PathEscape(s.flags["to"]), nil, &to); err != nil {
		return err
	}
	if err := s.approve("Move " + s.target + " from " + from.Name + " to " + to.Name + " without changing its runtime? [y/N] "); err != nil {
		return err
	}
	return s.swarmMutation("POST", "/v1/swarms/"+url.PathEscape(to.ID)+"/members/"+url.PathEscape(s.target), map[string]string{"from": from.ID})
}

// selectProjectSwarm rejects conflicting selectors before source preparation or deployment.
func (s *session) selectProjectSwarm(p model.Project) (model.Project, error) {
	selector := s.flags["swarm"]
	if selector == "" {
		return p, nil
	}
	if !model.ValidSwarmName(selector) {
		return p, model.Err("ARGUMENTS", "Invalid --swarm name")
	}
	if p.Swarm != "" && p.Swarm != selector {
		return p, model.Err("ARGUMENTS", "--swarm conflicts with the project swarm")
	}
	p.Swarm = selector
	return p, nil
}

// validateSwarmSelectionFlags rejects selectors on commands that cannot honor them yet.
func validateSwarmSelectionFlags(args arguments) error {
	if _, present := args.flags["swarm"]; !present {
		return nil
	}
	if args.flags["swarm"] == "" || !model.ValidSwarmName(args.flags["swarm"]) {
		return model.Err("ARGUMENTS", "Invalid --swarm name")
	}
	if len(args.args) == 0 {
		return model.Err("ARGUMENTS", "--swarm requires a command")
	}
	switch args.args[0] {
	case "deploy", "update", "plan", "rollback", "list", "authorize":
		return nil
	}
	if args.args[0] == "pairing" && len(args.args) > 1 && args.args[1] == "code" {
		return nil
	}
	return model.Err("ARGUMENTS", "--swarm is supported by deploy, update, plan, rollback, app list, authorize and pairing code")
}
