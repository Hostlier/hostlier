package githubpackages

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"

	"hostlier/internal/packageexec"
)

// roundTripFunc injects HTTP responses while preserving the production redirect machinery.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements the standard HTTP transport contract.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// response creates an independent response body for each request or retry.
func response(data []byte) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}
}

// TestReleaseSelectionIntegrityAndPlatform exercises the production download and native-header pipeline.
func TestReleaseSelectionIntegrityAndPlatform(t *testing.T) {
	path, err := os.Executable()
	// The running test executable provides a real native format without running downloaded content.
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	// Fixture read failures must not masquerade as checksum failures.
	if err != nil {
		t.Fatal(err)
	}
	target := packageexec.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	filename := "hostlier-postgres-" + target.OS + "-" + target.Arch
	cases := []releaseCase{
		{"metadata", "sha256:" + packageexec.Digest(data), int64(len(data)), "", target, true},
		{"manifest", "", int64(len(data)), packageexec.Digest(data) + "  " + filename + "\n", target, true},
		{"checksum mismatch", "sha256:" + strings.Repeat("0", 64), int64(len(data)), "", target, false},
		{"truncation", "sha256:" + packageexec.Digest(data), int64(len(data)) + 1, "", target, false},
		{"missing integrity", "", int64(len(data)), "", target, false},
		{"wrong architecture", "sha256:" + packageexec.Digest(data), int64(len(data)), "", packageexec.Platform{OS: target.OS, Arch: map[string]string{"amd64": "arm64", "arm64": "amd64"}[target.Arch]}, false},
	}
	// Each malformed source must fail before a usable download is returned.
	for _, tc := range cases {
		// Each subtest checks one independent integrity or platform constraint.
		t.Run(tc.name, func(t *testing.T) {
			checkReleaseCase(t, tc, data)
		})
	}
}

// TestRedirectNeverForwardsCredentials exercises net/http redirects, including same-host redirects.
func TestRedirectNeverForwardsCredentials(t *testing.T) {
	// Both API-origin and cross-origin redirects must drop the bearer token.
	for _, destination := range []string{"https://api.github.com/asset", "https://assets.example.test/asset", "http://assets.example.test/asset"} {
		client := New("private-test-token")
		calls := 0
		// Exercise net/http redirect handling with a deterministic transport.
		client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			// The initial trusted API request redirects to a signed asset location.
			if calls == 1 {
				out := response(nil)
				out.StatusCode = 302
				out.Header.Set("Location", destination)
				return out, nil
			}
			// No destination, including the original origin, receives API credentials after redirection.
			if r.Header.Get("Authorization") != "" {
				t.Fatal("credential forwarded")
			}
			return response([]byte("asset")), nil
		})
		_, err := client.read(context.Background(), "/start", "application/octet-stream", 100)
		// HTTP downgrades fail before issuing the redirected request.
		if strings.HasPrefix(destination, "http:") {
			// An insecure redirect must be rejected without contacting its destination.
			if err == nil || calls != 1 {
				t.Fatal("insecure redirect accepted")
			}
			// HTTPS asset downloads should finish after exactly one redirect.
		} else if err != nil || calls != 2 {
			t.Fatal(err, calls)
		}
	}
}

// TestInvalidSourceAndAmbiguousChecksum rejects traversal and nonunique integrity identities.
func TestInvalidSourceAndAmbiguousChecksum(t *testing.T) {
	// Malformed coordinates cannot become paths or module arguments.
	for _, pair := range [][2]string{{"https://github.com/a/b", "v1"}, {"a/../b", "v1"}, {"a/b", "../main"}, {"a/b", ""}, {"a/b", "--flag"}} {
		// All invalid values fail without network access.
		if ValidateSelection(pair[0], pair[1]) == nil {
			t.Fatal("accepted", pair)
		}
	}
	line := strings.Repeat("a", 64) + "  package\n"
	// Repeated checksum entries are ambiguous even when their contents match.
	if _, err := checksumFor(line+line, "package"); err == nil {
		t.Fatal("duplicate checksum accepted")
	}
	// Duplicate release filenames must not depend on ordering.
	if _, err := findAsset([]asset{{ID: 1, Name: "package"}, {ID: 2, Name: "package"}}, "package"); err == nil {
		t.Fatal("duplicate asset accepted")
	}
}

// releaseCase describes one published asset and the expected verification result.
type releaseCase struct {
	name, digest string
	size         int64
	manifest     string
	target       packageexec.Platform
	valid        bool
}

// checkReleaseCase exercises real download selection with a controlled provider response.
func checkReleaseCase(t *testing.T, tc releaseCase, data []byte) {
	t.Helper()
	metadata := release{Tag: "v1.0.0", Assets: []asset{{ID: 1, Name: "hostlier-postgres-" + tc.target.OS + "-" + tc.target.Arch, Size: tc.size, Digest: tc.digest}}}
	// Older releases supply a separately named checksum manifest.
	if tc.manifest != "" {
		metadata.Assets = append(metadata.Assets, asset{ID: 2, Name: "SHA256SUMS", Size: int64(len(tc.manifest))})
	}
	raw, _ := json.Marshal(metadata)
	client := New("private-test-token")
	// Keep response dispatch separate from assertions about downloaded content.
	client.HTTP.Transport = releaseTransport(t, raw, data, tc.manifest)

	downloaded, err := client.Package(context.Background(), DefaultRepository, "v1.0.0", "postgres", tc.target)
	// Only exact integrity and actual native platform matches can succeed.
	if (err == nil) != tc.valid {
		t.Fatal("unexpected download result", err)
	}
	// Provenance identifies the reviewed asset without exposing credentials.
	if tc.valid && (!bytes.Equal(downloaded.Data, data) || downloaded.Request.Source.Repository != DefaultRepository) {
		t.Fatal("incorrect verified result")
	}
}

// releaseTransport verifies API authentication before serving deterministic release responses.
func releaseTransport(t *testing.T, metadata, data []byte, manifest string) roundTripFunc {
	t.Helper()
	// All authenticated requests must remain on the trusted API origin.
	return func(r *http.Request) (*http.Response, error) {
		// Source credentials are required for this private repository fixture.
		if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer private-test-token" {
			t.Fatal("incorrect API authentication")
		}
		// Only API asset identities determine the downloaded content.
		switch r.URL.Path {
		// Return the explicit release tag metadata.
		case "/repos/Hostlier/hostlier-packages/releases/tags/v1.0.0":
			return response(metadata), nil
		// Return the selected native executable bytes.
		case "/repos/Hostlier/hostlier-packages/releases/assets/1":
			return response(data), nil
		// Return the older-style checksum manifest.
		case "/repos/Hostlier/hostlier-packages/releases/assets/2":
			return response([]byte(manifest)), nil
		// Unexpected paths indicate an incorrect source selection.
		default:
			t.Fatalf("unexpected API path %s", r.URL.Path)
			return nil, nil
		}
	}
}
