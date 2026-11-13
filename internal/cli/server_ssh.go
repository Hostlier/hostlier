package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// SSHExitError preserves the native SSH or remote command status for shell scripts.
type SSHExitError struct{ Code int }

// Error reports status without echoing potentially sensitive remote command arguments.
func (e *SSHExitError) Error() string { return fmt.Sprintf("SSH exited with status %d", e.Code) }

// ExitCode is consumed by the executable's process boundary.
func (e *SSHExitError) ExitCode() int { return e.Code }

// serverSSH streams commands through the same OpenSSH transport used during setup.
func (s *session) serverSSH() error {
	// SSH settings are local; unrelated workload options must never be silently ignored.
	if err := s.validateServerSSH(); err != nil {
		return err
	}
	profile, box, connection, err := s.savedSSHConnection()
	// Resolve the exact selected destination before any connection or provisioning.
	if err != nil {
		return err
	}
	// Revocation uses the controller approval path, never remote sudo.
	if s.target == "revoke" {
		return s.revokeShell(profile, box, connection)
	}
	connection = s.overrideShellConnection(connection)
	// Reject ambiguous state or honor the explicit SSH selection.
	if s.target == "configure" {
		return s.configureShell(profile, box, connection)
	}
	// A missing setup connection is provisioned through the saved owner login.
	if s.target == "enable" || connection.Destination == "" {
		connection, err = s.enrollShell(profile, box)
		// Preserve failure before publishing or using SSH authority.
		if err != nil {
			return err
		}
		// Reject ambiguous state or honor the explicit SSH selection.
		if s.target == "enable" {
			return s.print(connection)
		}
	}
	// Preserve failure before publishing or using SSH authority.
	if err := validateSSHConnection(connection); err != nil {
		return err
	}
	args := s.shellSSHArguments(connection)
	command := exec.CommandContext(s.ctx, "ssh", args...)
	command.Stdin, command.Stdout, command.Stderr = s.in, s.out, s.errOut
	return sshProcessResult(s.ctx, command.Run())
}

