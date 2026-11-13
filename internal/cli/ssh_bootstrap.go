package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"hostlier/internal/client"
	"hostlier/internal/model"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var sshDestination = regexp.MustCompile(`^(?:[A-Za-z0-9_][A-Za-z0-9_.-]*@)?(?:[A-Za-z0-9_][A-Za-z0-9_.-]*|\[[0-9A-Fa-f:]+\])$`)

// validateSSHBootstrap rejects transport ambiguity before contacting either endpoint.
func (s *session) validateSSHBootstrap(enrollment client.Enrollment) error {
	destination := s.flags["ssh"]
	// SSH options never change how ordinary HTTPS commands authenticate.
	if destination == "" {
		// Reject dangling modifiers rather than silently ignoring administrator intent.
		if s.flags["ssh-config"] != "" || s.flags["ssh-socket"] != "" || s.flags["ssh-sudo"] != "" {
			return model.Err("ARGUMENTS", "SSH setup options require --ssh USER@HOST")
		}
		return nil
	}
	// Bootstrap prepares browser owner approval, not an alternative credential-granting method.
	if enrollment.Method != "browser" || !s.interactive {
		return model.Err("ARGUMENTS", "--ssh requires interactive browser authentication")
	}
	// Prevent SSH option injection and ambiguous shell destinations.
	if !sshDestination.MatchString(destination) || len(destination) > 255 {
		return model.Err("ARGUMENTS", "--ssh requires a hostname, SSH alias, or USER@HOST")
	}
	socket := s.flags["ssh-socket"]
	// The remote socket is a filesystem path, never a URL or command.
	if !validBootstrapSocket(socket) {
		return model.Err("ARGUMENTS", "--ssh-socket requires an absolute remote socket path")
	}
	return nil
}

// setupOwnerSSH obtains a bounded enrollment link from the standalone controller executable.
func (s *session) setupOwnerSSH(requestID string) (string, error) {
	fmt.Fprintf(s.errOut, "Preparing owner access through SSH to %s…\n", s.flags["ssh"])
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	defer cancel()
	run := s.options.RunSSH
	// Tests and embedding callers can replace process execution without bypassing response validation.
	if run == nil {
		run = s.runBootstrapSSH
	}
	data, err := run(ctx, s.bootstrapSSHArguments(requestID))
	// Never echo untrusted stdout, which may contain the one-time enrollment secret.
	if err != nil {
		return "", err
	}
	var result model.OwnerSetupResult
	// Bound output even for injected runners and reject banners or malformed protocol responses.
	if len(data) > 65536 || json.Unmarshal(data, &result) != nil {
		return "", model.Err("SSH_BOOTSTRAP", "Controller returned an invalid bootstrap response")
	}
	return s.ownerSetupURL(result, requestID)
}

// bootstrapSSHArguments retains SSH host verification and quotes the remote shell command separately.
func (s *session) bootstrapSSHArguments(requestID string) []string {
	args := s.sshArguments()
	command := "hostlier-controller bootstrap --url " + quoteSSHArgument(s.serverTarget) + " --request " + quoteSSHArgument(requestID)
	// Nonstandard controller installations may explicitly select their remote administration socket.
	if s.flags["ssh-socket"] != "" {
		command += " --socket " + quoteSSHArgument(s.flags["ssh-socket"])
	}
	// Sudo is explicit and noninteractive; a remote password must never enter this protocol stream.
	if s.flags["ssh-sudo"] != "" {
		command = "sudo -n -- " + command
	}
	return append(args, "--", s.flags["ssh"], command)
}

// sshArguments applies the native host-key and forwarding policy to either bootstrap flow.
func (s *session) sshArguments() []string {
	return nativeSSHArguments(s.flags["ssh-config"], true, false)
}

