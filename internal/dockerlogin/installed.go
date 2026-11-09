package dockerlogin

import (
	"encoding/json"
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"strings"
)

// InstalledSource loads only the credential ownership required by the running controller.
func InstalledSource(stateDir string) (*Source, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "installation.json"))
	// Fresh direct controllers use their own login identity.
	if os.IsNotExist(err) {
		return Discover()
	}
	// An unreadable record cannot authorize fallback to another account.
	if err != nil {
		return nil, err
	}
	var installed struct {
		DockerMode  string
		DockerLogin *Source
	}
	// Preserve installer validation without importing the host installation machinery.
	if json.Unmarshal(data, &installed) != nil || strings.TrimSpace(string(data)) == "null" {
		return nil, model.Err("INSTALL_STATE", "Existing installation state is unreadable")
	}
	// Unknown Docker ownership cannot grant runtime authority.
	if installed.DockerMode != "" && installed.DockerMode != "existing" && installed.DockerMode != "managed" {
		return nil, model.Err("INSTALL_STATE", "Unknown Docker ownership in installation state")
	}
	// Legacy records without a selected account retain direct-controller discovery.
	if installed.DockerLogin == nil {
		return Discover()
	}
	return installed.DockerLogin, installed.DockerLogin.Validate()
}
