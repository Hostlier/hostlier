package cli

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/config"
	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

func (s *session) joinMaster() error {
	if len(s.parsed.args) != 3 {
		return model.Err("ARGUMENTS", "Usage: hostlier server join MASTER_URL_OR_PROFILE")
	}
	for flag := range s.flags {
		switch flag {
		case "name", "host", "json", "toon", "key":
		default:
			return model.Err("ARGUMENTS", "Unsupported join option --"+flag)
		}
	}
	local, err := client.New(client.Options{SocketPath: "/run/hostlier/controller.sock"})
	if err != nil {
		return err
	}
	return s.joinInstalledBox(local)
}

func (s *session) joinInstalledBox(local Requester) error {
	var box fleet.Member
	if err := local.Request(s.ctx, "GET", "/v1/fleet/master", nil, &box); err != nil {
		return fmt.Errorf("read installed controller (run on the box with local administrator access): %w", err)
	}
	if !fleet.ValidPin(box.Fingerprint) {
		return model.Err("INVALID_HOST", "Local controller returned an invalid identity")
	}
	host := defaultValue(s.flags["host"], box.Hostname)
	if _, err := config.NormalizeDomain(host); err != nil && net.ParseIP(host) == nil {
		return model.Err("INVALID_HOST", "Supply --host with the box address reachable from the master")
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(host, "hostlier."), ".")
	name = defaultValue(s.flags["name"], strings.ToLower(name))
	if !fleet.ValidHostName(name) || name == "local" {
		return model.Err("INVALID_HOST", "Choose a valid fleet --name for this box")
	}
	if err := s.joinConnection(name); err != nil {
		return err
	}
	master, err := s.masterPin()
	if err != nil {
		return err
	}
	if master == box.Fingerprint {
		return model.Err("INVALID_HOST", "A controller cannot join itself")
	}
	hosts, err := s.enrolledServers()
	if err != nil {
		return err
	}
	record := fleet.HostRecord{Name: name, URL: "https://" + net.JoinHostPort(host, fleet.Port), Fingerprint: box.Fingerprint}
	// Keep the name and master-side address chosen during an earlier enrollment.
	if s.flags["name"] == "" {
		matched := false
		for _, prior := range hosts {
			if prior.Fingerprint != box.Fingerprint || (s.flags["host"] != "" && prior.URL != record.URL) {
				continue
			}
			if matched {
				return model.Err("ARGUMENTS", "This box has several fleet names on the master. Choose one with --name")
			}
			matched = true
			record = prior
		}
	}
	if err = s.enrollServer(hosts, record); err != nil {
		return err
	}
	change := fleet.MasterChange{Previous: box.Master, Fingerprint: master}
	if err = local.Request(s.ctx, "PUT", "/v1/fleet/master", change, nil); err != nil {
		return fmt.Errorf("box enrolled, setting its master: %w", err)
	}
	if err = s.checkServer(record); err != nil {
		if master == box.Master {
			return err
		}
		// Even when the operator cancels the probe, restore trust only if this switch still owns it.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
		defer cancel()
		restore := fleet.MasterChange{Previous: master, Fingerprint: box.Master}
		if restoreErr := local.Request(ctx, "PUT", "/v1/fleet/master", restore, nil); restoreErr != nil {
			return fmt.Errorf("%w; restoring previous master: %v", err, restoreErr)
		}
		return fmt.Errorf("%w; previous master selection restored", err)
	}
	return s.print(record)
}

// A saved profile uses its existing credential. A fresh URL uses the usual browser approval
// with a credential held only for this invocation.
func (s *session) joinConnection(name string) error {
	if !strings.Contains(s.target, "://") {
		s.flags["server"] = s.target
		return s.connectRemote()
	}
	origin, err := client.ControllerURL(s.target)
	if err != nil {
		return err
	}
	profiles, err := s.store.List()
	if err != nil {
		return err
	}
	profile, err := savedProfileForOrigin(profiles, origin)
	if err != nil {
		return err
	}
	if profile != "" {
		s.flags["server"] = profile
		return s.connectRemote()
	}
	s.serverTarget = origin
	remote, err := s.enrollmentRemote(origin)
	if err != nil {
		return err
	}
	credential, err := s.enrollBrowserCLI(remote, "fleet-"+name)
	if err != nil {
		return err
	}
	s.remote, err = client.New(client.Options{URL: origin, Token: credential.Token})
	return err
}
