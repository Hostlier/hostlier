package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
)

const registryPullSecret = "local-only-fixture-secret"

// registryPullFixture serves actual manifests, layers and a scoped bearer exchange.
type registryPullFixture struct {
	backend http.Handler
	server  *httptest.Server
	ref     name.Tag
	index   v1.ImageIndex
	auth    atomic.Bool
	tokens  atomic.Int64
	blobs   atomic.Int64
}

// newRegistryPullFixture publishes a multi-platform image before enabling private pull authentication.
func newRegistryPullFixture(t *testing.T, tls bool) *registryPullFixture {
	t.Helper()
	f := &registryPullFixture{backend: registry.New(registry.Logger(log.New(io.Discard, "", 0)))}
	// Direct pull tests verify TLS; complete CLI tests use a disposable loopback registry.
	if tls {
		f.server = httptest.NewTLSServer(f)
	} else {
		f.server = httptest.NewServer(f)
	}
	t.Cleanup(f.server.Close)
	var err error
	f.ref, err = name.NewTag(strings.TrimPrefix(strings.TrimPrefix(f.server.URL, "https://"), "http://") + "/team/private:latest")
	// Invalid fixture references must fail before populating a different repository.
	if err != nil {
		t.Fatal(err)
	}
	f.index = empty.Index
	// Distinct real layers ensure platform selection changes the downloaded bytes.
	for _, arch := range []string{"amd64", "arm64"} {
		image := registryPullImage(t, arch)
		f.index = mutate.AppendManifests(f.index, mutate.IndexAddendum{Add: image, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch}}})
	}
	// Populate through the registry protocol rather than mocking manifests or layer requests.
	if err := remote.WriteIndex(f.ref, f.index, remote.WithTransport(f.server.Client().Transport)); err != nil {
		t.Fatal(err)
	}
	f.auth.Store(true)
	return f
}

