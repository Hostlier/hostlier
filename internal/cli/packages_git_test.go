package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/gitpackages"
	"hostlier/internal/packageexec"
)

// cliGitFixture commits a catalog and real Linux ARM64 executable for end-to-end CLI routing tests.
func cliGitFixture(t *testing.T) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "package")
	build := exec.Command("go", "build", "-o", binary, "../packageexec/testdata/nativefixture")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
	// Server-target builds must not depend on the CLI host's native architecture.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	data, err := os.ReadFile(binary)
	// A real binary is required to exercise both transport and executable-format checks.
	if err != nil {
		t.Fatal(err)
	}
	manifest := gitpackages.Manifest{Version: 1, Packages: []gitpackages.Package{{Name: "postgres", Artifacts: []gitpackages.Artifact{{OS: "linux", Arch: "arm64", Path: "package", SHA256: packageexec.Digest(data)}}}}}
	raw, _ := json.Marshal(manifest)
	// The catalog is a tracked source file rather than provider API metadata.
	if err = os.WriteFile(filepath.Join(root, "hostlier-packages.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{{"init", "-q"}, {"config", "user.name", "CLI Test"}, {"config", "user.email", "cli@example.invalid"}, {"add", "hostlier-packages.json", "package"}, {"commit", "-qm", "fixture"}, {"tag", "v1"}}
	// Repository setup uses explicit Git argument arrays, never shell interpolation.
	for _, args := range commands {
		command := exec.Command("git", args...)
		command.Dir = root
		// Fail fixture construction before testing production source operations.
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	return (&url.URL{Scheme: "file", Path: root}).String(), data
}

// TestGitRepositoryDiscoveryAndRemoteInstall exercises provider-independent Git catalogs through actual CLI commands.
func TestGitRepositoryDiscoveryAndRemoteInstall(t *testing.T) {
	source, data := cliGitFixture(t)
	options := Options{Home: t.TempDir()}
	output, err := invoke([]string{"package", "available", "--repository", source}, "", options)
	// Discovery uses Git HEAD without any GitHub API fixture or controller connection.
	if err != nil {
		t.Fatal(err)
	}
	var catalog packageexec.Catalog
	// The returned commit can be copied directly into a subsequent pinned install command.
	if json.Unmarshal([]byte(output), &catalog) != nil || len(catalog.Packages) != 1 || len(catalog.Version) != 40 {
		t.Fatal(output)
	}
	remote, uploaded := packageRemoteFixture(t, data)
	options.Client = remote
	output, err = invoke([]string{"package", "install", "postgres", "--repository", source, "--version", catalog.Version, "--server", "production"}, "", options)
	// The source-neutral transfer preserves controller approval and artifact identity checks.
	if err != nil || uploaded.Len() != len(data) || !strings.Contains(output, packageexec.Digest(data)) {
		t.Fatal(err, output)
	}
	request := remote.approval.pending.body.(packageexec.InstallRequest)
	// The approved provenance must retain the Git URL and exact source commit.
	if request.Source.Repository != source || request.Source.Version != catalog.Version {
		t.Fatal(request.Source)
	}
}

// TestRepositoryArtifactAuthenticationIsScoped prevents a catalog from exporting source credentials to arbitrary hosts.
func TestRepositoryArtifactAuthenticationIsScoped(t *testing.T) {
	cases := []struct {
		source        string
		authenticated bool
	}{
		{"https://api.github.com/repos/Usefused/hostlier-packages/releases/assets/123", true},
		{"https://api.github.com/repos/Hostlier/hostlier-packages/releases/assets/123", true},
		{"https://api.github.com/repos/another/packages/releases/assets/123", false},
		{"https://api.github.com/user", false},
		{"https://artifacts.example.test/package", false},
		{"https://api.github.com.example.test/repos/Usefused/hostlier-packages/releases/assets/123", false},
	}
	// Only exact asset endpoints for the selected source repository can receive the local GitHub token.
	for _, tc := range cases {
		transport := repositoryArtifactTransport{repository: "Hostlier/hostlier-packages", token: "fixture-secret"}
		// Assert the actual outgoing request rather than only the path predicate.
		transport.base = packageTransport(func(r *http.Request) (*http.Response, error) {
			// Generic artifact sources must never inherit GitHub credentials.
			if (r.Header.Get("Authorization") == "Bearer fixture-secret") != tc.authenticated {
				t.Fatal("incorrect credential scope", tc.source)
			}
			// A pinned old catalog is sent directly to its current owner without a redirect.
			if tc.authenticated && r.URL.Path != "/repos/Hostlier/hostlier-packages/releases/assets/123" {
				t.Fatal("old asset URL was not canonicalized")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		request, _ := http.NewRequestWithContext(context.Background(), "GET", tc.source, nil)
		response, err := transport.RoundTrip(request)
		// Source headers remain unchanged after credential injection into the private request copy.
		if err != nil || request.Header.Get("Authorization") != "" || request.URL.String() != tc.source {
			t.Fatal(err)
		}
		response.Body.Close()
	}
}

// TestGitSourceFlagsRejectAmbiguousInstallIntent prevents URL and provider-coordinate mixing.
func TestGitSourceFlagsRejectAmbiguousInstallIntent(t *testing.T) {
	cases := [][]string{
		{"package", "available", "--repository", "https://git.example.test/repo.git", "--github", "owner/repo"},
		{"package", "install", "postgres", "--repository", "https://git.example.test/repo.git"},
		{"package", "install", "postgres", "--repository", "https://git.example.test/repo.git", "--file", "binary", "--version", "v1"},
	}
	// Invalid intent must fail before invoking Git, downloading or contacting a controller.
	for _, args := range cases {
		_, err := invoke(args, "", Options{Home: t.TempDir()})
		// No conflicting source selection can become an implicit local installation.
		if err == nil {
			t.Fatal("ambiguous flags accepted", args)
		}
	}
}
