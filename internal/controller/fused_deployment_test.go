package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const deploymentFamily = "22222222-2222-4333-8444-555555555555"
const deploymentBucket = "33333333-2222-4333-8444-555555555555"
const deploymentFirstApp = "11111111-2222-4333-8444-555555555555"
const deploymentNextApp = "44444444-2222-4333-8444-555555555555"

// deploymentFixture is a strict app-bound issuer that records every allowed lifecycle request.
type deploymentFixture struct {
	server              *httptest.Server
	manifest, next      string
	challenge, behavior atomic.Value
	deploys, refreshes  atomic.Int32
	revokes             atomic.Int32
}

// deploymentManifest keeps the approved operation scope identical while changing its immutable app release.
func deploymentManifest(version string) string {
	return `{"schema_version":1,"name":"mini","bucket":"support","version":"` + version + `","services":{"slack":{"version":"v1","operations":["send"]}}}`
}

// deploymentDigest binds every issuer response to the precise compiler manifest bytes.
func deploymentDigest(manifest string) string {
	sum := sha256.Sum256([]byte(manifest))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// deploymentResponse constructs one bounded runtime identity without adding management tokens to deployment responses.
func deploymentResponse(appID, manifest, token string) map[string]any {
	return map[string]any{"app_id": appID, "app_family_id": deploymentFamily, "runtime_token": token, "manifest_digest": deploymentDigest(manifest), "default_bucket_id": deploymentBucket, "buckets": []map[string]string{{"id": deploymentBucket, "name": "support"}}}
}

// newDeploymentIssuer admits only the dedicated registration, token, deploy and revocation protocol.
func newDeploymentIssuer(t *testing.T) *deploymentFixture {
	t.Helper()
	fixture := &deploymentFixture{manifest: deploymentManifest("1.0.0"), next: deploymentManifest("1.1.0")}
	fixture.challenge.Store("")
	fixture.behavior.Store("ok")
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Any ordinary token-management endpoint would indicate an authority escalation.
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]string{"issuer": fixture.server.URL, "authorization_endpoint": fixture.server.URL + "/oauth/authorize", "token_endpoint": fixture.server.URL + "/oauth/token", "revocation_endpoint": fixture.server.URL + "/oauth/revoke"})
		case "/oauth/runtime/register":
			var input map[string]any
			oauthAssert(t, json.NewDecoder(r.Body).Decode(&input) == nil, "registration JSON")
			oauthAssert(t, input["manifest"] == fixture.manifest && input["management"] == true, "missing app-bound management registration")
			oauthAssert(t, input["redirect_uri"] == "http://127.0.0.1:12345/fused/callback", "unexpected redirect")
			json.NewEncoder(w).Encode(map[string]any{"client_id": "deployment-client", "client_secret": "private-deployment-client", "client_id_expires_at": time.Now().Add(time.Hour)})
		case "/oauth/runtime/token":
			fixture.token(t, w, r)
		case "/oauth/runtime/deploy":
			fixture.deploy(t, w, r)
		case "/oauth/runtime/revoke":
			r.ParseForm()
			expected := "deploy-refresh"
			// A rotated grant must revoke its current refresh token rather than an obsolete runtime token.
			if fixture.refreshes.Load() > 0 {
				expected = "deploy-refresh-rotated"
			}
			oauthAssert(t, r.Form.Get("token") == expected && r.Form.Get("client_secret") == "private-deployment-client", "wrong deployment revocation")
			fixture.revokes.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected non-deployment endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

