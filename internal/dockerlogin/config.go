package dockerlogin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const hub = "https://index.docker.io/v1/"

// Credential uses Docker Engine's authentication payload, including device-login identity tokens.
type Credential struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	IdentityToken string `json:"identitytoken,omitempty"`
	RegistryToken string `json:"registrytoken,omitempty"`
	ServerAddress string `json:"serveraddress,omitempty"`
	Auth          string `json:"auth,omitempty"`
}

// dockerConfig decodes only fields participating in Docker credential lookup.
type dockerConfig struct {
	Auths   map[string]Credential `json:"auths"`
	Helpers map[string]string     `json:"credHelpers"`
	Store   string                `json:"credsStore"`
}

// Registry identifies an image's registry and normalizes Docker Hub's historical aliases.
func Registry(image string) string {
	first := strings.Split(image, "/")[0]
	// Unqualified image names always belong to Docker Hub.
	if !strings.Contains(image, "/") || (!strings.ContainsAny(first, ".:") && first != "localhost") {
		return "docker.io"
	}
	return Canonical(first)
}

// Canonical compares config keys without forwarding credentials to subdomains or other ports.
func Canonical(key string) string {
	key = strings.TrimPrefix(strings.TrimPrefix(key, "https://"), "http://")
	key = strings.Split(key, "/")[0]
	// Docker Hub commonly stores its login under a legacy URL rather than the image hostname.
	if key == "index.docker.io" || key == "registry-1.docker.io" {
		return "docker.io"
	}
	return key
}

// Lookup reloads configuration and invokes only the selected helper's get operation.
func (s *Source) Lookup(ctx context.Context, registry string) (Credential, bool, error) {
	// Embedders without an explicit login source retain anonymous pulls.
	if s == nil {
		return Credential{}, false, nil
	}
	cfg, err := s.readConfig()
	// Invalid or unreadable configuration must not silently downgrade an authenticated pull.
	if err != nil {
		return Credential{}, false, err
	}
	registry = Canonical(registry)
	server := registry
	// Docker's helpers expect the canonical historical index URL for Hub.
	if registry == "docker.io" {
		server = hub
	}
	helper := cfg.Helpers[server]
	// Exact per-registry helper entries take precedence over the default store.
	if helper == "" {
		helper = cfg.Helpers[registry]
	}
	// A configured global store supersedes inline credentials, matching Docker CLI behavior.
	if helper == "" {
		helper = cfg.Store
	}
	// Never fall back to stale inline secrets after an explicit helper failure or missing entry.
	if helper != "" {
		return s.helper(ctx, helper, server)
	}
	return inlineCredential(cfg.Auths, server)
}

// readConfig bounds local config reads and treats only absence as an anonymous configuration.
func (s *Source) readConfig() (dockerConfig, error) {
	var cfg dockerConfig
	file, err := os.OpenFile(filepath.Join(s.Directory, "config.json"), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	// A server without a Docker login continues to support public images.
	if os.IsNotExist(err) {
		return cfg, nil
	}
	// Hide operating-system details that could reveal unexpected source paths.
	if err != nil {
		return cfg, failure("Cannot read the server user's Docker config")
	}
	defer file.Close()
	info, err := file.Stat()
	// Devices and huge files are not valid Docker login configurations.
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return cfg, failure("Docker config must be a regular file smaller than 1 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	// Reject truncated, oversized or malformed configuration before credential selection.
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &cfg) != nil {
		return cfg, failure("Docker config is invalid; check it with the server user's Docker CLI")
	}
	return cfg, nil
}

// inlineCredential prefers an exact Docker key before trying legacy registry URL spellings.
func inlineCredential(auths map[string]Credential, server string) (Credential, bool, error) {
	credential, found := auths[server]
	// Deterministic alias resolution matches only the same registry authority.
	if !found {
		keys := make([]string, 0, len(auths))
		// Sort aliases so map iteration cannot select different identities on repeated pulls.
		for key := range auths {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		// Stop at the first matching authority; unrelated registry credentials remain untouched.
		for _, key := range keys {
			// A legacy URL may match only the exact canonical registry.
			if Canonical(key) == Canonical(server) {
				credential, found = auths[key]
				break
			}
		}
	}
	// No matching entry is a normal anonymous registry request.
	if !found {
		return Credential{}, false, nil
	}
	credential.ServerAddress = server
	return decodeCredential(credential)
}

// decodeCredential accepts Docker's base64 auth or token fields without retaining the encoded copy.
func decodeCredential(c Credential) (Credential, bool, error) {
	// Inline auth is the Docker CLI's encoded username/password representation.
	if c.Auth != "" {
		raw, err := base64.StdEncoding.DecodeString(c.Auth)
		username, password, ok := strings.Cut(string(raw), ":")
		// Malformed credentials must fail without echoing their encoded or decoded contents.
		if err != nil || !ok {
			return Credential{}, false, failure("Docker login contains invalid encoded credentials")
		}
		c.Username, c.Password, c.Auth = username, strings.TrimRight(password, "\x00"), ""
	}
	return c, c.Username != "" || c.Password != "" || c.IdentityToken != "" || c.RegistryToken != "", nil
}
