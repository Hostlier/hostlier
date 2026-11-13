package cli

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"hostlier/internal/client"
	"hostlier/internal/config"
	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// serverInstall describes public, validated bootstrap inputs. The master credential stays on this client.
type serverInstall struct{ name, endpoint, domain, hostname string }

func (s *session) serverInstallTarget() (serverInstall, error) {
	var target serverInstall
	for flag := range s.flags {
		switch flag {
		case "name", "host", "domain", "release-dir", "ssh-config", "server", "socket", "key", "json", "toon":
		default:
			return target, model.Err("ARGUMENTS", "Unsupported box connection option --"+flag)
		}
	}
	if s.flags["server"] != "" && s.flags["socket"] != "" {
		return target, model.Err("ARGUMENTS", "Choose one master connection")
	}

	expected := 3
	if s.command == "connect" {
		expected = 2
	}
	if len(s.parsed.args) != expected || !sshDestination.MatchString(s.target) || len(s.target) > 255 {
		return target, model.Err("ARGUMENTS", "Usage: hostlier server add USER@HOST")
	}
	_, host, ok := strings.Cut(s.target, "@")
	if !ok {
		host = s.target
	}
	host = defaultValue(s.flags["host"], host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if !sshDestination.MatchString(host) && net.ParseIP(host) == nil || strings.ContainsAny(host, "@[]") {
		return target, model.Err("ARGUMENTS", "--host requires a hostname or IP address reachable from the master")
	}
	target.endpoint = "https://" + net.JoinHostPort(host, fleet.Port)
	name, _, _ := strings.Cut(strings.ToLower(host), ".")
	if net.ParseIP(host) != nil {
		name = strings.NewReplacer(".", "-", ":", "-").Replace(host)
	}
	target.name = defaultValue(s.flags["name"], name)
	if !fleet.ValidHostName(target.name) || target.name == "local" {
		return target, model.Err("ARGUMENTS", "Choose a fleet --name of up to 63 lowercase letters, digits or hyphens (except local and identity)")
	}
	var err error
	target.domain, err = config.NormalizeDomain(defaultValue(s.flags["domain"], host))
	if err != nil && s.flags["domain"] == "" && s.interactive {
		value, promptErr := s.ask("Application DNS domain for this box: ", false)
		if promptErr != nil {
			return target, promptErr
		}
		target.domain, err = config.NormalizeDomain(strings.TrimSpace(value))
	}
	if err != nil {
		return target, model.Err("ARGUMENTS", "Supply --domain with this box's application DNS domain")
	}
	target.hostname = "hostlier." + target.domain
	if strings.EqualFold(host, target.domain) {
		target.hostname = target.domain
	}
	return target, nil
}

func (s *session) addServer() error {
	target, err := s.serverInstallTarget()
	if err != nil {
		return err
	}
	if err = s.connectRemote(); err != nil {
		return err
	}
	hosts, err := s.enrolledServers()
	if err != nil {
		return err
	}
	if existing, ok := hosts[target.name]; ok {
		if existing.URL != target.endpoint {
			return model.Err("HOST_EXISTS", "Fleet name is already enrolled at another address. Choose a different --name")
		}
		return s.verifyAddedServer(existing)
	}
	master, err := s.masterPin()
	if err != nil {
		return err
	}
	dir := defaultValue(s.flags["release-dir"], filepath.Join(s.store.Home, "releases"))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.cwd, dir)
	}
	pin, err := s.installServerSSH(target, master, dir)
	if err != nil {
		return err
	}
	record := fleet.HostRecord{Name: target.name, URL: target.endpoint, Fingerprint: pin}
	fmt.Fprintf(s.errOut, "Controller installed. Requesting enrollment of %s at %s.\n", record.Name, record.URL)
	if err := s.enrollServer(hosts, record); err != nil {
		return fmt.Errorf("controller installed, master enrollment: %w", err)
	}
	return s.verifyAddedServer(record)
}

// verifyAddedServer checks the real master-to-box path, including both certificate pins.
func (s *session) verifyAddedServer(record fleet.HostRecord) error {
	if err := s.checkServer(record); err != nil {
		return err
	}
	if s.masterProfile != "" && s.flags["socket"] == "" {
		if err := s.store.SaveSSH(s.masterProfile, record.Name, client.SSHConnection{Destination: s.target, ConfigFile: s.sshConfigPath()}); err != nil {
			return err
		}
	}
	if s.command == "connect" && s.masterProfile != "" && s.flags["socket"] == "" {
		if err := s.store.UseHost(s.masterProfile, record.Name); err != nil {
			return fmt.Errorf("box connected, saving workload selection: %w", err)
		}
		fmt.Fprintf(s.errOut, "Selected %s/%s for workload commands.\n", s.masterProfile, record.Name)
	}
	return s.print(record)
}

func (s *session) checkServer(record fleet.HostRecord) error {
	var state struct {
		Generation *int64 `json:"generation"`
	}
	if err := s.request("GET", "/v1/hosts/"+record.Name+"/proxy/host-state", nil, &state); err != nil {
		return fmt.Errorf("%s is enrolled, but the master could not verify %s. Check reachability and the fleet pin: %w", record.Name, record.URL, err)
	}
	if state.Generation == nil {
		return model.Err("HOST_PROTOCOL", "Enrolled host returned an invalid fleet state")
	}
	return nil
}

func (s *session) enrolledServers() (map[string]fleet.HostRecord, error) {
	var result struct {
		Hosts map[string]fleet.HostRecord `json:"hosts"`
	}
	err := s.request("GET", "/v1/hosts", nil, &result)
	return result.Hosts, err
}

func (s *session) masterPin() (string, error) {
	var identity struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := s.request("GET", "/v1/hosts/identity", nil, &identity); err != nil {
		return "", err
	}
	if !fleet.ValidPin(identity.Fingerprint) {
		return "", model.Err("INVALID_HOST", "Master returned an invalid fleet fingerprint")
	}
	return identity.Fingerprint, nil
}

func (s *session) enrollServer(hosts map[string]fleet.HostRecord, record fleet.HostRecord) error {
	if prior, ok := hosts[record.Name]; ok {
		if prior != record {
			return model.Err("HOST_EXISTS", "Fleet name belongs to a different controller. Choose another name or revoke its enrollment first")
		}
		return nil
	}
	return s.request("POST", "/v1/hosts", record, nil)
}
