package dockerlogin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loginFixture creates an isolated login home without consulting real Docker credentials.
func loginFixture(t *testing.T, config string) *Source {
	t.Helper()
	t.Setenv("SUDO_USER", "")
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	source, err := Discover()
	// Tests require a valid local identity before exercising process credential handling.
	if err != nil {
		t.Fatal(err)
	}
	source.Home = t.TempDir()
	writeLogin(t, source, config)
	return source
}

// writeLogin simulates the operator's Docker CLI replacing its own config between requests.
func writeLogin(t *testing.T, s *Source, config string) {
	t.Helper()
	// Never write fixtures to a developer's actual Docker directory.
	if err := os.WriteFile(filepath.Join(s.Directory, "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestInlineLoginReloadAndAliases verifies current credentials, isolation, identity tokens and read-only access.
func TestInlineLoginReloadAndAliases(t *testing.T) {
	source := loginFixture(t, `{"auths":{"https://index.docker.io/v1/":{"auth":"am9lOnRva2VuOndpdGg6Y29sb24="},"ghcr.io":{"identitytoken":"refresh"}}}`)
	before, _ := os.ReadFile(filepath.Join(source.Directory, "config.json"))
	// All Hub image spellings must select the same Docker CLI login.
	for _, image := range []string{"nginx:1", "joe/private:1", "docker.io/joe/private:1", "registry-1.docker.io/joe/private:1", "index.docker.io/joe/private:1"} {
		c, ok, err := source.Lookup(context.Background(), Registry(image))
		// Password colons must survive decoding and Hub helpers require the legacy index URL.
		if err != nil || !ok || c.Username != "joe" || c.Password != "token:with:colon" || c.ServerAddress != hub {
			t.Fatalf("lookup failed for %s", image)
		}
	}
	assertInlineLoginState(t, source, before)
}

// assertInlineLoginState checks isolation, token preservation and live account rotation.
func assertInlineLoginState(t *testing.T, source *Source, before []byte) {
	t.Helper()
	c, ok, err := source.Lookup(context.Background(), "ghcr.io")
	// Device-login identity tokens must remain distinct from username/password authentication.
	if err != nil || !ok || c.IdentityToken != "refresh" {
		t.Fatal("identity token lost")
	}
	_, ok, err = source.Lookup(context.Background(), "ghcr.io.attacker.test")
	// Registry credentials must not leak to similarly named registries.
	if err != nil || ok {
		t.Fatal("registry authority isolation failed")
	}
	after, _ := os.ReadFile(filepath.Join(source.Directory, "config.json"))
	// Reads preserve the original bytes exactly.
	if string(before) != string(after) {
		t.Fatal("Docker config changed")
	}
	assertLoginRotation(t, source)
}

// assertLoginRotation proves no credential remains cached after the operator changes their login.
func assertLoginRotation(t *testing.T, source *Source) {
	t.Helper()
	writeLogin(t, source, `{"auths":{"ghcr.io":{"username":"new-user","password":"rotated"}}}`)
	c, ok, err := source.Lookup(context.Background(), "ghcr.io")
	// A fresh lookup must observe rotation without restarting the controller.
	if err != nil || !ok || c.Password != "rotated" {
		t.Fatal("login rotation not observed")
	}
	writeLogin(t, source, `{"auths":{}}`)
	_, ok, err = source.Lookup(context.Background(), "ghcr.io")
	// Logout must remove authority immediately rather than retain a cached password.
	if err != nil || ok {
		t.Fatal("logout not observed")
	}
}

// TestLoginFailuresAreRedacted checks invalid configs and helper responses without exposing their contents.
func TestLoginFailuresAreRedacted(t *testing.T) {
	source := loginFixture(t, `{"auths":{"ghcr.io":{"auth":"private-secret-not-base64"}}}`)
	assertLoginFailure(t, source)
	writeLogin(t, source, `{"private-secret":`)
	assertLoginFailure(t, source)
	writeLogin(t, source, `{"credsStore":"../../private-secret"}`)
	assertLoginFailure(t, source)
	writeLogin(t, source, `{"credsStore":"nonexistent-private-secret"}`)
	assertLoginFailure(t, source)
}

// assertLoginFailure verifies errors remain useful without returning secret-bearing config or helper output.
func assertLoginFailure(t *testing.T, s *Source) {
	t.Helper()
	_, _, err := s.Lookup(context.Background(), "ghcr.io")
	// Failure must not downgrade to anonymous access or reveal raw diagnostic content.
	if err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("credential failure was absent or not redacted")
	}
}

// installHelper creates a real executable implementing the Docker helper stdin/stdout protocol.
func installHelper(t *testing.T, s *Source, name, body string) {
	t.Helper()
	directory := filepath.Join(s.Home, ".local/bin")
	// The fake helper lives only in the isolated account's test search directory.
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	// Shell fixtures are executed as programs, never interpolated into a production shell command.
	if err := os.WriteFile(filepath.Join(directory, "docker-credential-"+name), []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
}

// TestHelperSelectionAndProtocol verifies precedence, get-only calls, identity tokens and no stale fallback.
func TestHelperSelectionAndProtocol(t *testing.T) {
	source := loginFixture(t, `{"credHelpers":{"ghcr.io":"specific"},"credsStore":"global","auths":{"ghcr.io":{"username":"stale","password":"stale"}}}`)
	installHelper(t, source, "specific", `[ "$1" = get ] || exit 2
server=$(cat)
[ "$server" = ghcr.io ] || exit 3
[ -d "$HOME/.local/bin" ] || exit 4
printf '{"Username":"specific","Secret":"fresh"}'
`)
	installHelper(t, source, "global", `[ "$1" = get ] || exit 2
server=$(cat)
[ "$server" = https://index.docker.io/v1/ ] || exit 3
printf '{"Username":"<token>","Secret":"identity"}'
`)
	c, ok, err := source.Lookup(context.Background(), "ghcr.io")
	// Registry-specific helpers supersede the global store and stale inline values.
	if err != nil || !ok || c.Username != "specific" || c.Password != "fresh" {
		t.Fatal("specific helper not used", err)
	}
	c, ok, err = source.Lookup(context.Background(), "docker.io")
	// Hub's helper address and token convention must match Docker's protocol.
	if err != nil || !ok || c.IdentityToken != "identity" || c.Password != "" {
		t.Fatal("Hub helper token not used", err)
	}
	assertHelperFailureBehavior(t, source)
}

// assertHelperFailureBehavior distinguishes a logged-out helper from a broken store.
func assertHelperFailureBehavior(t *testing.T, source *Source) {
	t.Helper()
	installHelper(t, source, "specific", "echo 'credentials not found in native keychain'\nexit 1\n")
	_, ok, err := source.Lookup(context.Background(), "ghcr.io")
	// A missing helper entry must not reuse stale inline credentials.
	if err != nil || ok {
		t.Fatal("helper miss reused stale login")
	}
	installHelper(t, source, "specific", "echo private-secret\necho private-secret >&2\nexit 1\n")
	assertLoginFailure(t, source)
}

// TestHelperCancellationBoundsOutput verifies an interactive or broken helper cannot stall deployments.
func TestHelperCancellationBoundsOutput(t *testing.T) {
	source := loginFixture(t, `{"credsStore":"blocked"}`)
	installHelper(t, source, "blocked", "sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := source.Lookup(ctx, "ghcr.io")
	// Cancellation must bound subprocess and inherited output-pipe waits.
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatal("helper cancellation failed")
	}
	installHelper(t, source, "blocked", "head -c 70000 /dev/zero\n")
	assertLoginFailure(t, source)
}

// TestSourceMetadataContainsNoCredential checks that installation records retain identity only.
func TestSourceMetadataContainsNoCredential(t *testing.T) {
	source := loginFixture(t, `{"auths":{"ghcr.io":{"password":"private-secret"}}}`)
	raw, _ := json.Marshal(source)
	// Serializing installer metadata must never read or persist registry secrets.
	if strings.Contains(string(raw), "private-secret") {
		t.Fatal("source metadata contains credentials")
	}
	// Explicit Docker config locations remain separate from HOME even under a service context.
	if source.Directory != os.Getenv("DOCKER_CONFIG") {
		t.Fatal("DOCKER_CONFIG not retained")
	}
}

// TestRootHelperDropsPrivileges exercises the actual UID transition in a disposable Linux container.
func TestRootHelperDropsPrivileges(t *testing.T) {
	// Ordinary developer sessions cannot change process credentials.
	if os.Geteuid() != 0 {
		t.Skip("requires disposable root test environment")
	}
	directory, err := os.MkdirTemp("/tmp", "hostlier-login-uid-")
	// A direct /tmp child avoids inaccessible root-owned test-runner ancestors.
	if err != nil {
		t.Fatal(err)
	}
	// Remove only the temporary login tree created by this test.
	t.Cleanup(func() { os.RemoveAll(directory) })
	source := &Source{Username: "nobody", UID: "65534", GID: "65534", Home: directory, Directory: directory}
	writeLogin(t, source, `{"credsStore":"identity"}`)
	installHelper(t, source, "identity", `[ "$1" = get ] || exit 2
printf '{"Username":"%s","Secret":"fixture"}' "$(id -u)"
`)
	// The selected user owns their executable, config and home, as on an installed server.
	err = filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		// Stop if any fixture path could not be inspected.
		if err != nil {
			return err
		}
		return os.Chown(path, 65534, 65534)
	})
	// All helper paths must be accessible under the intended identity.
	if err != nil {
		t.Fatal(err)
	}
	c, ok, err := source.Lookup(context.Background(), "ghcr.io")
	// The root controller must never run a user-selected helper with UID zero.
	if err != nil || !ok || c.Username != "65534" {
		t.Fatal("helper did not drop root authority", err)
	}
}
