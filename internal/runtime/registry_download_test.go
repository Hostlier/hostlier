package runtime

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"hostlier/internal/artifact"
	"hostlier/internal/dockerlogin"
)

// registryDownloadFixture provides an authenticated registry that can fail after successful metadata resolution.
type registryDownloadFixture struct {
	backend              http.Handler
	server               *httptest.Server
	private, refuseLayer atomic.Bool
	layerReads           atomic.Int64
	layerDigest          string
}

// newRegistryDownloadFixture publishes real bytes before requiring the recorded server-account login.
func newRegistryDownloadFixture(t *testing.T) (*registryDownloadFixture, *Runtime, name.Tag) {
	t.Helper()
	f := &registryDownloadFixture{backend: registry.New(registry.Logger(log.New(io.Discard, "", 0)))}
	image := registryDownloadImage(t)
	layers, _ := image.Layers()
	layerDigest, _ := layers[0].Digest()
	f.layerDigest = layerDigest.String()
	f.server = httptest.NewTLSServer(f)
	t.Cleanup(f.server.Close)
	ref, _ := name.NewTag(strings.TrimPrefix(f.server.URL, "https://") + "/team/private:latest")
	if err := remote.Write(ref, image, remote.WithTransport(f.server.Client().Transport)); err != nil { // Publish actual config and layer bytes through the registry API before enabling authentication.
		t.Fatal(err)
	}
	f.private.Store(true)
	r := registryLoginFixture(t, ref.RegistryStr(), dockerlogin.Credential{Username: "alice", Password: "server-secret"})
	r.web = f.server.Client()
	return f, r, ref
}

// ServeHTTP independently enforces token delivery, content access and optional layer failure.
func (f *registryDownloadFixture) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if f.private.Load() { // Original server credentials are needed only after fixture publication.
		if request.URL.Path == "/token" { // Passwords go only to this approved TLS origin.
			f.exchangeToken(w, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer server-access" { // All content requires an exchanged read-only token.
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+f.server.URL+`/token",service="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Method == "GET" && strings.HasSuffix(request.URL.Path, f.layerDigest) { // Metadata-only resolution cannot satisfy a full download.
			f.layerReads.Add(1)
			if f.refuseLayer.Load() { // Lose blob access after metadata resolution succeeds.
				http.Error(w, "private provider diagnostic server-secret", http.StatusForbidden)
				return
			}
		}
	}
	f.backend.ServeHTTP(w, request)
}

// exchangeToken verifies the server login and exact pull-only repository scope.
func (f *registryDownloadFixture) exchangeToken(w http.ResponseWriter, request *http.Request) {
	user, password, ok := request.BasicAuth()
	if !ok || user != "alice" || password != "server-secret" || request.URL.Query().Get("scope") != "repository:team/private:pull" { // Laptop credentials and broader scopes cannot authorize a server-first pull.
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	io.WriteString(w, `{"token":"server-access"}`)
}

// TestPrepareRegistryImageDownloadsLayers uses a real private TLS registry and the recorded server-account login.
func TestPrepareRegistryImageDownloadsLayers(t *testing.T) {
	f, r, ref := newRegistryDownloadFixture(t)
	receipt, err := r.PrepareRegistryImage(context.Background(), ref.Name(), t.TempDir())
	if err != nil { // Server credentials must be sufficient to stage the whole image without a Docker daemon.
		t.Fatal(err)
	}
	if _, err := artifact.ContainerImage(receipt.Path, runtime.GOARCH); err != nil { // The output is a verified portable image for the receiving controller.
		t.Fatal(err)
	}
	if f.layerReads.Load() == 0 { // Metadata-only resolution cannot satisfy the server-first download contract.
		t.Fatal("preparation never downloaded the filesystem layer")
	}
	f.refuseLayer.Store(true)
	_, err = r.PrepareRegistryImage(context.Background(), ref.Name(), t.TempDir())
	if err == nil || strings.Contains(err.Error(), "server-secret") { // Blob access failure is eligible for CLI fallback without disclosing registry responses.
		t.Fatal("layer failure accepted or leaked diagnostics", err)
	}
}

// registryDownloadImage preserves a real filesystem layer while setting the controller's Linux platform.
func registryDownloadImage(t *testing.T) v1.Image {
	t.Helper()
	image, err := random.Image(2048, 1)
	if err != nil { // A failed layer fixture cannot silently become a metadata-only test.
		t.Fatal(err)
	}
	config, err := image.ConfigFile()
	if err != nil { // Keep RootFS identities while editing the runnable platform metadata.
		t.Fatal(err)
	}
	config.OS, config.Architecture = "linux", runtime.GOARCH
	image, err = mutate.ConfigFile(image, config)
	if err != nil { // The registry receives an internally consistent image.
		t.Fatal(err)
	}
	return image
}

// TestRegistryDownloadOriginIsolation protects passwords and identity-token forms throughout native layer requests.
func TestRegistryDownloadOriginIsolation(t *testing.T) {
	calls := 0
	transport := registryDownloadTransport{registry: "ghcr.io", authenticated: true}
	transport.base = sidecarTransport(func(request *http.Request) (*http.Response, error) { // A simulated CDN must see no registry authorization or cookie headers.
		calls++
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" { // External content requests cannot inherit either credential header.
			t.Error("registry credentials reached external content origin")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("content"))}, nil
	})
	request, _ := http.NewRequest("GET", "https://cdn.example.test/layer", nil)
	request.Header.Set("Authorization", "Bearer server-secret")
	request.Header.Set("Cookie", "private=value")
	response, err := transport.RoundTrip(request)
	if err != nil { // Anonymous verified-TLS blob redirects remain supported.
		t.Fatal(err)
	}
	response.Body.Close()
	request, _ = http.NewRequest("POST", "https://unapproved.example.test/token", strings.NewReader("refresh_token=secret"))
	if _, err := transport.RoundTrip(request); err == nil || calls != 1 { // Refresh-token request bodies cannot cross to a redirect-selected origin.
		t.Fatal("unapproved token body forwarded", err)
	}
	request, _ = http.NewRequest("GET", "http://ghcr.io/v2/", nil)
	if _, err := transport.RoundTrip(request); err == nil || calls != 1 { // There is no insecure transport fallback for server credentials.
		t.Fatal("TLS downgrade allowed", err)
	}
	transport.base = sidecarTransport(func(request *http.Request) (*http.Response, error) { // A registry cannot nominate an unrelated password recipient through a bearer challenge.
		return &http.Response{StatusCode: 401, Header: http.Header{"Www-Authenticate": {`Bearer realm="https://unapproved.example.test/token"`}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	request, _ = http.NewRequest("GET", "https://ghcr.io/v2/", nil)
	if _, err := transport.RoundTrip(request); err == nil { // Reject the challenge before the library starts exchanging credentials.
		t.Fatal("unapproved token service accepted")
	}
}

// TestRegistryDownloadHubOrigins keeps the reviewed Docker Hub aliases usable with its separate token service.
func TestRegistryDownloadHubOrigins(t *testing.T) {
	hub := registryDownloadTransport{registry: "registry-1.docker.io"}
	for _, origin := range []string{"https://index.docker.io/v2/", "https://registry-1.docker.io/v2/", "https://auth.docker.io/token"} { // Hub's reviewed aliases and separate token service are all valid.
		target, _ := url.Parse(origin)
		if !hub.approvedOrigin(target) { // Private Hub pulls must retain the same trusted authentication endpoints.
			t.Fatal("Docker Hub origin rejected", origin)
		}
	}
}
