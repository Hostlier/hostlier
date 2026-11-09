package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"hostlier/internal/model"
)

// exportSDKCredential isolates one owner-approved signed profile for a product service.
// The CLI is needed only for credential setup; cloud SDKs call controller APIs directly.
func (s *session) exportSDKCredential() error {
	if len(s.parsed.args) != 3 || s.flags["server"] == "" || s.flags["output"] == "" {
		return model.Err("ARGUMENTS", "Usage: sdk credential export --server PROFILE --output PRIVATE_FILE")
	}
	for flag := range s.flags {
		if flag != "server" && flag != "output" && flag != "json" && flag != "toon" {
			return model.Err("ARGUMENTS", "Credential export accepts only --server and --output")
		}
	}
	profile := s.flags["server"]
	settings, err := s.store.Get(profile)
	if err != nil {
		return err
	}
	// Legacy bearer tokens cannot authorize device-signed controller requests.
	if !strings.HasPrefix(settings.Token, "device-v1:") {
		return model.Err("NO_CREDENTIAL", "Profile needs owner-approved signed device enrollment")
	}
	profiles, err := s.store.List()
	if err != nil {
		return err
	}
	clientID := profiles.Servers[profile].ClientID
	if clientID == "" {
		return model.Err("NO_CREDENTIAL", "Profile has no enrolled client identity")
	}
	path, err := filepath.Abs(s.flags["output"])
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(struct {
		ControllerURL string `json:"controllerUrl"`
		Credential    string `json:"credential"`
		ClientID      string `json:"clientId"`
	}{settings.URL, settings.Token, clientID}, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return model.Err("SDK_CREDENTIAL_EXISTS", "Credential file already exists; choose a new path")
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return s.print(map[string]string{"credentialFile": path, "clientId": clientID, "controllerUrl": settings.URL})
}