// validateServerSSH keeps command arguments behind an explicit option boundary.
func (s *session) validateServerSSH() error {
	// Validate every configured value at this trust boundary.
	for flag := range s.flags {
		// The same SSH config and destination options work during setup and ordinary use.
		switch flag {
		// Keep this operation within the validated SSH contract.
		case "server", "ssh", "ssh-config", "tty", "json", "toon":
		// Keep this operation within the validated SSH contract.
		default:
			return model.Err("ARGUMENTS", "Unsupported SSH option --"+flag)
		}
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if len(s.parsed.args) > 3 || !map[string]bool{"": true, "configure": true, "enable": true, "revoke": true}[s.target] {
		return model.Err("ARGUMENTS", "Use server ssh [--server PROFILE] -- COMMAND ARG... or server ssh configure --ssh USER@HOST")
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if s.target != "" && (s.parsed.remoteArgs != nil || s.flags["tty"] != "") {
		return model.Err("ARGUMENTS", "SSH configuration cannot run a command or allocate a terminal")
	}
	return nil
}

// savedSSHConnection selects profile/box metadata without loading Hostlier credentials.
func (s *session) savedSSHConnection() (string, string, client.SSHConnection, error) {
	p, err := s.store.List()
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return "", "", client.SSHConnection{}, err
	}
	name, box := p.Current, p.CurrentHost
	// Reject ambiguous state or honor the explicit SSH selection.
	if selected := s.flags["server"]; selected != "" {
		name, box, _ = strings.Cut(selected, "/")
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if _, ok := p.Servers[name]; !ok {
		return "", "", client.SSHConnection{}, model.Err("PROFILE_UNKNOWN", "Select a saved server profile")
	}
	key := name
	// Reject ambiguous state or honor the explicit SSH selection.
	if box != "" {
		key += "/" + box
	}
	return name, box, p.SSH[key], nil
}

// validateSSHConnection prevents option injection and explains legacy profiles clearly.
func validateSSHConnection(c client.SSHConnection) error {
	// Reject ambiguous state or honor the explicit SSH selection.
	if c.Destination == "" {
		return model.Err("SSH_NOT_CONFIGURED", "No saved SSH setup connection; run hostlier server ssh configure --ssh USER@HOST")
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if len(c.Destination) > 255 || !sshDestination.MatchString(c.Destination) {
		return model.Err("ARGUMENTS", "SSH requires a hostname, SSH alias, or USER@HOST")
	}
	// Validate every configured value at this trust boundary.
	for _, path := range []string{c.ConfigFile, c.IdentityFile, c.KnownHostsFile} {
		// Reject ambiguous state or honor the explicit SSH selection.
		if strings.ContainsAny(path, "\x00\r\n") {
			return model.Err("ARGUMENTS", "Invalid SSH file path")
		}
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if c.Port < 0 || c.Port > 65535 {
		return model.Err("ARGUMENTS", "Invalid SSH port")
	}
	return nil
}

// shellSSHArguments preserves native host verification and quotes every remote argument.
func (s *session) shellSSHArguments(c client.SSHConnection) []string {
	args := nativeSSHArguments(c.ConfigFile, s.interactive, s.flags["tty"] != "" || (len(s.parsed.remoteArgs) == 0 && s.interactive))
	// Managed enrollment supplies an authenticated pin; never fall back to trusting an unknown key.
	if c.Managed {
		args[1] = "StrictHostKeyChecking=yes"
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if c.IdentityFile != "" {
		args = append(args, "-i", c.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if c.KnownHostsFile != "" {
		args = append(args, "-o", "UserKnownHostsFile="+c.KnownHostsFile, "-o", "GlobalKnownHostsFile=/dev/null")
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if c.Port != 0 {
		args = append(args, "-p", fmt.Sprint(c.Port))
	}
	args = append(args, "--", c.Destination)
	// Reject ambiguous state or honor the explicit SSH selection.
	if len(s.parsed.remoteArgs) > 0 {
		quoted := make([]string, len(s.parsed.remoteArgs))
		// Validate every configured value at this trust boundary.
		for i, arg := range s.parsed.remoteArgs {
			quoted[i] = quoteSSHArgument(arg)
		}
		args = append(args, strings.Join(quoted, " "))
	}
	return args
}

// sshProcessResult distinguishes connection/command failure from cancellation.
func sshProcessResult(ctx context.Context, err error) error {
	// Reject ambiguous state or honor the explicit SSH selection.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var status *exec.ExitError
	// Preserve failure before publishing or using SSH authority.
	if errors.As(err, &status) {
		return &SSHExitError{Code: status.ExitCode()}
	}
	return err
}

// configureShell saves explicit transport metadata without installing credentials or changing a server.
func (s *session) configureShell(profile, box string, connection client.SSHConnection) error {
	// Preserve failure before publishing or using SSH authority.
	if err := validateSSHConnection(connection); err != nil {
		return err
	}
	// Preserve failure before publishing or using SSH authority.
	if err := s.store.SaveSSH(profile, box, connection); err != nil {
		return err
	}
	return s.print(map[string]any{"configured": true, "server": profile, "box": box, "ssh": connection})
}

// sshConfigPath preserves relative setup configuration after the CLI changes working directory.
func (s *session) sshConfigPath() string {
	path := s.flags["ssh-config"]
	// Reject ambiguous state or honor the explicit SSH selection.
	if path != "" && !filepath.IsAbs(path) {
		return filepath.Join(s.cwd, path)
	}
	return path
}

// overrideShellConnection applies explicit native transport options without modifying saved state.
func (s *session) overrideShellConnection(connection client.SSHConnection) client.SSHConnection {
	// Explicit overrides use the existing setup option names.
	if s.flags["ssh"] != "" {
		connection = client.SSHConnection{Destination: s.flags["ssh"]}
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if s.flags["ssh-config"] != "" {
		connection.ConfigFile = s.sshConfigPath()
	}
	return connection
}