// nativeSSHArguments shares host verification and forwarding policy across setup and shells.
func nativeSSHArguments(config string, interactive, tty bool) []string {
	batch, requestTTY := "yes", "no"
	// Interactive terminals may prompt for an identity or new host-key confirmation.
	if interactive {
		batch = "no"
	}
	// Explicit terminal requests support interactive shells and commands such as sudo.
	if tty {
		requestTTY = "force"
	}
	args := []string{"-o", "StrictHostKeyChecking=ask", "-o", "BatchMode=" + batch, "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ClearAllForwardings=yes", "-o", "RequestTTY=" + requestTTY, "-o", "ConnectTimeout=15", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "RemoteCommand=none"}
	// Native configuration retains identities, ports and jump-host routing.
	if config != "" {
		args = append(args, "-F", config)
	}
	return args
}

// quoteSSHArgument protects arguments from the remote shell used by OpenSSH's command protocol.
func quoteSSHArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// ownerSetupURL accepts only a fresh link for the exact requested controller and pending device.
func (s *session) ownerSetupURL(result model.OwnerSetupResult, requestID string) (string, error) {
	// Existing owners use ordinary approval without generating additional enrollment links.
	if result.Status == "ready" {
		return s.serverTarget + "/connect?request=" + url.QueryEscape(requestID), nil
	}
	u, err := url.Parse(result.URL)
	// Validate origin and path before exposing the bootstrap secret to a browser.
	if err != nil || result.Status != "enroll" || result.ExpiresAt <= time.Now().UnixMilli() {
		return "", model.Err("SSH_BOOTSTRAP", "Controller returned an invalid or expired owner enrollment link")
	}
	// A valid SSH identity cannot redirect browser enrollment to an unrelated HTTPS origin.
	if u.Scheme+"://"+u.Host != s.serverTarget || u.User != nil || u.Path != "/security" || u.RawQuery != "" {
		return "", model.Err("CONTROLLER_MISMATCH", "Enrollment link does not match the requested HTTPS controller")
	}
	// Only the bootstrap secret and exact pending request may influence browser navigation.
	if !validOwnerLinkFragment(u.Fragment, requestID) {
		return "", model.Err("SSH_BOOTSTRAP", "Owner enrollment link does not match this CLI request")
	}

	return result.URL, nil
}

// validBootstrapSocket admits only bounded absolute remote filesystem paths or the default.
func validBootstrapSocket(socket string) bool {
	return socket == "" || filepath.IsAbs(socket) && len(socket) <= 4096 && !strings.ContainsAny(socket, "\x00\r\n")
}

// validOwnerLinkFragment binds one enrollment secret to exactly the initiating device request.
func validOwnerLinkFragment(fragment string, requestID string) bool {
	values, err := url.ParseQuery(fragment)
	return err == nil && len(values) == 2 && len(values["enroll"]) == 1 && values.Get("enroll") != "" && len(values["request"]) == 1 && values.Get("request") == requestID
}

// prepareOwnerSetup prompts only when this controller explicitly reports no enrolled owner.
func (s *session) prepareOwnerSetup(id, address string, pollSupported, required bool) (string, error) {
	// Ordinary users need only connect URL; SSH remains an optional explicit shortcut.
	if required && s.flags["ssh"] == "" {
		fmt.Fprintln(s.errOut, "This controller has no owner yet. Use your existing SSH administrator access for one-time setup.")
		destination, err := s.ask("SSH destination (USER@HOST or an SSH config alias; blank to cancel): ", false)
		// Prompt cancellation never grants ownership or writes a profile.
		if err != nil {
			return "", err
		}
		s.flags["ssh"] = strings.TrimSpace(destination)
		// Blank input is cancellation rather than an implicit root login.
		if s.flags["ssh"] == "" {
			return "", model.Err("CANCELLED", "Owner setup cancelled; no profile was saved")
		}
		// Prompted destinations receive the same validation as command-line arguments.
		if err = s.validateSSHBootstrap(client.Enrollment{Method: "browser"}); err != nil {
			return "", err
		}
	}
	// Existing owners proceed directly to ordinary browser approval without SSH.
	if s.flags["ssh"] == "" {
		return address, nil
	}
	return s.bootstrapPairingURL(id, pollSupported)
}