// token proves PKCE on first admission and binds refresh requests to the same confidential client.
func (fixture *deploymentFixture) token(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	r.ParseForm()
	oauthAssert(t, r.Form.Get("client_id") == "deployment-client" && r.Form.Get("client_secret") == "private-deployment-client", "wrong deployment client")
	response := map[string]any{"access_token": "deploy-access", "refresh_token": "deploy-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": "app.api.deploy"}
	// The initial exchange is the only response combining a management grant with an execution identity.
	if r.Form.Get("grant_type") == "authorization_code" {
		proof := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		oauthAssert(t, r.Form.Get("code") == "deployment-code" && base64.RawURLEncoding.EncodeToString(proof[:]) == fixture.challenge.Load().(string), "wrong deployment PKCE proof")
		for key, value := range deploymentResponse(deploymentFirstApp, fixture.manifest, "frt_deployment-first") {
			response[key] = value
		}
	} else {
		oauthAssert(t, r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") == "deploy-refresh", "wrong app-bound refresh")
		fixture.refreshes.Add(1)
		response["access_token"], response["refresh_token"] = "deploy-access-rotated", "deploy-refresh-rotated"
		// A malicious issuer scope expansion must be rejected before state publication or deployment.
		if fixture.behavior.Load().(string) == "broaden-refresh" {
			response["scope"] = "app.api.deploy app.api.tokens.manage"
		}
	}
	json.NewEncoder(w).Encode(response)
}

// deploy verifies use of the controller-held access token and can return invalid binding fixtures.
func (fixture *deploymentFixture) deploy(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	fixture.deploys.Add(1)
	expected := "Bearer deploy-access"
	// Deployment after expiry must use the successfully rotated access token.
	if fixture.refreshes.Load() > 0 {
		expected = "Bearer deploy-access-rotated"
	}
	oauthAssert(t, r.Header.Get("Authorization") == expected, "wrong deployment bearer")
	var input map[string]string
	oauthAssert(t, json.NewDecoder(r.Body).Decode(&input) == nil && input["manifest"] == fixture.next, "wrong deployment manifest")
	response := deploymentResponse(deploymentNextApp, fixture.next, "frt_deployment-next")
	// Each failure changes exactly one authority binding to prove independent validation.
	switch fixture.behavior.Load().(string) {
	case "error":
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	case "family":
		response["app_family_id"] = "55555555-2222-4333-8444-555555555555"
	case "digest":
		response["manifest_digest"] = deploymentDigest(fixture.manifest)
	case "bucket":
		response["buckets"] = []map[string]string{{"id": deploymentBucket, "name": "support"}, {"id": "66666666-2222-4333-8444-555555555555", "name": "unapproved"}}
	}
	json.NewEncoder(w).Encode(response)
}

// connectDeployment completes the owner-approved fixture ceremony without bypassing its PKCE state checks.
func connectDeployment(t *testing.T, c *Controller, fixture *deploymentFixture) {
	t.Helper()
	status, result := call(t, c, true, "POST", "/v1/fused/mini/connect", map[string]any{"engineUrl": fixture.server.URL, "grpcUrl": "https://grpc.example.com", "secretName": "mini-first", "manifest": fixture.manifest, "management": true, "redirectUri": "http://127.0.0.1:12345/fused/callback"}, "")
	oauthAssert(t, status == 200, status, result)
	authorization, err := url.Parse(result["authorizationUrl"].(string))
	oauthAssert(t, err == nil && authorization.Path == "/oauth/runtime/authorize" && authorization.Query().Get("scope") == "app.api.deploy", "wrong deployment consent scope")
	fixture.challenge.Store(authorization.Query().Get("code_challenge"))
	status, result = call(t, c, true, "POST", "/v1/fused/mini/complete", map[string]string{"state": authorization.Query().Get("state"), "code": "deployment-code"}, "")
	oauthAssert(t, status == 200, status, result)
}

// deploymentInput requires a fresh runtime secret for each immutable Minion release.
func deploymentInput(fixture *deploymentFixture) map[string]string {
	return map[string]string{"engineUrl": fixture.server.URL, "grpcUrl": "https://grpc.example.com", "secretName": "mini-next", "manifest": fixture.next}
}

// decryptedDeploymentSecret inspects only the exact named fixture secret and fails on missing encryption state.
func decryptedDeploymentSecret(t *testing.T, c *Controller, name string) string {
	t.Helper()
	secret := c.state.Secrets[name]
	oauthAssert(t, secret != nil, "missing runtime secret", name)
	value, err := c.store.Decrypt(secret.Revisions[secret.Current])
	oauthAssert(t, err == nil, "runtime decryption failed")
	return value
}

// deploymentState captures authority-bearing state while allowing mandatory local-administration audit records.
func deploymentState(t *testing.T, c *Controller) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"connections": c.state.FusedConnections, "secrets": c.state.Secrets, "generation": c.state.Generation})
	oauthAssert(t, err == nil, "state snapshot failed")
	return string(data)
}

