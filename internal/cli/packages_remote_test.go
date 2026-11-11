package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/githubpackages"
	"hostlier/internal/packageexec"
)

// packageTransport serves GitHub fixtures through the actual HTTP client interface.
type packageTransport func(*http.Request) (*http.Response, error)

// RoundTrip implements HTTP fixture responses.
func (f packageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRemotePackageUsesServerArchitectureAndApproval transfers a Linux executable from any CLI host.
func TestRemotePackageUsesServerArchitectureAndApproval(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "package")
	build := exec.Command("go", "build", "-o", binary, "../packageexec/testdata/nativefixture")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
	// A real cross-compiled binary distinguishes the server target from the test machine.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	data, err := os.ReadFile(binary)
	// Build output is required before exercising release transport.
	if err != nil {
		t.Fatal(err)
	}
	github := packageGitHubFixture(t, data)
	remote, uploaded := packageRemoteFixture(t, data)
	output, err := invoke([]string{"package", "install", "postgres", "--github", "Hostlier/hostlier-packages", "--version", "v0.1.0", "--server", "production"}, "", Options{Home: root, Client: remote, GitHub: github})
	// The CLI must wait for the simulated independent approval and print the installed identity.
	if err != nil || !strings.Contains(output, packageexec.Digest(data)) {
		t.Fatal(err, output)
	}
	// The retained bytes and pending exact request must match the requested artifact.
	if !bytes.Equal(uploaded.Bytes(), data) || remote.approval.pending.path != "/v1/packages" {
		t.Fatal("incorrect transfer or approval")
	}
	checkRemotePlatformRejection(t, root, binary)
}

// packageGitHubFixture verifies release selection uses the reported Linux arm64 platform.
func packageGitHubFixture(t *testing.T, data []byte) *githubpackages.Client {
	t.Helper()
	client := githubpackages.New("private-test-token")
	// Only the exact server-platform release asset is available in this repository fixture.
	client.HTTP.Transport = packageTransport(func(r *http.Request) (*http.Response, error) {
		var raw []byte
		// Wrong tags or asset identifiers cannot silently download another binary.
		switch r.URL.Path {
		// Supply an arm64 artifact even when the CLI runs on Darwin or amd64.
		case "/repos/Hostlier/hostlier-packages/releases/tags/v0.1.0":
			raw, _ = json.Marshal(map[string]any{"tag_name": "v0.1.0", "assets": []any{map[string]any{"id": 1, "name": "hostlier-postgres-linux-arm64", "size": len(data), "digest": "sha256:" + packageexec.Digest(data)}}})
		// Asset bytes are downloaded by GitHub API identity.
		case "/repos/Hostlier/hostlier-packages/releases/assets/1":
			raw = data
		// Extra lookups indicate incorrect release selection.
		default:
			t.Fatalf("unexpected GitHub path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})
	return client
}

// packageRemoteFixture records bounded chunks and exposes only the server platform and approved installation.
func packageRemoteFixture(t *testing.T, data []byte) (*fakeClient, *bytes.Buffer) {
	t.Helper()
	var uploaded bytes.Buffer
	remote := &fakeClient{}
	// The approval fixture executes only after the CLI proposes its retained exact intent.
	remote.reply = func(c call) (any, error) {
		raw, _ := json.Marshal(c.body)
		// GitHub credentials and local paths must never cross the controller transport.
		if bytes.Contains(raw, []byte("private-test-token")) {
			t.Fatal("credential sent to server")
		}
		// Preparation and activation have separate endpoints and permissions.
		switch {
		// Platform discovery precedes asset selection.
		case c.path == "/v1/packages/platform":
			return packageexec.Platform{OS: "linux", Arch: "arm64"}, nil
		// Chunks must arrive in order with bounded sizes.
		case strings.HasPrefix(c.path, "/v1/package-uploads/"):
			body := c.body.(map[string]any)
			chunk := body["data"].([]byte)
			// Invalid cursor or unbounded transfer would make retries unsafe.
			if body["offset"].(int) != uploaded.Len() || len(chunk) > packageexec.ChunkBytes {
				t.Fatal("invalid upload chunk")
			}
			uploaded.Write(chunk)
			return map[string]int{"offset": uploaded.Len()}, nil
		// The proposal retains typed integrity metadata until execution.
		case c.path == "/v1/packages":
			request := c.body.(packageexec.InstallRequest)
			return packageexec.Installation{Name: request.Name, Digest: request.Digest}, nil
		}
		return nil, fmt.Errorf("unexpected controller path %s", c.path)
	}
	return remote, &uploaded
}

// checkRemotePlatformRejection ensures local-file transfers also respect the server platform.
func checkRemotePlatformRejection(t *testing.T, home, binary string) {
	t.Helper()
	remote := &fakeClient{}
	// A different server architecture must abort before uploading any bytes.
	remote.reply = func(c call) (any, error) { return packageexec.Platform{OS: "linux", Arch: "amd64"}, nil }
	_, err := invoke([]string{"package", "install", "postgres", "--file", binary, "--server", "other"}, "", Options{Home: home, Client: remote})
	// Exactly the read-only platform discovery should have reached the controller.
	if err == nil || len(remote.calls) != 1 || remote.approval.pending.path != "" {
		t.Fatal("wrong platform uploaded", err)
	}
}

// TestSDKCommandResolvesCommitBeforeInstalling pins the named ref and routes only to a local Go project.
func TestSDKCommandResolvesCommitBeforeInstalling(t *testing.T) {
	root := t.TempDir()
	sha := strings.Repeat("a", 40)
	github := githubpackages.New("test-token")
	// Resolve the tag through the same API used in production.
	github.HTTP.Transport = packageTransport(func(r *http.Request) (*http.Response, error) {
		// SDK setup cannot resolve another repository or an implicit default branch.
		if r.URL.Path != "/repos/Hostlier/hostlier-packages/commits/v0.1.0" {
			t.Fatal(r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"sha":"` + sha + `"}`))}, nil
	})
	called := false
	options := Options{Home: root, CWD: root, GitHub: github}
	// Observe the immutable revision and project location passed to the Go installer.
	options.InstallSDK = func(_ context.Context, directory, commit, token string) error {
		called = true
		// Relative directories resolve against the invocation rather than global CLI state.
		if directory != filepath.Join(root, "project") || commit != sha || token != "test-token" {
			t.Fatal("incorrect SDK destination")
		}
		return nil
	}
	_, err := invoke([]string{"sdk", "install", "--version", "v0.1.0", "--directory", "project"}, "", options)
	// Successful resolution must invoke exactly the selected local dependency installer.
	if err != nil || !called {
		t.Fatal(err)
	}
}
