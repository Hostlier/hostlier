package gitpackages

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hostlier/internal/packageexec"
)

// gitFixtureCommand runs explicit local fixture operations without invoking repository scripts.
func gitFixtureCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = gitEnvironment(nil)
	output, err := command.CombinedOutput()
	// Fixture construction failures must not be mistaken for expected fetch rejection.
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// writeFixture creates deterministic fixture files before committing their contents.
func writeFixture(t *testing.T, root, name string, data []byte) {
	t.Helper()
	// Fixture contents need not be executable because production reads only Git blobs.
	if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// catalogFixture creates a real Git repository with a catalog and one native package binary.
func catalogFixture(t *testing.T) (string, string, []byte) {
	t.Helper()
	root := t.TempDir()
	gitFixtureCommand(t, root, "init", "-q")
	gitFixtureCommand(t, root, "config", "user.name", "Package Test")
	gitFixtureCommand(t, root, "config", "user.email", "package@example.invalid")
	binary := filepath.Join(t.TempDir(), "package")
	build := exec.Command("go", "build", "-o", binary, "../packageexec/testdata/nativefixture")
	// Native headers verify that installation inspects bytes independently of manifest claims.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	data, err := os.ReadFile(binary)
	// The catalog integrity value must reflect actual fixture bytes.
	if err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "binary", SHA256: packageexec.Digest(data)}
	remote := artifact
	remote.Path = ""
	remote.URL = "https://artifacts.example.test/package?signature=private"
	bad := artifact
	bad.SHA256 = strings.Repeat("0", 64)
	manifest := Manifest{Version: 1, Packages: []Package{{Name: "postgres", Artifacts: []Artifact{artifact}}, {Name: "remote", Artifacts: []Artifact{remote}}, {Name: "bad", Artifacts: []Artifact{bad}}}}
	raw, _ := json.Marshal(manifest)
	writeFixture(t, root, "hostlier-packages.json", raw)
	writeFixture(t, root, "binary", data)
	gitFixtureCommand(t, root, "add", "hostlier-packages.json", "binary")
	gitFixtureCommand(t, root, "commit", "-qm", "fixture")
	gitFixtureCommand(t, root, "tag", "v1")
	return root, gitFixtureCommand(t, root, "rev-parse", "HEAD"), data
}

// testTransport provides HTTPS artifact responses without opening a listener.
type testTransport func(*http.Request) (*http.Response, error)

// RoundTrip implements isolated artifact HTTP fixtures.
func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestGitCatalogAndInstallationUsePinnedSource exercises real Git fetch, catalog discovery and native transfer.
func TestGitCatalogAndInstallationUsePinnedSource(t *testing.T) {
	root, commit, data := catalogFixture(t)
	source := (&url.URL{Scheme: "file", Path: root}).String()
	client := New()
	calls := 0
	// Artifact downloads are independently authorized and never receive Git transport credentials.
	client.HTTP.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		// Generic HTTPS distribution must not inherit authentication from the source repository.
		if r.Header.Get("Authorization") != "" {
			t.Fatal("Git credentials leaked to artifact host")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	catalog, err := client.Available(context.Background(), source, "")
	// Discovery resolves HEAD to an immutable commit without external binary downloads.
	if err != nil || catalog.Version != commit || len(catalog.Packages) != 3 || calls != 0 {
		t.Fatal(err, catalog, calls)
	}
	checkGitInstallations(t, client, source, commit, data)
	// Only the explicitly requested remote artifact should have been downloaded.
	if calls != 1 {
		t.Fatal("unexpected artifact downloads", calls)
	}
}

// checkGitInstallations checks tag/commit pinning, external binaries, missing refs and checksum failure.
func checkGitInstallations(t *testing.T, client *Client, source, commit string, data []byte) {
	t.Helper()
	target := packageexec.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	// Both tracked and separately hosted artifacts must produce the same verified bytes.
	for _, name := range []string{"postgres", "remote"} {
		result, err := client.Package(context.Background(), source, "v1", name, target)
		// Approval provenance must contain the resolved commit, never a mutable ref or signed download URL.
		if err != nil || result.Request.Source.Version != commit || !bytes.Equal(result.Data, data) {
			t.Fatal(err, result.Request)
		}
	}
	// Integrity failures cannot produce an installable request even with a valid Git commit.
	if _, err := client.Package(context.Background(), source, commit, "bad", target); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	// Unknown tags must not fall back to default HEAD.
	if _, err := client.Available(context.Background(), source, "missing"); err == nil {
		t.Fatal("missing ref silently replaced")
	}
	// Source installation always requires an explicit selection.
	if _, err := client.Package(context.Background(), source, "", "postgres", target); err == nil {
		t.Fatal("implicit install version accepted")
	}
}

// TestGitCatalogRequiresRegularManifest rejects a catalog symlink without checking out or following it.
func TestGitCatalogRequiresRegularManifest(t *testing.T) {
	root := t.TempDir()
	gitFixtureCommand(t, root, "init", "-q")
	gitFixtureCommand(t, root, "config", "user.name", "Package Test")
	gitFixtureCommand(t, root, "config", "user.email", "package@example.invalid")
	// A catalog cannot redirect reads to any local or external file.
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "hostlier-packages.json")); err != nil {
		t.Fatal(err)
	}
	gitFixtureCommand(t, root, "add", "hostlier-packages.json")
	gitFixtureCommand(t, root, "commit", "-qm", "symlink")
	_, err := New().Available(context.Background(), (&url.URL{Scheme: "file", Path: root}).String(), "")
	// Blob-mode validation must reject a symlink even though Git stores its target as blob data.
	if err == nil {
		t.Fatal("catalog symlink accepted")
	}
}

