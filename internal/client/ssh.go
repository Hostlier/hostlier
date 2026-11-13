package client

import "hostlier/internal/model"

// SSHConnection retains public native SSH settings; private keys remain local files.
type SSHConnection struct {
	Destination    string `json:"destination"`
	ConfigFile     string `json:"configFile,omitempty"`
	IdentityFile   string `json:"identityFile,omitempty"`
	KnownHostsFile string `json:"knownHostsFile,omitempty"`
	Port           int    `json:"port,omitempty"`
	Managed        bool   `json:"managed,omitempty"`
}

// SaveSSH associates a previously reviewed connection with a named profile or box.
func (s Store) SaveSSH(profile, box string, connection SSHConnection) error {
	p, err := s.List()
	// Never replace corrupt metadata with an SSH-only profile.
	if err != nil {
		return err
	}
	// SSH metadata cannot create an unenrolled controller profile.
	if _, ok := p.Servers[profile]; !ok {
		return model.Err("PROFILE_UNKNOWN", "Unknown server profile")
	}
	// Initialize only the optional connection map, retaining enrollment and selection.
	if p.SSH == nil {
		p.SSH = map[string]SSHConnection{}
	}
	name := profile
	// Fleet destinations must remain distinct from their master.
	if box != "" {
		name += "/" + box
	}
	p.SSH[name] = connection
	// Clearing local metadata never deletes a private key or retained remote workspace.
	if connection.Destination == "" {
		delete(p.SSH, name)
	}
	return s.write("profiles.json", p)
}
