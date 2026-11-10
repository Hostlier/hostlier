package cli

import (
	"errors"
	"net/url"
	"strings"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// connectRemote selects an explicit socket or saved remote profile before authenticated commands.
func (s *session) connectRemote() error {
	s.remote = s.options.Client
	s.serverTarget = s.flags["server"]
	var settings client.Options
	if s.flags["socket"] != "" {
		settings.SocketPath = s.flags["socket"]
	} else {
		profiles, err := s.store.List()
		if err != nil {
			return err
		}
		name := defaultValue(s.flags["server"], profiles.Current)
		s.masterProfile = name
		if s.flags["server"] == "" && s.flags["on"] == "" {
			s.flags["on"] = profiles.CurrentHost
		}
		if s.localAdminFallback(profiles.Current) {
			settings.SocketPath = "/run/hostlier/controller.sock"
		} else if s.remote == nil {
			settings, err = s.store.Get(name)
			if err != nil {
				return err
			}
		}
	}
	if s.remote == nil {
		c, e := client.NewGRPC(settings)
		// Require a verified controller transport before sending credentials or commands.
		if e != nil {
			return e
		}
		s.remote = c
		s.serverTarget = c.Target
	}
	return nil
}

// request retries safe reads and writes carrying durable request identities after connection failure.
func (s *session) request(method, path string, body, result any) error {
	// An explicit target always routes workload APIs through the central controller.
	route, _, _ := strings.Cut(path, "?")
	if s.flags["on"] != "" && s.flags["on"] != "local" && model.FleetRoute(route) {
		path = "/v1/hosts/" + url.PathEscape(s.flags["on"]) + "/proxy/" + path[len("/v1/"):]
	}
	// Remote mutations always use retained approval; an explicit protected Unix socket is server administration.
	if s.needsRemoteApproval(method, path) {
		return s.requestHumanApproval(method, path, body, result)
	}
	e := s.remote.Request(s.ctx, method, path, body, result)
	var failure *model.Error
	// Retry only transport failures, never semantic API rejections.
	if errors.As(e, &failure) && failure.Code == "CONNECTION" {
		retry := method == "GET"
		// A durable request identity makes replay safe after an ambiguous connection failure.
		if object, ok := body.(map[string]any); ok && object["requestId"] != nil {
			retry = true
		}
		// Repeat the same request body so its idempotency identity is preserved.
		if retry {
			return s.remote.Request(s.ctx, method, path, body, result)
		}
	}
	return e
}

// getPrint requests and prints an authenticated API response.
func (s *session) getPrint(path string) error {
	var result any
	// Require the requested controller data before displaying or acting on it.
	if err := s.request("GET", path, nil, &result); err != nil {
		return err
	}
	return s.print(result)
}

// list prints the controller application inventory.
func (s *session) list() error {
	if s.flags["all"] != "" {
		return s.getPrint("/v1/apps?includeDeleted=true")
	}
	return s.getPrint("/v1/apps")
}

// doctor prints controller runtime diagnostics.
func (s *session) doctor() error { return s.getPrint("/v1/doctor") }

// status selects controller-wide or named application status.
func (s *session) status() error {
	path := "/v1/status"
	// Use application status when a name was supplied.
	if s.sub != "" {
		path = "/v1/apps/" + url.PathEscape(s.sub)
	}
	return s.getPrint(path)
}

// controllerCommand dispatches controller administration reads.
func (s *session) controllerCommand() error {
	// Select the requested controller administration read.
	switch s.sub {
	// Maintenance uses the same saved native gRPC connection as ordinary commands.
	case "update", "update-status", "migrate":
		return s.controllerMaintenanceCommand()
	// Print controller health and operation inventory.
	case "status":
		return s.getPrint("/v1/status")
	// Follow bounded controller execution events.
	case "logs":
		return s.logs()
	}
	return model.Err("UNSUPPORTED_COMMAND", "Unsupported command; run hostlier help for supported commands")
}

// localAdminFallback allows local administration when no explicit or selected remote profile exists.
func (s *session) localAdminFallback(current string) bool {
	return s.flags["server"] == "" && current == "" && (s.command == "pairing" || s.command == "controller" || s.command == "clients" || s.command == "security")
}

// needsRemoteApproval preserves retained approval for every nonadministrative transport.
func (s *session) needsRemoteApproval(method, path string) bool {
	return s.flags["socket"] == "" && model.RequiresHumanApproval(method, path)
}
