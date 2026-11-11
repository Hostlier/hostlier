package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"hostlier/internal/githubpackages"
	"hostlier/internal/packageexec"
)

// availableGitHubFixture serves catalog metadata while rejecting downloads and unexpected source coordinates.
func availableGitHubFixture(t *testing.T, endpoint string) *githubpackages.Client {
	t.Helper()
	client := githubpackages.New("private-test-token")
	// Digest metadata makes discovery a single GitHub request with no executable reads.
	client.HTTP.Transport = packageTransport(func(r *http.Request) (*http.Response, error) {
		// Validate source selection and private authentication independently from controller access.
		if r.URL.Path != endpoint || r.Header.Get("Authorization") != "Bearer private-test-token" {
			t.Fatal("unexpected GitHub catalog request")
		}
		metadata := map[string]any{"tag_name": "v0.1.0", "assets": []any{
			map[string]any{"id": 1, "name": "hostlier-postgres-linux-arm64", "size": 100, "digest": "sha256:" + strings.Repeat("a", 64)},
			map[string]any{"id": 2, "name": "hostlier-redis-linux-amd64", "size": 100, "digest": "sha256:" + strings.Repeat("b", 64)},
		}}
		raw, _ := json.Marshal(metadata)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	return client
}

// TestPackageAvailableAndRepositoryListAlias discover packages without a controller or local state directory.
func TestPackageAvailableAndRepositoryListAlias(t *testing.T) {
	cases := []struct {
		args     []string
		endpoint string
	}{
		{[]string{"package", "available", "--github", "Hostlier/hostlier-packages"}, "/repos/Hostlier/hostlier-packages/releases/latest"},
		{[]string{"package", "list", "--github", "other/packages", "--version", "v0.1.0"}, "/repos/other/packages/releases/tags/v0.1.0"},
	}
	// Both discovery forms use GitHub while unqualified package list retains installed inventory semantics.
	for _, tc := range cases {
		output, err := invoke(tc.args, "", Options{Home: t.TempDir(), GitHub: availableGitHubFixture(t, tc.endpoint)})
		// A valid catalog includes all advertised platforms and a concrete version for installation.
		if err != nil {
			t.Fatal(err)
		}
		var catalog githubpackages.Catalog
		// No private credentials may appear in the machine-readable result.
		if json.Unmarshal([]byte(output), &catalog) != nil || len(catalog.Packages) != 2 || strings.Contains(output, "private-test-token") {
			t.Fatal(output)
		}
	}
}

// TestPackageAvailableServerFilterOnlyReadsPlatform ensures browsing cannot trigger installation or approval.
func TestPackageAvailableServerFilterOnlyReadsPlatform(t *testing.T) {
	remote := &fakeClient{}
	// Any operation beyond platform discovery would exceed the catalog's read-only scope.
	remote.reply = func(c call) (any, error) {
		// GitHub tokens and source metadata are never sent to the controller during discovery.
		if c.method != "GET" || c.path != "/v1/packages/platform" || c.body != nil {
			t.Fatal("unexpected controller catalog request")
		}
		return packageexec.Platform{OS: "linux", Arch: "arm64"}, nil
	}
	output, err := invoke([]string{"package", "available", "--github", "Hostlier/hostlier-packages", "--server", "production"}, "", Options{Home: t.TempDir(), Client: remote, GitHub: availableGitHubFixture(t, "/repos/Hostlier/hostlier-packages/releases/latest")})
	// A failed platform request cannot be replaced with the CLI machine's own architecture.
	if err != nil {
		t.Fatal(err)
	}
	var catalog githubpackages.Catalog
	// Only the ARM64 artifact belongs in this server's available package list.
	if json.Unmarshal([]byte(output), &catalog) != nil || len(catalog.Packages) != 1 || catalog.Packages[0].Name != "postgres" {
		t.Fatal(output)
	}
	// The command must never propose a human approval or mutate controller state.
	if len(remote.calls) != 1 || remote.approval.pending.path != "" {
		t.Fatal("catalog mutated controller")
	}
}

// TestPackageAvailableRejectsConflictingArguments validates intent before discovering credentials or opening connections.
func TestPackageAvailableRejectsConflictingArguments(t *testing.T) {
	// None of these invalid requests should require GitHub or controller access to reject.
	for _, flags := range [][]string{{"postgres"}, {"--file", "binary"}, {"--state-dir", "state"}, {"--server", "prod", "--socket", "socket"}, {"--github", "../repo"}, {"--version", "../tag"}} {
		_, err := invoke(append([]string{"package", "available"}, flags...), "", Options{Home: t.TempDir()})
		// Invalid discovery options cannot quietly select installed packages or another repository.
		if err == nil {
			t.Fatal("accepted invalid arguments", flags)
		}
	}
}
