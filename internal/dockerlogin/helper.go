package dockerlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// limitedOutput prevents a broken helper from exhausting controller memory.
type limitedOutput struct{ bytes.Buffer }

// Write rejects output beyond the credential protocol's bounded response size.
func (b *limitedOutput) Write(p []byte) (int, error) {
	// Helper diagnostics and credentials are never allowed unbounded buffering.
	if b.Len()+len(p) > 64*1024 {
		return 0, errors.New("credential helper output limit")
	}
	return b.Buffer.Write(p)
}

// helper executes the exact configured helper with no shell and only the read-only get verb.
func (s *Source) helper(ctx context.Context, name, server string) (Credential, bool, error) {
	// Reject paths and shell syntax from the operator's helper selector.
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(name) {
		return Credential{}, false, failure("Invalid Docker credential helper name")
	}
	path, err := s.helperPath("docker-credential-" + name)
	// A configured but unavailable helper cannot silently fall back to another identity.
	if err != nil {
		return Credential{}, false, err
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, path, "get")
	cmd.Stdin = strings.NewReader(server)
	cmd.Dir = s.Home
	cmd.Env = s.helperEnvironment()
	cmd.WaitDelay = 500 * time.Millisecond
	// Drop root authority before any user-selected executable or interpreter starts.
	if err = s.helperIdentity(cmd); err != nil {
		return Credential{}, false, err
	}
	var output limitedOutput
	cmd.Stdout = &output
	err = cmd.Run()
	// Docker's standard not-found response is the only helper failure that permits anonymous access.
	if err != nil && strings.TrimSpace(output.String()) == "credentials not found in native keychain" {
		return Credential{}, false, nil
	}
	// Never expose helper stdout, stderr, arguments, or underlying subprocess errors.
	if err != nil {
		return Credential{}, false, failure("Docker credential helper failed; ensure it is installed and unlocked for the server user in a noninteractive service")
	}
	return parseHelper(output.Bytes(), server)
}

// parseHelper validates a successful helper result without retaining protocol-specific fields.
func parseHelper(raw []byte, server string) (Credential, bool, error) {
	var result struct{ Username, Secret string }
	// Invalid helper responses must not be mistaken for a successful anonymous lookup.
	if json.Unmarshal(raw, &result) != nil || result.Username == "" || result.Secret == "" {
		return Credential{}, false, failure("Docker credential helper returned invalid credentials")
	}
	c := Credential{Username: result.Username, Password: result.Secret, ServerAddress: server}
	// Docker stores identity/refresh tokens using the special helper username.
	if c.Username == "<token>" {
		c.IdentityToken, c.Username, c.Password = c.Password, "", ""
	}
	return c, true, nil
}

// helperSearchPath excludes the working directory and includes conventional user-installed helpers.
func (s *Source) helperSearchPath() string {
	return filepath.Join(s.Home, ".local/bin") + ":" + filepath.Join(s.Home, "bin") + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
}

// helperPath resolves executable files without trusting the controller's inherited environment.
func (s *Source) helperPath(name string) (string, error) {
	// Search only explicit absolute directories associated with the recorded account or system.
	for _, directory := range filepath.SplitList(s.helperSearchPath()) {
		path := filepath.Join(directory, name)
		info, err := os.Stat(path)
		// Let process creation enforce executable permissions under the selected identity.
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	return "", failure("Docker credential helper is unavailable in the server user's standard executable paths")
}

// helperEnvironment supplies only the login context required by common server credential stores.
func (s *Source) helperEnvironment() []string {
	runtimeDir := "/run/user/" + s.UID
	return []string{"HOME=" + s.Home, "USER=" + s.Username, "LOGNAME=" + s.Username, "PATH=" + s.helperSearchPath(), "DOCKER_CONFIG=" + s.Directory, "XDG_RUNTIME_DIR=" + runtimeDir, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDir + "/bus"}
}

// helperIdentity avoids executing an ordinary user's credential program as the root controller.
func (s *Source) helperIdentity(cmd *exec.Cmd) error {
	// Validate numeric IDs before converting them into operating-system credentials.
	if err := s.Validate(); err != nil {
		return err
	}
	uid, _ := strconv.ParseUint(s.UID, 10, 32)
	gid, _ := strconv.ParseUint(s.GID, 10, 32)
	// Unprivileged direct controllers may use only their own account.
	if os.Geteuid() != 0 && os.Geteuid() != int(uid) {
		return failure("Controller cannot access another user's Docker credential helper")
	}
	// Explicit empty supplementary groups avoid inheriting root's group privileges.
	if os.Geteuid() == 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}}
	}
	return nil
}
