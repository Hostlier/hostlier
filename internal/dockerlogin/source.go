// Package dockerlogin reads an operator's Docker credentials without modifying their configuration.
package dockerlogin

import (
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"

	"hostlier/internal/model"
)

// Source records only the local account and configuration location, never registry secrets.
type Source struct {
	Username  string `json:"username"`
	UID       string `json:"uid"`
	GID       string `json:"gid"`
	Home      string `json:"home"`
	Directory string `json:"directory"`
}

// Discover selects the sudo invoker's login, or the current account for direct execution.
func Discover() (*Source, error) {
	account, err := user.Current()
	// Only a privileged sudo invocation may select another account through sudo's metadata.
	if os.Geteuid() == 0 && os.Getenv("SUDO_USER") != "" {
		account, err = user.Lookup(os.Getenv("SUDO_USER"))
	}
	// Never silently substitute root when the intended account cannot be resolved.
	if err != nil {
		return nil, failure("Cannot identify the Docker login owner")
	}
	directory := os.Getenv("DOCKER_CONFIG")
	// Docker's ordinary config location is relative to the selected account, not sudo's HOME.
	if directory == "" {
		directory = filepath.Join(account.HomeDir, ".docker")
	}
	directory, err = filepath.Abs(directory)
	// Record a stable absolute source before systemd changes the working directory.
	if err != nil {
		return nil, failure("Docker config directory is invalid")
	}
	s := &Source{Username: account.Username, UID: account.Uid, GID: account.Gid, Home: account.HomeDir, Directory: directory}
	return s, s.Validate()
}

// Validate rejects unsafe service paths and invalid numeric process identities.
func (s *Source) Validate() error {
	safe := regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
	// Paths also become explicit read-only systemd mounts, so exclude unit-file metacharacters.
	for _, path := range []string{s.Home, s.Directory} {
		// Clean absolute paths prevent unexpected mount traversal or root-wide exposure.
		if !safe.MatchString(path) || filepath.Clean(path) != path || path == "/" {
			return failure("Docker login paths must be clean absolute paths without whitespace")
		}
	}
	// Helpers run with the saved account's numeric identity, never an inferred privilege level.
	for _, id := range []string{s.UID, s.GID} {
		// Reject malformed IDs before constructing a helper subprocess.
		if _, err := strconv.ParseUint(id, 10, 32); err != nil {
			return failure("Docker login account has an invalid numeric identity")
		}
	}
	return nil
}

// failure returns actionable metadata without including config contents or helper output.
func failure(message string) error { return model.Err("REGISTRY_CREDENTIALS", message) }
