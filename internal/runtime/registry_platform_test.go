package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hostlier/internal/dockerlogin"
	"hostlier/internal/model"
	"io"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"strings"
	"testing"
)

// fixtureDigest addresses the same bytes served by the test registry.
func fixtureDigest(body string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body))) }

// fixtureConfig matches the controller platform on any test host.
func fixtureConfig() string { return fmt.Sprintf(`{"os":"linux","architecture":%q}`, goruntime.GOARCH) }

// fixtureManifest points at the verified local config.
func fixtureManifest() string { return fixtureManifestFor(fixtureConfig()) }

// fixtureManifestFor produces the same wire representation for compatible and incompatible cases.
func fixtureManifestFor(config string) string {
	return fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q}}`, fixtureDigest(config))
}

// serveImageFixture serves the config or manifest requested by the transport being exercised.
func serveImageFixture(w http.ResponseWriter, r *http.Request) {
	body := fixtureManifest()
	// Registry config blobs have their own immutable URL.
	if strings.Contains(r.URL.Path, "/blobs/") {
		body = fixtureConfig()
	}
	w.Header().Set("Docker-Content-Digest", fixtureDigest(body))
	fmt.Fprint(w, body)
}

// TestControllerImagePlatform exercises the shared Docker and K3s admission path for pinned inputs.
func TestControllerImagePlatform(t *testing.T) {
	other := "amd64"
	// Exercise the architecture opposite the test machine.
	if goruntime.GOARCH == other {
		other = "arm64"
	}
	wrongConfig := fmt.Sprintf(`{"os":"linux","architecture":%q}`, other)
	wrong := fixtureManifestFor(wrongConfig)
	// Label each descriptor as an OCI registry would.
	entry := func(arch, digest string) map[string]any {
		return map[string]any{"digest": digest, "platform": map[string]string{"os": "linux", "architecture": arch}}
	}
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{entry(other, fixtureDigest(wrong)), entry(goruntime.GOARCH, fixtureDigest(fixtureManifest()))}})
	bodies := map[string]string{}
	// Serve each immutable object only by its actual digest.
	for _, body := range []string{fixtureConfig(), fixtureManifest(), wrongConfig, wrong, string(index)} {
		bodies[fixtureDigest(body)] = body
	}
	// Keep registry identity and TLS trust isolated to the test.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, bodies[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]])
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "https://")
	r := registryLoginFixture(t, host, dockerlogin.Credential{})
	r.web = server.Client()
	// Both runtime implementations must enforce the same rule.
	for _, backend := range []model.Runtime{r, &K3s{base: r}} {
		ref := host + "/app@"
		got, err := backend.InspectImage(context.Background(), ref+fixtureDigest(string(index)))
		// The approval must name the selected child, even when the request pins an index.
		if err != nil || got.Digest != ref+fixtureDigest(fixtureManifest()) {
			t.Fatal(backend.Name(), got, err)
		}
		// A direct pin cannot bypass config validation.
		if _, err = backend.InspectImage(context.Background(), ref+fixtureDigest(wrong)); err == nil || !strings.Contains(err.Error(), "controller requires linux/") {
			t.Fatal(backend.Name(), err)
		}
	}
}

// TestMutableTagRefreshesRegistry ensures a cached Docker image cannot hide a
// newly published tag or mask a registry failure with stale content.
func TestMutableTagRefreshesRegistry(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(serveImageFixture))
	defer server.Close()
	authority := strings.TrimPrefix(server.URL, "https://")
	r := registryLoginFixture(t, authority, dockerlogin.Credential{})
	r.web = server.Client()
	// Model Docker holding an older manifest for this repository.
	r.docker = &http.Client{Transport: sidecarTransport(func(*http.Request) (*http.Response, error) {
		local := fmt.Sprintf(`{"Os":"linux","Architecture":%q,"RepoDigests":[%q]}`,
			goruntime.GOARCH, authority+"/app@sha256:"+strings.Repeat("a", 64))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(local))}, nil
	})}
	image := authority + "/app:latest"
	got, err := r.InspectImage(context.Background(), image)
	// Require a fresh registry resolution with no local-cache warning.
	if err != nil || got.Digest != authority+"/app@"+fixtureDigest(fixtureManifest()) || len(got.Warnings) != 0 {
		t.Fatalf("mutable tag did not use registry: %+v, %v", got, err)
	}
	// A failed registry lookup must not turn the old cache into an approval.
	r.web = &http.Client{Transport: sidecarTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	// Reject a stale fallback when the registry is unavailable.
	if _, err := r.InspectImage(context.Background(), image); err == nil {
		t.Fatal("unavailable registry accepted a stale local tag")
	}
}
