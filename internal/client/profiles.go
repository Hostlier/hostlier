package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"hostlier/internal/model"
)

// Profile stores public connection metadata separately from bearer credentials.
type Profile struct {
	URL           string     `json:"url"`
	ClientID      string     `json:"clientId"`
	CredentialKey string     `json:"credentialKey,omitempty"`
	Enrollment    Enrollment `json:"enrollment,omitzero"`
}

// Profiles records named servers and the current CLI selection.
type Profiles struct {
	SSH         map[string]SSHConnection `json:"ssh,omitempty"`
	CurrentHost string                   `json:"currentHost,omitempty"`
	Current     string                   `json:"current,omitempty"`
	Servers     map[string]Profile       `json:"servers"`
}

// Store owns the protected credential directory and its durable profile documents.
type Store struct{ Home string }

// NewStore resolves the explicit, environment or default credential directory.
func NewStore(home string) Store {
	// Honor HOSTLIER_HOME when no explicit directory was provided.
	if home == "" {
		home = os.Getenv("HOSTLIER_HOME")
	}
	// Fall back to the user profile directory only after both overrides are absent.
	if home == "" {
		root, _ := os.UserHomeDir()
		home = filepath.Join(root, ".hostlier")
	}
	return Store{Home: home}
}

// read loads an optional profile document while distinguishing absence from corruption.
func (s Store) read(name string, value any) error {
	data, e := os.ReadFile(filepath.Join(s.Home, name))
	// Treat a missing document as an empty initial store.
	if os.IsNotExist(e) {
		return nil
	}
	// Preserve I/O failures rather than replacing unreadable credentials.
	if e != nil {
		return e
	}
	return json.Unmarshal(data, value)
}

// write atomically commits owner-only profile data and syncs its directory.
func (s Store) write(name string, value any) error {
	// Create a private directory before writing credentials.
	if e := os.MkdirAll(s.Home, 0700); e != nil {
		return e
	}
	// Correct existing directory permissions before placing sensitive data there.
	if e := os.Chmod(s.Home, 0700); e != nil {
		return e
	}
	data, e := json.MarshalIndent(value, "", "  ")
	// Abort before opening a temporary file if JSON encoding fails.
	if e != nil {
		return e
	}
	temp, e := os.CreateTemp(s.Home, ".profile-*")
	// Require a fresh temporary file in the target directory for atomic rename.
	if e != nil {
		return e
	}
	defer os.Remove(temp.Name())
	// Write credentials only after owner-only file permissions are enforced.
	if e = temp.Chmod(0600); e == nil {
		_, e = temp.Write(append(data, '\n'))
	}
	// Flush complete bytes before publishing the new document.
	if e == nil {
		e = temp.Sync()
	}
	closeErr := temp.Close()
	// Preserve write or fsync failures rather than publishing incomplete data.
	if e != nil {
		return e
	}
	// Treat a failed close as a failed credential commit.
	if closeErr != nil {
		return closeErr
	}
	// Atomically replace the document only after its temporary file is durable.
	if e = os.Rename(temp.Name(), filepath.Join(s.Home, name)); e != nil {
		return e
	}
	return syncProfileDirectory(s.Home)
}

// List returns saved profile metadata without reading bearer credentials.
func (s Store) List() (Profiles, error) {
	p := Profiles{Servers: map[string]Profile{}}
	e := s.read("profiles.json", &p)
	return p, e
}

// Get resolves a selected profile together with its separately stored credential.
func (s Store) Get(name string) (Options, error) {
	p, e := s.List()
	// Preserve profile-read failures before resolving a server.
	if e != nil {
		return Options{}, e
	}
	// Use the selected default when the caller did not name a server.
	if name == "" {
		name = p.Current
	}
	profile, ok := p.Servers[name]
	// Require an enrolled profile before creating remote client options.
	if !ok {
		return Options{}, model.Err("NO_SERVER", "No server selected; connect a profile or pass --socket")
	}
	secrets := map[string]string{}
	// Read bearer credentials from their separate protected document.
	if e = s.read("credentials.json", &secrets); e != nil {
		return Options{}, e
	}
	key := profileCredentialKey(name, profile)
	// Fail closed when the profile metadata has no matching credential.
	if secrets[key] == "" {
		return Options{}, model.Err("NO_CREDENTIAL", "Server credential missing; run hostlier connect "+name)
	}
	return Options{URL: profile.URL, Token: secrets[key]}, nil
}

