package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/dockerlogin"
	"hostlier/internal/model"
)

// registryLoginFixture gives the runtime an isolated on-disk Docker login and a cache miss.
func registryLoginFixture(t *testing.T, authority string, c dockerlogin.Credential) *Runtime {
	t.Helper()
	directory := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"auths": map[string]any{authority: c}})
	// No test may load the developer's Docker account or helper configuration.
	if err := os.WriteFile(filepath.Join(directory, "config.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Model only a local cache miss; registry authentication uses a real verified TLS server.
	return &Runtime{options: Options{DockerLogin: &dockerlogin.Source{Directory: directory}}, web: &http.Client{}, docker: &http.Client{Transport: sidecarTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}}
}

// TestDockerLoginAuthenticatedResolutionAndPull exercises actual TLS challenge exchange and daemon auth delivery.
func TestDockerLoginAuthenticatedResolutionAndPull(t *testing.T) {
	var endpoint string
	tokenRequests := 0
	// The real TLS endpoint observes the authentication protocol at the wire boundary.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The token endpoint must receive credentials only for the expected pull scope.
		if r.URL.Path == "/token" {
			user, password, ok := r.BasicAuth()
			// Validate both credentials and the narrowly requested repository scope.
			if !ok || user != "alice" || password != "secret" || r.URL.Query().Get("scope") != "repository:team/private:pull" {
				t.Error("incorrect registry token authority")
			}
			tokenRequests++
			io.WriteString(w, `{"token":"registry-access"}`)
			return
		}
		// The manifest is inaccessible until a successful bearer exchange.
		if r.Header.Get("Authorization") != "Bearer registry-access" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+endpoint+`/token",service="fixture"`)
			w.WriteHeader(401)
			return
		}
		serveImageFixture(w, r)
	}))
	defer server.Close()
	endpoint = server.URL
	authority := strings.TrimPrefix(endpoint, "https://")
	r := registryLoginFixture(t, authority, dockerlogin.Credential{Username: "alice", Password: "secret"})
	r.web = server.Client()
	info, err := r.InspectImage(context.Background(), authority+"/team/private:latest")
	// Planning must resolve the protected tag to an immutable digest through verified TLS.
	if err != nil || tokenRequests != 1 || !strings.HasSuffix(info.Digest, fixtureDigest(fixtureManifest())) {
		t.Fatal("authenticated resolution failed", err)
	}
	assertDockerPullLogin(t, r, info.Digest)
}

// assertDockerPullLogin observes the actual Engine API payload rather than only testing the lookup function.
func assertDockerPullLogin(t *testing.T, r *Runtime, image string) {
	t.Helper()
	pulls := 0
	// Decode the Engine request to ensure credentials reach only the image pull.
	r.docker = &http.Client{Transport: sidecarTransport(func(req *http.Request) (*http.Response, error) {
		raw, err := base64.URLEncoding.DecodeString(req.Header.Get("X-Registry-Auth"))
		var c RegistryCredential
		// Only the pull endpoint may receive these credentials, for the approved immutable image.
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Username != "alice" || c.Password != "secret" || req.URL.Query().Get("fromImage") != image || req.URL.Path != "/images/create" {
			t.Error("incorrect Docker pull auth")
		}
		pulls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}\n"))}, nil
	})}
	attempt := dockerDeployment{runtime: r, ctx: context.Background(), spec: model.Project{Image: image}}
	// Pulls must reuse the login without involving the Docker CLI or rewriting its config.
	if err := attempt.pull(); err != nil || pulls != 1 {
		t.Fatal("authenticated pull failed", err)
	}
}

// TestDockerIdentityTokenExchange verifies device-login refresh tokens are exchanged by POST, never Basic auth.
func TestDockerIdentityTokenExchange(t *testing.T) {
	var endpoint string
	// The real TLS endpoint observes the authentication protocol at the wire boundary.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A private manifest first requests a registry token.
		if r.URL.Path != "/token" {
			// The exchanged access token must authorize the second manifest request.
			if r.Header.Get("Authorization") == "Bearer access" {
				serveImageFixture(w, r)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+endpoint+`/token",service="fixture"`)
			w.WriteHeader(401)
			return
		}
		r.ParseForm()
		// The refresh token belongs in the form body and is never persisted or sent as a Basic password.
		if r.Method != "POST" || r.Form.Get("refresh_token") != "refresh" || r.Form.Get("grant_type") != "refresh_token" || r.Header.Get("Authorization") != "" {
			t.Error("wrong identity token exchange")
		}
		io.WriteString(w, `{"access_token":"access","refresh_token":"ignored-rotation"}`)
	}))
	defer server.Close()
	endpoint = server.URL
	authority := strings.TrimPrefix(endpoint, "https://")
	r := registryLoginFixture(t, authority, dockerlogin.Credential{IdentityToken: "refresh"})
	r.web = server.Client()
	before, _ := os.ReadFile(filepath.Join(r.options.DockerLogin.Directory, "config.json"))
	_, err := r.InspectImage(context.Background(), authority+"/app:tag")
	// Device-login credentials must work for tag resolution as well as daemon pulls.
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(r.options.DockerLogin.Directory, "config.json"))
	// Token refresh must not modify the user's original refresh token or config.
	if string(before) != string(after) {
		t.Fatal("identity token config was rewritten")
	}
}

// TestRegistryCredentialOriginAndRedirectIsolation blocks challenge and redirect credential exfiltration.
func TestRegistryCredentialOriginAndRedirectIsolation(t *testing.T) {
	// Ports, userinfo and lookalike domains must not inherit the registry's trusted origin.
	for _, target := range []string{"https://ghcr.io:444/token", "https://ghcr.io.attacker.test/token", "https://user@ghcr.io/token"} {
		parsed, _ := url.Parse(target)
		// The approved authority is exact, including its port and absence of URL credentials.
		if approvedTokenOrigin(parsed, "ghcr.io", "") {
			t.Fatal("unapproved token origin accepted")
		}
	}
	var endpoint string
	// The real TLS endpoint observes the authentication protocol at the wire boundary.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Following even a same-host redirect could forward credentials to an unrelated endpoint.
		if r.URL.Path == "/leak" {
			t.Error("credential-bearing redirect followed")
			return
		}
		http.Redirect(w, r, endpoint+"/leak", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	endpoint = server.URL
	authority := strings.TrimPrefix(endpoint, "https://")
	r := registryLoginFixture(t, authority, dockerlogin.Credential{Username: "alice", Password: "secret"})
	r.web = server.Client()
	_, err := r.InspectImage(context.Background(), authority+"/app:tag")
	// A redirect must fail instead of delivering registry credentials to another route.
	if err == nil {
		t.Fatal("redirect accepted")
	}
}