// ServeHTTP challenges unauthenticated requests and counts authenticated content reads.
func (f *registryPullFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Setup publishes anonymously; subsequent requests must use the local Docker login.
	if f.auth.Load() {
		// Only the fixture's token endpoint receives the original registry password.
		if r.URL.Path == "/token" {
			f.exchangeToken(w, r)
			return
		}
		// Registry content needs the exchanged bearer token, not the original password.
		if r.Header.Get("Authorization") != "Bearer fixture-pull-access" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+f.server.URL+`/token",service="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Count config and filesystem blob downloads separately from manifest discovery.
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/blobs/") {
			f.blobs.Add(1)
		}
	}
	f.backend.ServeHTTP(w, r)
}

// exchangeToken rejects incorrect passwords and scopes with a deliberately private diagnostic body.
func (f *registryPullFixture) exchangeToken(w http.ResponseWriter, r *http.Request) {
	// Docker's device login stores a refresh token and exchanges it through the OAuth form.
	if r.Method == http.MethodPost {
		f.exchangeIdentityToken(w, r)
		return
	}
	user, password, ok := r.BasicAuth()
	// Read-only repository scope must survive the registry's bearer challenge.
	if !ok || user != "alice" || password != registryPullSecret || r.URL.Query().Get("scope") != "repository:team/private:pull" {
		http.Error(w, "denied "+registryPullSecret, http.StatusUnauthorized)
		return
	}
	f.tokens.Add(1)
	io.WriteString(w, `{"token":"fixture-pull-access"}`)
}

// exchangeIdentityToken models Docker Hub's refresh-token flow without requiring a real account.
func (f *registryPullFixture) exchangeIdentityToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	// Identity tokens belong in the refresh form rather than a Basic password or controller request.
	if r.Form.Get("refresh_token") != registryPullSecret || r.Form.Get("grant_type") != "refresh_token" || r.Header.Get("Authorization") != "" {
		http.Error(w, "denied "+registryPullSecret, http.StatusUnauthorized)
		return
	}
	f.tokens.Add(1)
	io.WriteString(w, `{"access_token":"fixture-pull-access"}`)
}

// registryPullImage creates a real image while retaining its filesystem layer identities.
func registryPullImage(t *testing.T, arch string) v1.Image {
	t.Helper()
	image, err := random.Image(2048, 1)
	// Real layer creation errors must not yield an empty fixture image.
	if err != nil {
		t.Fatal(err)
	}
	config, err := image.ConfigFile()
	// Preserve RootFS and history while changing the platform and runtime launcher.
	if err != nil {
		t.Fatal(err)
	}
	config.OS, config.Architecture = "linux", arch
	config.Config = v1.Config{User: "app", Entrypoint: []string{"/app/server"}}
	image, err = mutate.ConfigFile(image, config)
	// The mutation must preserve a valid runnable image.
	if err != nil {
		t.Fatal(err)
	}
	return image
}

// localRegistryLogin isolates all credential lookup from the developer's Docker configuration.
func localRegistryLogin(t *testing.T, authority string, helper bool) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	config := map[string]any{"auths": map[string]any{authority: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("alice:" + registryPullSecret))}}}
	// Helper cases exercise Docker's per-registry selection rather than only inline auth.
	if helper {
		bin := t.TempDir()
		t.Setenv("PATH", bin)
		t.Setenv("HELPER_CALLS", filepath.Join(dir, "helper-calls"))
		// A registry-specific helper must win over an unavailable store and stale inline credentials.
		config["auths"] = map[string]any{authority: map[string]string{"username": "stale", "password": "stale"}}
		config["credsStore"] = "missing-store"
		config["credHelpers"] = map[string]string{authority: "hostlier-fixture"}
		script := "#!/bin/sh\nread server\nprintf '%s %s\\n' \"$1\" \"$server\" >> \"$HELPER_CALLS\"\n" +
			"printf '%s' '{\"Username\":\"alice\",\"Secret\":\"" + registryPullSecret + "\"}'\n"
		// The helper needs no Docker binary or daemon and records every requested operation.
		if err := os.WriteFile(filepath.Join(bin, "docker-credential-hostlier-fixture"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(config)
	// Serialize only fixture credentials into the isolated configuration directory.
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	// A private mode mirrors a normal Docker login file.
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRegistryContainerPull authenticates locally and preserves the selected platform and launcher.
func TestRegistryContainerPull(t *testing.T) {
	// Both supported Linux architectures must work with ordinary and helper-backed logins.
	for _, test := range []struct {
		name, arch string
		helper     bool
		pinned     bool
	}{
		{name: "inline-amd64", arch: "amd64"},
		{name: "helper-arm64", arch: "arm64", helper: true},
		{name: "pinned-index", arch: "arm64", pinned: true},
	} {
		// Every case gets its own registry and Docker configuration, including immutable index pulls.
		t.Run(test.name, func(t *testing.T) {
			f := newRegistryPullFixture(t, true)
			configPath := localRegistryLogin(t, f.ref.RegistryStr(), test.helper)
			before, _ := os.ReadFile(configPath)
			path := f.download(t, test.arch, test.pinned)
			assertRegistryArchive(t, path, test.arch)
			// Successful preparation must fetch both runnable config and filesystem bytes.
			if f.tokens.Load() == 0 || f.blobs.Load() < 2 {
				t.Fatal("image content was not downloaded")
			}
			assertRegistryLoginUnchanged(t, configPath, before, f.ref.RegistryStr(), test.helper)
		})
	}
}

// download exercises either a mutable tag or a pinned index through the same TLS registry.
func (f *registryPullFixture) download(t *testing.T, arch string, pinned bool) string {
	t.Helper()
	reference := f.ref.Name()
	// Pinning a multi-platform index must still select the requested child platform.
	if pinned {
		digest, err := f.index.Digest()
		// Fixture digests must identify the complete published index.
		if err != nil {
			t.Fatal(err)
		}
		reference = f.ref.Context().Digest(digest.String()).Name()
	}
	path := filepath.Join(t.TempDir(), "image.tar")
	source := artifact.RegistryImage{Image: reference, Architecture: arch}
	// All image bytes must be portable before controller transfer begins.
	if err := pullContainerArchive(context.Background(), source, path, f.server.Client().Transport); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertRegistryArchive verifies the target platform, original launcher and actual filesystem content.
func assertRegistryArchive(t *testing.T, path, arch string) {
	t.Helper()
	image, err := artifact.ContainerImage(path, arch)
	// Reopening uses the same validator as ordinary archive preparation.
	if err != nil {
		t.Fatal(err)
	}
	config, err := image.ConfigFile()
	// Runtime identity and entrypoint must survive the registry-to-archive conversion.
	if err != nil || config.Config.User != "app" || len(config.Config.Entrypoint) != 1 || config.Config.Entrypoint[0] != "/app/server" {
		t.Fatal("image launcher changed", err)
	}
	layers, err := image.Layers()
	// Real layer bytes prevent metadata-only fixtures from passing as complete downloads.
	if err != nil || len(layers) != 1 {
		t.Fatal("image layers changed", err)
	}
}

// assertRegistryLoginUnchanged ensures pulls never persist login changes or mutate the credential store.
func assertRegistryLoginUnchanged(t *testing.T, path string, before []byte, authority string, helper bool) {
	t.Helper()
	after, _ := os.ReadFile(path)
	// Bearer exchange must not rewrite the user's Docker config.
	if !bytes.Equal(before, after) {
		t.Fatal("Docker configuration changed")
	}
	// Inline login cases have no helper process to inspect.
	if !helper {
		return
	}
	calls, err := os.ReadFile(filepath.Join(filepath.Dir(path), "helper-calls"))
	// Only get requests for the selected registry may reach the helper.
	if err != nil || string(calls) != "get "+authority+"\n" {
		t.Fatal("unexpected helper invocation", err)
	}
}

// TestRegistryContainerPlan covers the registry-to-upload-to-immutable-plan path for primary and sidecar images.
func TestRegistryContainerPlan(t *testing.T) {
	f := newRegistryPullFixture(t, false)
	localRegistryLogin(t, f.ref.RegistryStr(), false)
	dir := registryTargetProject(t, f.ref.Name())
	fixture := &containerPlanFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "paired-cli"}}
	client := &fakeClient{reply: fixture.reply}
	options := Options{CWD: dir, Client: client}
	// Offline validation checks syntax without fetching credentials or image content.
	if _, err := invoke([]string{"validate"}, "", options); err != nil {
		t.Fatal(err)
	}
	// No registry or controller requests are permitted during validation.
	if f.tokens.Load() != 0 || len(client.calls) != 0 {
		t.Fatal("offline validation contacted a registry or controller")
	}
	assertRegistryPlanReuse(t, options, fixture)
	assertRegistryControllerCalls(t, client.calls, f.ref.RegistryStr())
}

// assertRegistryPlanReuse exercises repeated preparation without retransmitting identical image content.
func assertRegistryPlanReuse(t *testing.T, options Options, fixture *containerPlanFixture) {
	t.Helper()
	// Each invocation independently resolves the tag and submits its own immutable deployment plan.
	for i := 0; i < 2; i++ {
		// Private registry diagnostics and credentials must never enter normal CLI output.
		if output, err := invoke([]string{"plan"}, "", options); err != nil || strings.Contains(output, registryPullSecret) {
			t.Fatal("registry planning failed or exposed a credential", err)
		}
		// The first plan must transfer actual image content before controller planning.
		if i == 0 && fixture.chunks == 0 {
			t.Fatal("no uploaded image content")
		}
	}
	// Primary, sidecar and repeated preparations share one verified committed artifact.
	if fixture.plans != 2 || fixture.chunks != 1 {
		t.Fatal("identical content was not reused", fixture.plans, fixture.chunks)
	}
}

// assertRegistryControllerCalls checks every controller payload rather than only the final plan.
func assertRegistryControllerCalls(t *testing.T, calls []call, authority string) {
	t.Helper()
	// Upload status, chunks, commit and planning must all exclude local registry credentials.
	for _, call := range calls {
		// The controller must receive the intended registry reference when asked to try downloading first.
		if call.path == "/v1/artifacts/pull" {
			continue
		}
		raw, _ := json.Marshal(call.body)
		// Inspect actual archive chunk bytes as well as JSON metadata; JSON base64 would hide leaks.
		if strings.HasSuffix(call.path, "/chunks") {
			raw = call.body.(map[string]any)["data"].([]byte)
		}
		// Prepared sources contain only the verified local artifact identity.
		if bytes.Contains(raw, []byte(registryPullSecret)) || bytes.Contains(raw, []byte(authority)) {
			t.Fatal("local credentials or registry source reached the controller")
		}
	}
}

// TestRegistryContainerFailures prevents invalid logins or platforms from uploading anything.
func TestRegistryContainerFailures(t *testing.T) {
	f := newRegistryPullFixture(t, true)
	configPath := localRegistryLogin(t, f.ref.RegistryStr(), false)
	// Missing, malformed and broken helper configurations all stop before archive creation.
	for _, test := range []struct {
		name, config string
	}{
		{name: "missing-login", config: `{}`},
		{name: "invalid-config", config: `{broken`},
		{name: "missing-helper", config: `{"credsStore":"hostlier-nonexistent-fixture"}`},
	} {
		// Every credential failure is isolated while the registry deliberately returns private text.
		t.Run(test.name, func(t *testing.T) {
			// Never consult the developer's real login while testing failure handling.
			if err := os.WriteFile(configPath, []byte(test.config), 0600); err != nil {
				t.Fatal(err)
			}
			err := pullContainerArchive(context.Background(), artifact.RegistryImage{Image: f.ref.Name(), Architecture: "amd64"}, filepath.Join(t.TempDir(), "image.tar"), f.server.Client().Transport)
			// Neither helper diagnostics nor registry response bodies may be rendered to the user.
			if err == nil || strings.Contains(err.Error(), registryPullSecret) {
				t.Fatal("failed login was accepted or exposed private diagnostics", err)
			}
		})
	}
	assertRegistryPlatformAndCancellation(t, f)
}

// assertRegistryPlatformAndCancellation checks mismatched child manifests and interrupted transfers.
func assertRegistryPlatformAndCancellation(t *testing.T, f *registryPullFixture) {
	t.Helper()
	localRegistryLogin(t, f.ref.RegistryStr(), false)
	manifest, err := f.index.IndexManifest()
	// Select the amd64 child directly so index selection cannot hide a platform mismatch.
	if err != nil {
		t.Fatal(err)
	}
	reference := f.ref.Context().Digest(manifest.Manifests[0].Digest.String()).Name()
	err = pullContainerArchive(context.Background(), artifact.RegistryImage{Image: reference, Architecture: "arm64"}, filepath.Join(t.TempDir(), "image.tar"), f.server.Client().Transport)
	var problem *model.Error
	// A pinned single-platform image must obey the same target architecture as a tag.
	if !errors.As(err, &problem) || problem.Code != "ARTIFACT_PLATFORM" {
		t.Fatal("single-platform image accepted for the wrong architecture", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = pullContainerArchive(ctx, artifact.RegistryImage{Image: f.ref.Name(), Architecture: "amd64"}, filepath.Join(t.TempDir(), "image.tar"), f.server.Client().Transport)
	// Cancellation must remain recognizable to the caller instead of becoming an auth error.
	if !errors.Is(err, context.Canceled) {
		t.Fatal("download ignored cancellation", err)
	}
}

// TestRegistryContainerIdentityToken supports Docker device-login credentials during local download.
func TestRegistryContainerIdentityToken(t *testing.T) {
	f := newRegistryPullFixture(t, true)
	path := localRegistryLogin(t, f.ref.RegistryStr(), false)
	raw, _ := json.Marshal(map[string]any{"auths": map[string]any{f.ref.RegistryStr(): map[string]string{"identitytoken": registryPullSecret}}})
	// Replace only the disposable fixture login with the device-login token representation.
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	archive := f.download(t, "amd64", false)
	assertRegistryArchive(t, archive, "amd64")
	assertRegistryLoginUnchanged(t, path, raw, f.ref.RegistryStr(), false)
}

// TestRegistryContainerAnonymousAndFailedPreparation verifies public pulls and failure-before-upload behavior.
func TestRegistryContainerAnonymousAndFailedPreparation(t *testing.T) {
	f := newRegistryPullFixture(t, true)
	configPath := localRegistryLogin(t, f.ref.RegistryStr(), false)
	// An empty config must still allow downloads from a public registry.
	if err := os.WriteFile(configPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	f.auth.Store(false)
	archive := f.download(t, "amd64", false)
	assertRegistryArchive(t, archive, "amd64")
	f.auth.Store(true)
	// The CLI must stop before artifact negotiation or planning after an authenticated pull fails.
	// Use the ordinary loopback transport here so the complete preparation entry point participates.
	private := newRegistryPullFixture(t, false)
	client := &fakeClient{reply: (&containerPlanFixture{}).reply}
	var diagnostics bytes.Buffer
	s := &session{ctx: context.Background(), remote: client, errOut: &diagnostics, flags: map[string]string{}, containerArchitecture: "amd64"}
	_, err := s.prepareRegistryImage(private.ref.Name())
	// Failed authentication cannot leave an uploaded artifact or expose private server response bodies.
	if err == nil || len(client.calls) != 1 || client.calls[0].path != "/v1/artifacts/pull" || strings.Contains(diagnostics.String(), registryPullSecret) {
		t.Fatal("failed download uploaded content or exposed credentials", err)
	}
}
