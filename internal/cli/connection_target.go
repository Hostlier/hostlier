package cli

import (
	"net/url"
	"strings"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// connectionTarget captures the exact saved identity that fresh enrollment may replace.
type connectionTarget struct {
	Name     string
	URL      string
	Previous *client.Profile
}

// resolveConnection accepts a new HTTPS origin, a saved name or the selected default.
func (s *session) resolveConnection(renewOnly bool) (connectionTarget, error) {
	profiles, err := s.store.List()
	// Corrupt metadata must not be treated as permission to create a replacement profile.
	if err != nil {
		return connectionTarget{}, err
	}
	// URLs always use canonical origin validation, including insecure or malformed schemes.
	if strings.Contains(s.sub, "://") {
		// The compatibility renewal alias never creates new profiles from URLs.
		if renewOnly {
			return connectionTarget{}, model.Err("ARGUMENTS", "Use hostlier connect URL --name NAME for a new controller")
		}
		return s.connectionURL(profiles)
	}
	name := defaultValue(s.sub, defaultValue(s.flags["server"], profiles.Current))
	// --name names a new URL connection, not a second selector for a saved profile.
	if s.flags["name"] != "" {
		return connectionTarget{}, model.Err("ARGUMENTS", "Use connect URL --name NAME or connect PROFILE")
	}
	previous, ok := profiles.Servers[name]
	// Missing profiles require an explicit controller URL.
	if !ok {
		return connectionTarget{}, model.Err("PROFILE_UNKNOWN", "No matching server profile; run hostlier connect https://CONTROLLER --name NAME")
	}
	return connectionTarget{Name: name, URL: previous.URL, Previous: &previous}, nil
}

// connectionURL prevents a familiar profile name from silently switching controller origins.
func (s *session) connectionURL(profiles client.Profiles) (connectionTarget, error) {
	address, err := client.ControllerURL(s.sub)
	// Invalid HTTPS origins cannot receive credentials or approval requests.
	if err != nil {
		return connectionTarget{}, err
	}
	// --server selects saved profiles; --name names URL connections.
	if s.flags["server"] != "" {
		return connectionTarget{}, model.Err("ARGUMENTS", "Use connect URL --name NAME; --server selects an existing profile")
	}
	name := s.flags["name"]
	if name == "" {
		name, err = profileNameForOrigin(profiles, address)
		if err != nil {
			return connectionTarget{}, err
		}
	}
	// Validate names before contacting the controller or consuming enrollment material.
	if err = client.ValidateProfileName(name); err != nil {
		return connectionTarget{}, err
	}
	target := connectionTarget{Name: name, URL: address}
	previous, exists := profiles.Servers[name]
	// Repeating the same URL and name renews in place through the same workflow.
	if exists {
		// Changing a saved origin needs a distinct profile name, never implicit replacement.
		if previous.URL != address {
			return target, model.Err("PROFILE_EXISTS", "Profile belongs to another controller; choose a different --name")
		}
		target.Previous = &previous
	}
	return target, nil
}

// validateConnectionArguments rejects ambiguous selectors and unrelated transport flags early.
func (s *session) validateConnectionArguments() error {
	// Connection accepts at most one origin or profile selector.
	if len(s.parsed.args) > 2 || s.sub != "" && s.flags["server"] != "" {
		return model.Err("ARGUMENTS", "Use hostlier connect [URL | PROFILE]; choose only one profile selector")
	}
	allowed := map[string]bool{"ssh": true, "ssh-config": true, "ssh-socket": true, "ssh-sudo": true, "name": true, "server": true, "auth": true, "email": true, "request": true, "token-stdin": true, "json": true, "toon": true}
	// Local administration and workload flags must not be silently ignored during authentication.
	for flag, value := range s.flags {
		// Keep the supported compatibility choices explicit and bounded.
		if !allowed[flag] || value == "" {
			return model.Err("ARGUMENTS", "Unsupported connection option --"+flag+"; run hostlier connect --help")
		}
	}
	return nil
}

// Prefer an existing connection to the same origin. Keep the historical first profile name
// and name further controllers after their host without overwriting an existing profile.
func profileNameForOrigin(profiles client.Profiles, origin string) (string, error) {
	match, err := savedProfileForOrigin(profiles, origin)
	if err != nil || match != "" {
		return match, err
	}
	if _, exists := profiles.Servers["default"]; !exists {
		return "default", nil
	}
	u, _ := url.Parse(origin)
	name := strings.NewReplacer(".", "-", ":", "-").Replace(u.Hostname())
	if port := u.Port(); port != "" {
		name += "-" + port
	}
	if client.ValidateProfileName(name) != nil {
		return "", model.Err("PROFILE_NAME", "Supply --name for this controller")
	}
	return name, nil
}

func savedProfileForOrigin(profiles client.Profiles, origin string) (string, error) {
	if profiles.Servers[profiles.Current].URL == origin {
		return profiles.Current, nil
	}
	var match string
	for name, profile := range profiles.Servers {
		if profile.URL != origin {
			continue
		}
		if match != "" {
			return "", model.Err("ARGUMENTS", "Multiple profiles use this URL. Choose a saved profile by name")
		}
		match = name
	}
	return match, nil
}