// TestGitSourceValidation rejects transport helpers and embedded secrets before executing Git.
func TestGitSourceValidation(t *testing.T) {
	// These forms cover commonly used self-hosted, GitLab, Bitbucket and local repositories.
	for _, source := range []string{"https://git.example.test/team/packages.git", "ssh://git@git.example.test/team/packages.git", "git@git.example.test:team/packages.git", "file:///tmp/packages.git"} {
		// Valid source coordinates must remain provider independent.
		if err := ValidateSelection(source, "v1"); err != nil {
			t.Fatal(source, err)
		}
	}
	// These forms cannot safely become public provenance or transport arguments.
	for _, source := range []string{"ext::sh -c command", "https://token@git.example.test/repo", "ssh://user:password@git.example.test/repo", "https://git.example.test/repo?token=secret", "-repo", "git://git.example.test/repo"} {
		// Invalid sources must fail before local credential helpers run.
		if ValidateSelection(source, "v1") == nil {
			t.Fatal("unsafe source accepted", source)
		}
	}
}

// TestManifestRejectsAmbiguityAndTraversal checks catalog interpretation separately from transport success.
func TestManifestRejectsAmbiguityAndTraversal(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := Artifact{OS: "linux", Arch: "arm64", Path: "binary", SHA256: digest}
	cases := [][]Artifact{{valid, valid}, {{OS: "linux", Arch: "arm64", Path: "../binary", SHA256: digest}}, {{OS: "linux", Arch: "arm64", URL: "http://host/binary", SHA256: digest}}, {{OS: "linux", Arch: "arm64", Path: "binary", URL: "https://host/binary", SHA256: digest}}}
	// No malformed artifact selection can become a seemingly valid available package.
	for _, artifacts := range cases {
		raw, _ := json.Marshal(Manifest{Version: 1, Packages: []Package{{Name: "postgres", Artifacts: artifacts}}})
		// The catalog must fail as a whole instead of silently choosing one ambiguous target.
		if _, err := decodeManifest(raw); err == nil {
			t.Fatal("invalid catalog accepted", string(raw))
		}
	}
}

// TestGitOutputBoundCannotBeBypassedByIOCopy guards the subprocess pipe's actual streaming interface.
func TestGitOutputBoundCannotBeBypassedByIOCopy(t *testing.T) {
	output := &boundedBuffer{limit: 8}
	reader := io.LimitReader(strings.NewReader(strings.Repeat("x", 128)), 128)
	_, err := io.Copy(output, reader)
	// Promoted Buffer.ReadFrom methods must not bypass the Write size limit.
	if err == nil || output.buffer.Len() > 8 {
		t.Fatal("subprocess output bound bypassed")
	}
}
