package runtime

import (
	"context"
	"hostlier/internal/dockerlogin"
	"hostlier/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestAnonymousManifestFollowsVerifiedHTTPSMirror exercises the public registry flow used by Kubernetes pause images.
func TestAnonymousManifestFollowsVerifiedHTTPSMirror(t *testing.T) {
	var visits atomic.Int32
	// The mirror must receive only credential-free GET metadata requests.
	mirror := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cookies, bearer tokens and Basic credentials must never follow a public metadata redirect.
		if r.Method != "GET" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credential or method crossed anonymous mirror boundary")
		}
		visits.Add(1)
		serveImageFixture(w, r)
	}))
	defer mirror.Close()
	// A real HTTPS registry can redirect without transferring its own cookie state.
	registry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "fixture-private-cookie=never-forward; Path=/")
		http.Redirect(w, r, mirror.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer registry.Close()
	authority := strings.TrimPrefix(registry.URL, "https://")
	runtime := registryLoginFixture(t, authority, dockerlogin.Credential{})
	runtime.options.DockerLogin = &dockerlogin.Source{Directory: t.TempDir()}
	runtime.web = registry.Client()
	info, err := runtime.InspectImage(context.Background(), authority+"/pause:3.10")
	// The original registry identity is retained with the mirror's verified content digest.
	if err != nil || visits.Load() != 2 || info.Digest != authority+"/pause@"+fixtureDigest(fixtureManifest()) {
		t.Fatalf("mirror resolution %v %v", info, err)
	}
}

// TestAuthenticatedManifestNeverRedirectsCredentials preserves the original strict credential-forwarding boundary.
func TestAuthenticatedManifestNeverRedirectsCredentials(t *testing.T) {
	var visits atomic.Int32
	// The second origin records whether the runtime accidentally contacted it with retained authority.
	mirror := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { visits.Add(1) }))
	defer mirror.Close()
	// Even HTTPS redirects cannot carry an authenticated manifest request to another origin.
	registry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, mirror.URL, http.StatusTemporaryRedirect)
	}))
	defer registry.Close()
	authority := strings.TrimPrefix(registry.URL, "https://")
	runtime := registryLoginFixture(t, authority, dockerlogin.Credential{Username: "alice", Password: "private-fixture-password"})
	runtime.web = registry.Client()
	_, err := runtime.InspectImage(context.Background(), authority+"/private:latest")
	// The caller gets an image-specific status without a request credential or provider body.
	if err == nil || visits.Load() != 0 || !strings.Contains(err.Error(), "HTTP 307") || strings.Contains(err.Error(), "private-fixture-password") {
		t.Fatalf("redirect boundary %v visits %d", err, visits.Load())
	}
}

// TestAnonymousManifestRejectsHTTPDowngrade prevents a public metadata redirect from losing TLS verification.
func TestAnonymousManifestRejectsHTTPDowngrade(t *testing.T) {
	var visits atomic.Int32
	// Plain HTTP must remain unreachable even when an HTTPS registry requests it.
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { visits.Add(1) }))
	defer mirror.Close()
	// The initial source is verified TLS, isolating downgrade handling from certificate errors.
	registry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, mirror.URL, http.StatusTemporaryRedirect)
	}))
	defer registry.Close()
	authority := strings.TrimPrefix(registry.URL, "https://")
	runtime := registryLoginFixture(t, authority, dockerlogin.Credential{})
	runtime.options.DockerLogin = &dockerlogin.Source{Directory: t.TempDir()}
	runtime.web = registry.Client()
	_, err := runtime.InspectImage(context.Background(), authority+"/public:latest")
	// No insecure fallback may satisfy an immutable approval input.
	if err == nil || visits.Load() != 0 {
		t.Fatalf("HTTP downgrade %v visits %d", err, visits.Load())
	}
}

// TestRegistryImageDiagnosticsOmitCredentialURLs prevents malformed image input from appearing in public errors.
func TestRegistryImageDiagnosticsOmitCredentialURLs(t *testing.T) {
	err := registryImageContext("https://user:private-value@example.com/repo", model.Err("IMAGE_RESOLUTION", "Registry manifest lookup returned HTTP 404"))
	// A malformed URL is identified generically, while the useful registry HTTP status is preserved.
	if strings.Contains(err.Error(), "private-value") || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatal("unsafe or unhelpful image diagnostic")
	}
}
