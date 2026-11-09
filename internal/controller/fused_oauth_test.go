package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestFusedOAuthLifecycle exercises the real controller handlers against a protocol-checking issuer.
func TestFusedOAuthLifecycle(t *testing.T) {
	var issuer, challenge string
	var exchanges, refreshes, revocations atomic.Int32
	// This issuer fixture validates requests through the real HTTP transport.
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The fixture rejects incorrect protocol bindings rather than returning unconditional success.
		switch r.URL.Path {
		// Return only the response for this specific protocol endpoint.
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "revocation_endpoint": issuer + "/revoke"})
		// Return only the response for this specific protocol endpoint.
		case "/token":
			r.ParseForm()
			oauthAssert(t, !(r.Form.Get("client_id") != "hostlier-test"), "wrong OAuth client")
			// Check the protocol invariant before continuing the scenario.
			if r.Form.Get("grant_type") == "authorization_code" {
				exchanges.Add(1)
				hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				oauthAssert(t, !(base64.RawURLEncoding.EncodeToString(hash[:]) != challenge || r.Form.Get("redirect_uri") != "https://hostlier.example.com/fused/oauth/callback" || r.Form.Get("code") != "one-time-code"), "invalid code or PKCE binding")
			} else {
				refreshes.Add(1)
				oauthAssert(t, !(r.Form.Get("refresh_token") != "private-refresh"), "refresh token not retained")
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "private-access", "refresh_token": "private-refresh", "expires_in": 3600, "token_type": "Bearer"})
		// Return only the response for this specific protocol endpoint.
		case "/workspace/app-tokens":
			oauthAssert(t, !(r.Header.Get("Authorization") != "Bearer private-access" || r.URL.Query().Get("app_family_id") != "11111111-2222-3333-4444-555555555555"), "runtime issuance lacks OAuth family authority")
			json.NewEncoder(w).Encode(map[string]string{"token": "private-runtime", "id": "runtime-id"})
		// Return only the response for this specific protocol endpoint.
		case "/revoke":
			r.ParseForm()
			oauthAssert(t, !(r.Form.Get("token") != "private-refresh"), "wrong revocation token")
			revocations.Add(1)
			w.WriteHeader(200)
		// Unexpected endpoints must fail the scenario.
		default:
			http.NotFound(w, r)
		}
	}))
	defer engine.Close()
	issuer = engine.URL
	dir := t.TempDir()
	c := newTestController(t, dir, &recordingRuntime{}, time.Now)
	input := map[string]string{"engineUrl": issuer, "clientId": "hostlier-test"}
	status, result := call(t, c, true, "POST", "/v1/fused/stocks/connect", input, "")
	oauthAssert(t, !(status != 200), status, result)
	authorization, _ := url.Parse(result["authorizationUrl"].(string))
	challenge = authorization.Query().Get("code_challenge")
	state := authorization.Query().Get("state")
	// A forged state cannot consume the legitimate ceremony.
	oauthAssert(t, fusedCallbackTest(c, "wrong", "one-time-code") == 400, "forged callback accepted")
	oauthAssert(t, fusedCallbackTest(c, state, "one-time-code") == 200, "callback failed")
	oauthAssert(t, fusedCallbackTest(c, state, "one-time-code") == 400, "callback replay accepted")
	oauthAssert(t, !(exchanges.Load() != 1), "code replay reached issuer")
	assertFusedPrivate(t, c, state)

	// Restart proves refresh relies on authenticated durable credentials rather than process memory.
	c.Close(t.Context())
	c = newTestController(t, dir, &recordingRuntime{}, time.Now)
	defer c.Close(t.Context())
	status, result = call(t, c, true, "POST", "/v1/fused/stocks/refresh", nil, "")
	oauthAssert(t, !(status != 200 || refreshes.Load() != 1), status, result)
	assertFusedProvisionAndDisconnect(t, c, &revocations)
}