// Save validates and persists a newly enrolled profile after storing its credential.
func (s Store) Save(name, address, id, token string) error {
	return s.SaveEnrollment(name, address, id, token, Enrollment{})
}

// SaveEnrollment publishes a new profile with its non-secret authentication preferences.
func (s Store) SaveEnrollment(name, address, id, token string, enrollment Enrollment) error {
	// Validate the profile name before writing any credential.
	if e := ValidateProfileName(name); e != nil {
		return e
	}
	address, e := ControllerURL(address)
	// Require a canonical HTTPS origin for the saved server.
	if e != nil {
		return e
	}
	p, e := s.List()
	// Load existing metadata before checking for duplicate names.
	if e != nil {
		return e
	}
	// Never silently overwrite an existing enrolled identity.
	if _, ok := p.Servers[name]; ok {
		return model.Err("PROFILE_EXISTS", "Profile already exists; renew it with hostlier connect "+name)
	}
	secrets := map[string]string{}
	// Preserve existing credential records while adding the new identity.
	if e = s.read("credentials.json", &secrets); e != nil {
		return e
	}
	p.Servers[name] = Profile{URL: address, ClientID: id, Enrollment: enrollment}
	p.Current, p.CurrentHost = name, ""
	secrets[name] = token
	// Commit credentials first so published metadata never points at an unwritten token.
	if e = s.write("credentials.json", secrets); e != nil {
		return e
	}
	return s.write("profiles.json", p)
}

// Use selects an existing profile without changing its credential.
func (s Store) Use(name string) error { return s.UseHost(name, "") }

// UseHost publishes the master and workload destination in the same profile document.
func (s Store) UseHost(name, host string) error {
	p, e := s.List()
	// Preserve profile-read failures before changing the selection.
	if e != nil {
		return e
	}
	// Reject unknown names instead of leaving a broken default.
	if _, ok := p.Servers[name]; !ok {
		return model.Err("PROFILE_UNKNOWN", "Unknown server profile")
	}
	p.Current, p.CurrentHost = name, host
	return s.write("profiles.json", p)
}

// Remove deletes a profile and its local credential while clearing a removed default.
func (s Store) Remove(name string) error {
	p, e := s.List()
	// Load metadata before deciding which profile to disconnect.
	if e != nil {
		return e
	}
	// Disconnect the selected default when no explicit name was supplied.
	if name == "" {
		name = p.Current
	}
	// Reject names absent from saved metadata.
	if _, ok := p.Servers[name]; !ok {
		return model.Err("PROFILE_UNKNOWN", "Unknown server profile")
	}
	secrets := map[string]string{}
	// Load the separate token document before removing the matching credential.
	if e = s.read("credentials.json", &secrets); e != nil {
		return e
	}
	delete(secrets, profileCredentialKey(name, p.Servers[name]))
	delete(p.Servers, name)
	for target := range p.SSH {
		// Removing a profile also forgets its associated native SSH destinations.
		if target == name || strings.HasPrefix(target, name+"/") {
			delete(p.SSH, target)
		}
	}
	// Clear the selection when its profile is removed.
	if p.Current == name {
		p.Current, p.CurrentHost = "", ""
	}
	// Remove the local credential before publishing the metadata change.
	if e = s.write("credentials.json", secrets); e != nil {
		return e
	}
	return s.write("profiles.json", p)
}

// ValidateProfileName rejects names that cannot be stored and addressed portably.
func ValidateProfileName(name string) error {
	// Restrict profile identities to bounded portable ASCII names.
	if name == "" || len(name) > 64 || strings.IndexFunc(name, invalidProfileRune) >= 0 {
		return model.Err("PROFILE_NAME", "Invalid profile name")
	}
	return nil
}

// syncProfileDirectory makes an atomically renamed credential file durable.
func syncProfileDirectory(home string) error {
	directory, e := os.Open(home)
	// Require an open directory handle before committing its rename metadata.
	if e != nil {
		return e
	}
	defer directory.Close()
	return directory.Sync()
}

// invalidProfileRune restricts names to portable ASCII letters, digits and separators.
func invalidProfileRune(c rune) bool {
	return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-')
}
