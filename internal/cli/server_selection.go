package cli

import (
	"strings"

	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

func (s *session) listServers() error {
	profiles, err := s.store.List()
	if err != nil {
		return err
	}
	if len(s.parsed.args) == 2 {
		return s.print(profiles)
	}
	if len(s.parsed.args) != 3 || s.flags["socket"] != "" || s.flags["server"] != "" {
		return model.Err("ARGUMENTS", "Usage: server list [MASTER]")
	}
	if _, ok := profiles.Servers[s.target]; !ok {
		return model.Err("PROFILE_UNKNOWN", "Unknown master profile")
	}
	s.flags["server"] = s.target
	if err = s.connectRemote(); err != nil {
		return err
	}
	return s.getPrint("/v1/hosts")
}

// A bare profile always selects that master. An unqualified box belongs to the current master.
func (s *session) useServer() error {
	if len(s.parsed.args) != 3 || s.flags["socket"] != "" || s.flags["server"] != "" {
		return model.Err("ARGUMENTS", "Usage: server use PROFILE | [PROFILE/]BOX")
	}
	profiles, err := s.store.List()
	if err != nil {
		return err
	}
	if _, ok := profiles.Servers[s.target]; ok {
		if err = s.store.Use(s.target); err != nil {
			return err
		}
		return s.print(map[string]string{"selected": s.target})
	}
	master, host, qualified := strings.Cut(s.target, "/")
	if !qualified {
		master, host = profiles.Current, s.target
	}
	if _, ok := profiles.Servers[master]; !ok || !fleet.ValidHostName(host) || host == "local" {
		return model.Err("PROFILE_UNKNOWN", "Choose a saved master profile or an enrolled box as MASTER/BOX")
	}
	s.flags["server"] = master
	if err = s.connectRemote(); err != nil {
		return err
	}
	hosts, err := s.enrolledServers()
	if err != nil {
		return err
	}
	if _, ok := hosts[host]; !ok {
		return model.Err("HOST_UNKNOWN", "Box is not enrolled with "+master+"; run hostlier server list "+master)
	}
	if err = s.store.UseHost(master, host); err != nil {
		return err
	}
	return s.print(map[string]string{"selected": master + "/" + host})
}