// fusedCallbackTest exercises the public HTTPS callback without bypassing state checks.
func fusedCallbackTest(c *Controller, state, code string) int {
	r := httptest.NewRequest("GET", "https://hostlier.example.com/fused/oauth/callback?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
	w := httptest.NewRecorder()
	c.Handler().ServeHTTP(w, r)
	return w.Code
}

// TestFusedOAuthRejectsUnapprovedAndExpiredFlows preserves owner consent and short-lived state.
func TestFusedOAuthRejectsUnapprovedAndExpiredFlows(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer c.Close(t.Context())
	status, _ := call(t, c, false, "POST", "/v1/fused/test/connect", map[string]string{"engineUrl": "https://example.com", "clientId": "x"}, "")
	oauthAssert(t, !(status == 200), "anonymous owner operation admitted")
	c.state.FusedConnections = map[string]*FusedOAuthConnection{"expired": {Status: "pending", ExpiresAt: time.Now().Add(-time.Minute)}}
	c.saveFusedPrivate(c.state.FusedConnections["expired"], fusedOAuthPrivate{StateHash: verifier("expired")})
	oauthAssert(t, !(fusedCallbackTest(c, "expired", "code") != 400), "expired state accepted")
}

// oauthAssert keeps protocol assertions readable while retaining their caller location.
func oauthAssert(t *testing.T, valid bool, details ...any) {
	t.Helper()
	// Stop the scenario immediately when its expected security boundary fails.
	if !valid {
		t.Fatal(details...)
	}
}

// assertFusedPrivate checks both public status and durable serialization for plaintext credentials.
func assertFusedPrivate(t *testing.T, c *Controller, state string) {
	t.Helper()
	_, result := call(t, c, true, "GET", "/v1/fused", nil, "")
	public, _ := json.Marshal(result)
	durable, _ := json.Marshal(c.state)
	// Every token and ceremony secret must remain encrypted or hashed.
	for _, secret := range []string{"private-access", "private-refresh", state} {
		oauthAssert(t, !strings.Contains(string(public), secret), "public credential leak")
		oauthAssert(t, !strings.Contains(string(durable), secret), "durable credential leak")
	}
}

// assertFusedProvisionAndDisconnect checks one-time runtime issuance and explicit grant revocation.
func assertFusedProvisionAndDisconnect(t *testing.T, c *Controller, revocations *atomic.Int32) {
	t.Helper()
	tokenInput := map[string]any{"familyId": "11111111-2222-3333-4444-555555555555", "secretName": "stocks-token", "tokenName": "stocks-v1", "allow": []string{"prices"}}
	status, result := call(t, c, true, "POST", "/v1/fused/stocks/token", tokenInput, "")
	oauthAssert(t, !(status != 200), status, result)
	public, _ := json.Marshal(result)
	oauthAssert(t, !(strings.Contains(string(public), "private-runtime")), "runtime credential leaked to CLI")
	secret := c.state.Secrets["stocks-token"]
	value, err := c.store.Decrypt(secret.Revisions[secret.Current])
	oauthAssert(t, !(err != nil || value != "private-runtime"), "runtime token not encrypted")
	status, _ = call(t, c, true, "POST", "/v1/fused/stocks/token", tokenInput, "")
	oauthAssert(t, !(status == 200), "existing secret overwritten")
	status, result = call(t, c, true, "DELETE", "/v1/fused/stocks", nil, "")
	oauthAssert(t, !(status != 200 || revocations.Load() != 1 || len(c.state.FusedConnections) != 0), status, result)
}

// TestFusedOAuthDeniesDeploymentCredentials prevents a scoped deployment token becoming Engine management authority.
func TestFusedOAuthDeniesDeploymentCredentials(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, _, _ := pairedFixture(t, c)
	var result any
	// Both metadata and connection changes stay outside the deployment grant.
	for _, method := range []string{"GET", "POST", "DELETE"} {
		err := api.Request(t.Context(), method, "/v1/fused/stocks/connect", nil, &result)
		oauthAssert(t, err != nil, "deployment credential reached Engine administration")
	}
}

// TestFusedOAuthDenialAndHostBinding ensures a callback is tied to the controller origin and explicit consent.
func TestFusedOAuthDenialAndHostBinding(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	pending := &FusedOAuthConnection{Status: "pending", ExpiresAt: time.Now().Add(time.Minute)}
	c.state.FusedConnections = map[string]*FusedOAuthConnection{"stocks": pending}
	c.saveFusedPrivate(pending, fusedOAuthPrivate{StateHash: verifier("expected"), Verifier: "private-verifier"})
	r := httptest.NewRequest("GET", "https://other.example/fused/oauth/callback?state=expected&code=code", nil)
	w := httptest.NewRecorder()
	c.Handler().ServeHTTP(w, r)
	oauthAssert(t, w.Code == 400, "wrong host accepted")
	oauthAssert(t, pending.Status == "pending", "wrong host consumed state")
	r = httptest.NewRequest("GET", "https://hostlier.example.com/fused/oauth/callback?state=expected&error=access_denied", nil)
	w = httptest.NewRecorder()
	c.Handler().ServeHTTP(w, r)
	oauthAssert(t, w.Code == 400, "denial accepted")
	oauthAssert(t, fusedCallbackTest(c, "expected", "code") == 400, "denied state reused")
}
