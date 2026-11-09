package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"hostlier/internal/client"
	"hostlier/internal/model"
	"hostlier/internal/securitykey"
)

// approveCommand runs from the owner's terminal and never needs an SSH session on the controller.
func (s *session) approveCommand() error {
	// Reject the request before side effects: Usage: approve CODE --key KEY_FILE [--url https://CONTROLLER]; an interactive terminal is required.
	if len(s.parsed.args) != 2 || s.flags["key"] == "" || !s.interactive || s.flags["yes"] != "" {
		return model.Err("ARGUMENTS", "Usage: approve CODE --key KEY_FILE [--url https://CONTROLLER]; an interactive terminal is required")
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.flags["url"] != "" {
		remote, err := client.NewGRPC(client.Options{URL: s.flags["url"]})
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err != nil {
			return err
		}
		s.remote = remote
		s.serverTarget = remote.Target
	} else {
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err := s.connectRemote(); err != nil {
			return err
		}
	}
	return s.signHumanApproval(s.sub)
}

// signHumanApproval shows controller-retained intent before requesting a hardware PIN and touch.
func (s *session) signHumanApproval(id string) error {
	// Reject the request before side effects: Hardware approval requires the owner's interactive terminal.
	if !s.humanSigningTerminal() {
		return model.Err("INTERACTIVE_REQUIRED", "Hardware approval requires the owner's interactive terminal")
	}
	public, err := os.ReadFile(s.flags["key"] + ".pub")
	// Reject the request before side effects: Cannot read the hardware key's public file.
	if err != nil {
		return model.Err("KEY_FILE", "Cannot read the hardware key's public file")
	}
	key, err := securitykey.Parse(string(public))
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	var review struct {
		ID          string          `json:"id"`
		Fingerprint string          `json:"fingerprint"`
		Review      json.RawMessage `json:"review"`
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err = s.remote.Request(s.ctx, "GET", "/v1/human/review/"+url.PathEscape(id), nil, &review); err != nil {
		return err
	}
	fmt.Fprintln(s.errOut, "Review controller:", s.serverTarget)
	// Every format escapes terminal control sequences so names cannot alter the displayed review.
	if err = s.showApprovalReview(review.Review); err != nil {
		return err
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err = s.confirmExactApproval(); err != nil {
		return err
	}
	var ceremony struct {
		ID      string `json:"ceremony"`
		Message string `json:"message"`
	}
	body := map[string]string{"id": review.ID, "fingerprint": review.Fingerprint, "decision": "approve", "keyFingerprint": ssh.FingerprintSHA256(key)}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err = s.remote.Request(s.ctx, "POST", "/v1/human/decision/begin", body, &ceremony); err != nil {
		return err
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err = checkSigningIntent(ceremony.Message, review.ID, review.Fingerprint, ssh.FingerprintSHA256(key), s.serverTarget); err != nil {
		return err
	}
	signature, err := s.hardwareSign(ceremony.Message)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	var result any
	return s.remote.Request(s.ctx, "POST", "/v1/human/decision/finish", map[string]string{"ceremony": ceremony.ID, "signature": signature}, &result)
}

// checkSigningIntent prevents a signing challenge from substituting a different displayed request or decision.
func checkSigningIntent(message, id, fingerprint, key, target string) error {
	var intent struct {
		Protocol       string `json:"protocol"`
		ControllerHost string `json:"controllerHost"`
		RequestID      string `json:"requestId"`
		Fingerprint    string `json:"fingerprint"`
		Decision       string `json:"decision"`
		KeyFingerprint string `json:"keyFingerprint"`
		ExpiresAt      int64  `json:"expiresAt"`
	}
	// Reject the request before side effects: Controller signing challenge does not match the reviewed request.
	if json.Unmarshal([]byte(message), &intent) != nil || intent.Protocol != securitykey.Namespace || intent.RequestID != id || intent.Fingerprint != fingerprint || intent.Decision != "approve" || intent.KeyFingerprint != key || intent.ExpiresAt <= time.Now().UnixMilli() {
		return model.Err("SIGNING_INTENT", "Controller signing challenge does not match the reviewed request")
	}
	return checkSigningAudience(intent.ControllerHost, target)
}

// hardwareSign invokes OpenSSH directly; secrets and PINs are never command arguments or controller payloads.
func (s *session) hardwareSign(message string) (string, error) {
	command := exec.CommandContext(s.ctx, "ssh-keygen", "-Y", "sign", "-n", securitykey.Namespace, "-f", s.flags["key"])
	command.Env = hardwareEnvironment()
	command.Stdin = strings.NewReader(message)
	command.Stderr = s.errOut
	var output bytes.Buffer
	command.Stdout = &output
	// Reject the request before side effects: Hardware signing failed; use a FIDO-capable OpenSSH build and a key configured for PIN verification and touch.
	if err := command.Run(); err != nil {
		return "", model.Err("HARDWARE_SIGNING", "Hardware signing failed; use a FIDO-capable OpenSSH build and a key configured for PIN verification and touch")
	}
	// Reject the request before side effects: Hardware signature exceeds its bounded format.
	if output.Len() > 16384 {
		return "", model.Err("HARDWARE_SIGNING", "Hardware signature exceeds its bounded format")
	}
	return output.String(), nil
}

// hardwareEnvironment excludes agent forwarding, arbitrary authenticator providers and graphical askpass hooks.
func hardwareEnvironment() []string {
	env := []string{}
	// Remove inherited agent and graphical signing hooks from the human ceremony.
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		// Apply this check before granting authority or advancing protocol state.
		if key != "SSH_AUTH_SOCK" && key != "SSH_ASKPASS" && key != "SSH_ASKPASS_REQUIRE" && key != "SSH_SK_PROVIDER" {
			env = append(env, value)
		}
	}
	return append(env, "SSH_ASKPASS_REQUIRE=never")
}

// securityKeygen creates a dedicated hardware key without overwriting an existing key handle.
func (s *session) securityKeygen() error {
	path := s.flags["key"]
	// Reject the request before side effects: Usage: security keygen --key FILE in an interactive terminal.
	if path == "" || !s.interactive {
		return model.Err("ARGUMENTS", "Usage: security keygen --key FILE in an interactive terminal")
	}
	// Apply this check before granting authority or advancing protocol state.
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.cwd, path)
	}
	// Validate each entry before accepting the complete collection.
	for _, file := range []string{path, path + ".pub"} {
		// Reject the request before side effects: Key file exists or is inaccessible.
		if _, err := os.Lstat(file); !os.IsNotExist(err) {
			return model.Err("KEY_EXISTS", "Key file exists or is inaccessible")
		}
	}
	command := exec.CommandContext(s.ctx, "ssh-keygen", "-t", "ecdsa-sk", "-O", "verify-required", "-O", "application=ssh:hostlier", "-f", path)
	command.Env = hardwareEnvironment()
	command.Stdin = s.in
	command.Stdout = s.out
	command.Stderr = s.errOut
	return command.Run()
}

// confirmExactApproval keeps the human review decision before any hardware operation.
func (s *session) confirmExactApproval() error {
	answer, err := s.ask("Approve this exact request? [y/N] ", false)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	// Reject the request before side effects: Approval cancelled.
	if !affirmative(answer) {
		return model.Err("CANCELLED", "Approval cancelled")
	}
	return nil
}

// humanSigningTerminal disallows unattended confirmation of a hardware ceremony.
func (s *session) humanSigningTerminal() bool { return s.interactive && s.flags["yes"] == "" }

// checkSigningAudience prevents a different HTTPS hostname from relaying another controller's signing challenge.
func checkSigningAudience(host, target string) error {
	// Protected local sockets are already trusted recovery authority.
	if strings.HasPrefix(target, "unix:") && host != "" {
		return nil
	}
	parsed, err := url.Parse(target)
	// An alias or IP connection must use the configured controller hostname for human signing.
	if err != nil || parsed.Scheme != "https" || host == "" || !strings.EqualFold(parsed.Hostname(), host) {
		return model.Err("SIGNING_AUDIENCE", "Connect to the configured controller hostname before signing")
	}
	return nil
}
