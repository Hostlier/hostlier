package sshaccess

import (
	"context"
	"os"
	"strconv"
	"strings"

	"hostlier/internal/model"
	"hostlier/internal/sshidentity"
)

// inspectSSHD requires a compatible existing OpenSSH installation; it never weakens daemon policy.
func inspectSSHD(ctx context.Context, user string) (string, int, error) {
	raw, err := command(ctx, "/usr/sbin/sshd", "-T", "-C", "user="+user+",host=localhost,addr=127.0.0.1")
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return "", 0, model.Err("SSH_UNAVAILABLE", "An existing configured OpenSSH server is required")
	}
	settings := parseSSHDSettings(raw)
	// Preserve failure before publishing or using SSH authority.
	if err := validateSSHDLogin(settings); err != nil {
		return "", 0, err
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if len(settings["port"]) != 1 {
		return "", 0, model.Err("SSH_UNAVAILABLE", "Choose a single SSH listener port before managed enrollment")
	}
	port, err := strconv.Atoi(settings["port"][0])
	// Preserve failure before publishing or using SSH authority.
	if err != nil || port < 1 || port > 65535 {
		return "", 0, model.Err("SSH_UNAVAILABLE", "Invalid SSH listener port")
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if !strings.Contains(" "+strings.Join(settings["hostkey"], " ")+" ", " /etc/ssh/ssh_host_ed25519_key ") {
		return "", 0, model.Err("SSH_UNAVAILABLE", "An Ed25519 SSH host key is required")
	}
	raw, err = os.ReadFile("/etc/ssh/ssh_host_ed25519_key.pub")
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return "", 0, err
	}
	key, _, err := sshidentity.PublicKey(string(raw))
	return key, port, err
}

// validateSSHDLogin requires keys in the root-owned home and the standard locked-password PAM path.
func validateSSHDLogin(settings map[string][]string) error {
	// Reject ambiguous state or honor the explicit SSH selection.
	if strings.Join(settings["usepam"], "") != "yes" || strings.Join(settings["pubkeyauthentication"], "") != "yes" || strings.Join(settings["authorizedkeyscommand"], "") != "none" {
		return model.Err("SSH_UNAVAILABLE", "Managed SSH requires PAM, public-key login and no external AuthorizedKeysCommand")
	}
	// Validate every configured value at this trust boundary.
	for _, entry := range strings.Fields(strings.Join(settings["authorizedkeysfile"], " ")) {
		// Reject ambiguous state or honor the explicit SSH selection.
		if entry != ".ssh/authorized_keys" && entry != ".ssh/authorized_keys2" {
			return model.Err("SSH_UNAVAILABLE", "Custom AuthorizedKeysFile requires administrator configuration")
		}
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if !strings.Contains(" "+strings.Join(settings["authorizedkeysfile"], " ")+" ", " .ssh/authorized_keys ") {
		return model.Err("SSH_UNAVAILABLE", "Standard authorized_keys is required")
	}
	return nil
}

// parseSSHDSettings preserves repeated effective settings such as host keys and listener ports.
func parseSSHDSettings(raw []byte) map[string][]string {
	settings := map[string][]string{}
	// Validate every configured value at this trust boundary.
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, " ")
		// Reject ambiguous state or honor the explicit SSH selection.
		if ok {
			settings[k] = append(settings[k], v)
		}
	}
	return settings
}
