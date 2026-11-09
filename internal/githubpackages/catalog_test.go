package githubpackages

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"hostlier/internal/packageexec"
)

// catalogTestAssets mixes usable artifacts with malformed or unsupported release attachments.
func catalogTestAssets() []asset {
	digest := "sha256:" + strings.Repeat("a", 64)
	return []asset{
		{ID: 1, Name: "hostlier-postgres-linux-arm64", Size: 100, Digest: digest},
		{ID: 2, Name: "hostlier-postgres-darwin-amd64", Size: 100, Digest: digest},
		{ID: 3, Name: "hostlier-redis-linux-amd64", Size: 100},
		{ID: 4, Name: "SHA256SUMS", Size: 100},
		{ID: 5, Name: "source.zip", Size: 100, Digest: digest},
		{ID: 6, Name: "hostlier-unsupported-windows-amd64", Size: 100, Digest: digest},
		{ID: 7, Name: "hostlier-empty-linux-amd64", Size: 0, Digest: digest},
		{ID: 8, Name: "hostlier-huge-linux-amd64", Size: packageexec.MaxBinaryBytes + 1, Digest: digest},
		{ID: 9, Name: "hostlier-duplicate-linux-amd64", Size: 100, Digest: digest},
		{ID: 10, Name: "hostlier-duplicate-linux-amd64", Size: 100, Digest: digest},
		{ID: 11, Name: "hostlier-no-checksum-linux-amd64", Size: 100},
		{ID: 12, Name: "hostlier-my-provider-linux-amd64", Size: 100, Digest: digest},
	}
}

// TestAvailableGroupsOnlyInstallableAssets verifies metadata-only discovery and cached checksum lookup.
func TestAvailableGroupsOnlyInstallableAssets(t *testing.T) {
	metadata := release{Tag: "v1.0.0", Assets: catalogTestAssets()}
	raw, _ := json.Marshal(metadata)
	client := New("private-test-token")
	calls := []string{}
	// The fixture permits release metadata and the checksum manifest, never executable downloads.
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		// Private access stays in the API request header.
		if r.Header.Get("Authorization") != "Bearer private-test-token" {
			t.Fatal("missing authentication")
		}
		// Exactly one checksum read serves every manifest-backed candidate.
		switch r.URL.Path {
		// Latest discovery returns a concrete version for a subsequent explicit installation.
		case "/repos/Usefused/hostlier-packages/releases/latest":
			return response(raw), nil
		// Only the Redis artifact has valid fallback integrity evidence.
		case "/repos/Usefused/hostlier-packages/releases/assets/4":
			return response([]byte(strings.Repeat("b", 64) + "  hostlier-redis-linux-amd64\n")), nil
		// No source archive or executable may be downloaded while listing.
		default:
			t.Fatalf("unexpected download %s", r.URL.Path)
			return nil, nil
		}
	})
	result, err := client.Available(context.Background(), DefaultRepository, "")
	// Valid discovery must expose the concrete release and require only two small reads.
	if err != nil || result.Version != "v1.0.0" || len(calls) != 2 {
		t.Fatal(err, result, calls)
	}
	expected := []AvailablePackage{
		{Name: "my-provider", Platforms: []packageexec.Platform{{OS: "linux", Arch: "amd64"}}},
		{Name: "postgres", Platforms: []packageexec.Platform{{OS: "darwin", Arch: "amd64"}, {OS: "linux", Arch: "arm64"}}},
		{Name: "redis", Platforms: []packageexec.Platform{{OS: "linux", Arch: "amd64"}}},
	}
	// Unsupported, ambiguous, oversized and unverified entries must not become installation suggestions.
	if !reflect.DeepEqual(result.Packages, expected) {
		t.Fatalf("unexpected catalog %#v", result)
	}
}

// TestAvailableRejectsUnpublishedAndMismatchedReleases keeps discovery aligned with pinned installation.
func TestAvailableRejectsUnpublishedAndMismatchedReleases(t *testing.T) {
	cases := []struct {
		name, version string
		metadata      release
		valid         bool
	}{
		{"explicit prerelease", "v2-beta", release{Tag: "v2-beta", Prerelease: true}, true},
		{"latest prerelease", "", release{Tag: "v2-beta", Prerelease: true}, false},
		{"draft", "v1", release{Tag: "v1", Draft: true}, false},
		{"wrong tag", "v1", release{Tag: "v2"}, false},
		{"missing tag", "", release{}, false},
		{"empty published release", "v1", release{Tag: "v1"}, true},
	}
	// Explicit releases must remain exact even when discovery accepts latest by default.
	for _, tc := range cases {
		// Run independent metadata validations with no controller or disk mutation.
		t.Run(tc.name, func(t *testing.T) {
			client := New("")
			// All cases return only metadata and require no executable download.
			client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				endpoint := "/repos/Usefused/hostlier-packages/releases/latest"
				// Explicit selection uses the tag route rather than silently consulting latest.
				if tc.version != "" {
					endpoint = "/repos/Usefused/hostlier-packages/releases/tags/" + tc.version
				}
				// Wrong API routing would allow an unintended release to appear.
				if r.URL.Path != endpoint {
					t.Fatal(r.URL.Path)
				}
				raw, _ := json.Marshal(tc.metadata)
				return response(raw), nil
			})
			result, err := client.Available(context.Background(), DefaultRepository, tc.version)
			// Empty success is represented as an empty array, not an access failure.
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
			// A published release with no artifacts should remain scriptable.
			if tc.valid && result.Packages == nil {
				t.Fatal("null package list")
			}
		})
	}
}

// TestAvailablePreservesAccessErrorsWithoutProviderBodies avoids presenting denied access as an empty catalog.
func TestAvailablePreservesAccessErrorsWithoutProviderBodies(t *testing.T) {
	client := New("private-test-token")
	// Provider diagnostics are not safe CLI output and must remain discarded.
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		out := response([]byte("private-test-token"))
		out.StatusCode = 404
		return out, nil
	})
	_, err := client.Available(context.Background(), DefaultRepository, "")
	// The status remains actionable without leaking the provider body or credentials.
	if err == nil || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "private-test-token") {
		t.Fatal(err)
	}
}