// TestDeploymentGrantPublishesNewRuntimeAndPreservesRollback exercises consent, deployment and explicit grant revocation.
func TestDeploymentGrantPublishesNewRuntimeAndPreservesRollback(t *testing.T) {
	fixture := newDeploymentIssuer(t)
	directory := t.TempDir()
	c := newTestController(t, directory, &recordingRuntime{}, time.Now)
	t.Cleanup(func() { c.Close(t.Context()) })
	connectDeployment(t, c, fixture)
	status, result := call(t, c, true, "POST", "/v1/fused/mini/deploy", deploymentInput(fixture), "")
	oauthAssert(t, status == 200 && fixture.deploys.Load() == 1, status, result)
	oauthAssert(t, c.state.FusedConnections["mini"].Runtime.AppID == deploymentNextApp && c.state.FusedConnections["mini"].Runtime.TokenSecret == "mini-next", "new immutable runtime missing")
	oauthAssert(t, decryptedDeploymentSecret(t, c, "mini-first") == "frt_deployment-first" && decryptedDeploymentSecret(t, c, "mini-next") == "frt_deployment-next", "rollback secret changed")
	// Repeating a completed build recovers the captured identity without minting another runtime credential.
	status, result = call(t, c, true, "POST", "/v1/fused/mini/deploy", deploymentInput(fixture), "")
	oauthAssert(t, status == 200 && fixture.deploys.Load() == 1, "completed deployment was replayed upstream")
	// Restart proves both the new pointer and earlier rollback credential were durably encrypted.
	c.Close(t.Context())
	c = newTestController(t, directory, &recordingRuntime{}, time.Now)
	oauthAssert(t, c.state.FusedConnections["mini"].Runtime.AppID == deploymentNextApp && decryptedDeploymentSecret(t, c, "mini-first") == "frt_deployment-first", "deployment did not survive restart")
	state, _ := json.Marshal(c.state)
	public, _ := json.Marshal(result)
	for _, secret := range []string{"deploy-access", "deploy-refresh", "private-deployment-client", "frt_deployment-first", "frt_deployment-next"} {
		oauthAssert(t, !strings.Contains(string(state), secret) && !strings.Contains(string(public), secret), "private grant leaked", secret)
	}
	// The app-bound deployment grant cannot be reused by the generic family-token endpoint.
	status, _ = call(t, c, true, "POST", "/v1/fused/mini/token", map[string]any{"familyId": deploymentFamily, "secretName": "forbidden", "tokenName": "forbidden", "allow": []string{"send"}}, "")
	oauthAssert(t, status != 200 && c.state.Secrets["forbidden"] == nil, "generic token management admitted")
	status, result = call(t, c, true, "DELETE", "/v1/fused/mini", nil, "")
	oauthAssert(t, status == 200 && fixture.revokes.Load() == 1, status, result)
}

// TestDeploymentInvalidResponsesPreserveState rejects upstream errors and every changed authority binding atomically.
func TestDeploymentInvalidResponsesPreserveState(t *testing.T) {
	for _, mode := range []string{"error", "family", "digest", "bucket"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDeploymentIssuer(t)
			c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
			defer c.Close(t.Context())
			connectDeployment(t, c, fixture)
			fixture.behavior.Store(mode)
			before := deploymentState(t, c)
			status, _ := call(t, c, true, "POST", "/v1/fused/mini/deploy", deploymentInput(fixture), "")
			after := deploymentState(t, c)
			oauthAssert(t, status != 200 && before == after && c.state.Secrets["mini-next"] == nil, "invalid deployment mutated state", mode, status)
		})
	}
}

// TestDeploymentRefreshRotatesOnlyPrivateGrant verifies automatic refresh uses dedicated endpoints and stays encrypted.
func TestDeploymentRefreshRotatesOnlyPrivateGrant(t *testing.T) {
	fixture := newDeploymentIssuer(t)
	var seconds atomic.Int64
	seconds.Store(time.Now().Unix())
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, func() time.Time { return time.Unix(seconds.Load(), 0) })
	defer c.Close(t.Context())
	connectDeployment(t, c, fixture)
	seconds.Add(3700)
	status, result := call(t, c, true, "POST", "/v1/fused/mini/deploy", deploymentInput(fixture), "")
	oauthAssert(t, status == 200 && fixture.refreshes.Load() == 1 && fixture.deploys.Load() == 1, status, result)
	_, private, err := c.loadFusedPrivate("mini")
	oauthAssert(t, err == nil && private.Tokens.AccessToken == "deploy-access-rotated" && private.Tokens.RefreshToken == "deploy-refresh-rotated", "rotated grant not retained privately")
	state, _ := json.Marshal(c.state)
	oauthAssert(t, !strings.Contains(string(state), "deploy-access-rotated") && !strings.Contains(string(state), "deploy-refresh-rotated"), "rotated grant leaked")
	status, result = call(t, c, true, "DELETE", "/v1/fused/mini", nil, "")
	oauthAssert(t, status == 200 && fixture.revokes.Load() == 1, status, result)
}

// TestDeploymentRefreshCannotBroadenScope rejects a rotated grant with any additional management authority.
func TestDeploymentRefreshCannotBroadenScope(t *testing.T) {
	fixture := newDeploymentIssuer(t)
	var seconds atomic.Int64
	seconds.Store(time.Now().Unix())
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, func() time.Time { return time.Unix(seconds.Load(), 0) })
	defer c.Close(t.Context())
	connectDeployment(t, c, fixture)
	fixture.behavior.Store("broaden-refresh")
	seconds.Add(3700)
	before := deploymentState(t, c)
	status, _ := call(t, c, true, "POST", "/v1/fused/mini/deploy", deploymentInput(fixture), "")
	after := deploymentState(t, c)
	oauthAssert(t, status != 200 && fixture.refreshes.Load() == 1 && fixture.deploys.Load() == 0, "broadened refresh admitted")
	oauthAssert(t, before == after && c.state.Secrets["mini-next"] == nil, "broadened refresh mutated state")
}
