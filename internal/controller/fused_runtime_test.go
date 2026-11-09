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

// TestRuntimeConsentStoresOnlyDedicatedCredentials verifies the complete controller ceremony against a strict issuer fixture.
func TestRuntimeConsentStoresOnlyDedicatedCredentials(t *testing.T) {
	manifest := `{"schema_version":1,"name":"mini","version":"1.0.0","services":{"slack":{"version":"v1","operations":["send"]}}}`
	sum := sha256.Sum256([]byte(manifest))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	var issuer, challenge string
	var exchanges, revoked atomic.Int32
	engine := runtimeConsentIssuer(t, manifest, digest, &issuer, &challenge, &exchanges, &revoked)
	defer engine.Close()
	issuer = engine.URL
	directory := t.TempDir()
	c := newTestController(t, directory, &recordingRuntime{}, time.Now)
	input := map[string]string{"engineUrl": issuer, "grpcUrl": "https://grpc.example.com", "secretName": "mini-runtime", "manifest": manifest, "redirectUri": "http://127.0.0.1:12345/fused/callback"}
	status, result := call(t, c, true, "POST", "/v1/fused/mini/connect", input, "")
	oauthAssert(t, status == 200, status, result)
	authorization, _ := url.Parse(result["authorizationUrl"].(string))
	challenge = authorization.Query().Get("code_challenge")
	oauthAssert(t, authorization.Path == "/oauth/runtime/authorize" && authorization.Query().Get("scope") == "api.execute", "wrong consent audience")
	body := map[string]string{"state": authorization.Query().Get("state"), "code": "runtime-code"}
	status, result = call(t, c, true, "POST", "/v1/fused/mini/complete", body, "")
	oauthAssert(t, status == 200, status, result)
	status, _ = call(t, c, true, "POST", "/v1/fused/mini/complete", body, "")
	oauthAssert(t, status != 200 && exchanges.Load() == 1, "code replay")
	runtime := c.state.FusedConnections["mini"].Runtime
	oauthAssert(t, runtime != nil && runtime.GRPCURL == input["grpcUrl"] && runtime.TokenSecret == "mini-runtime", "runtime binding missing")
	stored := c.state.Secrets[runtime.TokenSecret]
	token, err := c.store.Decrypt(stored.Revisions[stored.Current])
	oauthAssert(t, err == nil && token == "frt_private-runtime", "runtime secret not stored")
	durable, _ := json.Marshal(c.state)
	oauthAssert(t, !strings.Contains(string(durable), token) && !strings.Contains(string(durable), "private-client-secret"), "plaintext persisted")
	// Runtime credentials cannot be repurposed for family-token management or refresh.
	status, _ = call(t, c, true, "POST", "/v1/fused/mini/refresh", nil, "")
	oauthAssert(t, status != 200, "runtime refresh admitted")
	c.Close(t.Context())
	c = newTestController(t, directory, &recordingRuntime{}, time.Now)
	defer c.Close(t.Context())
	status, result = call(t, c, true, "DELETE", "/v1/fused/mini", nil, "")
	oauthAssert(t, status == 200 && revoked.Load() == 1 && result["runtimeTokensRevoked"] == true, status, result)
}

// TestRuntimeConsentRejectsMismatchedManifest exercises response binding after the code has been consumed.
func TestRuntimeConsentRejectsMismatchedManifest(t *testing.T) {
	// The issuer deliberately returns another manifest identity.
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"default_bucket_id": "33333333-2222-4333-8444-555555555555", "buckets": []map[string]string{{"id": "33333333-2222-4333-8444-555555555555", "name": "support"}}, "app_id": "11111111-2222-4333-8444-555555555555", "app_family_id": "22222222-2222-4333-8444-555555555555", "runtime_token": "frt_private", "manifest_digest": "sha256:other"})
	}))
	defer engine.Close()
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer c.Close(t.Context())
	private := fusedOAuthPrivate{Runtime: &fusedRuntimeConsent{ManifestDigest: "sha256:expected", SecretName: "test", GRPCURL: "https://grpc.example"}}
	private.Metadata.TokenEndpoint = engine.URL
	_, err := c.exchangeRuntimeFused(httptest.NewRequest("POST", "/", nil), "missing", private, url.Values{})
	oauthAssert(t, err != nil && c.state.Secrets["test"] == nil, "mismatched manifest created secret")
}

// runtimeConsentIssuer checks real protocol fields without granting management access.
func runtimeConsentIssuer(t *testing.T, manifest, digest string, issuer, challenge *string, exchanges, revoked *atomic.Int32) *httptest.Server {
	// The fixture handles only the endpoints belonging to the runtime consent ceremony.
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Only runtime registration and exchange are available to this client.
		switch r.URL.Path {
		// Discovery must pin all endpoints to the chosen Engine.
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]string{"issuer": *issuer, "authorization_endpoint": *issuer + "/oauth/authorize", "token_endpoint": *issuer + "/oauth/token", "revocation_endpoint": *issuer + "/oauth/revoke"})
		// Registration binds the exact compiler bytes and loopback callback.
		case "/oauth/runtime/register":
			var input map[string]string
			oauthAssert(t, json.NewDecoder(r.Body).Decode(&input) == nil, "registration JSON")
			oauthAssert(t, input["manifest"] == manifest && input["redirect_uri"] == "http://127.0.0.1:12345/fused/callback" && input["scopes"] == "", "wrong runtime registration")
			json.NewEncoder(w).Encode(map[string]any{"client_id": "runtime-client", "client_secret": "private-client-secret", "client_id_expires_at": time.Now().Add(time.Hour)})
		// Redemption must prove possession of the original PKCE verifier.
		case "/oauth/runtime/token":
			r.ParseForm()
			proof := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			oauthAssert(t, base64.RawURLEncoding.EncodeToString(proof[:]) == *challenge && r.Form.Get("client_id") == "runtime-client" && r.Form.Get("code") == "runtime-code", "wrong token proof")
			exchanges.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"default_bucket_id": "33333333-2222-4333-8444-555555555555", "buckets": []map[string]string{{"id": "33333333-2222-4333-8444-555555555555", "name": "support"}}, "app_id": "11111111-2222-4333-8444-555555555555", "app_family_id": "22222222-2222-4333-8444-555555555555", "runtime_token": "frt_private-runtime", "manifest_digest": digest})
		// Revocation is confined to the dedicated client and runtime token.
		case "/oauth/runtime/revoke":
			r.ParseForm()
			oauthAssert(t, r.Form.Get("token") == "frt_private-runtime" && r.Form.Get("client_secret") == "private-client-secret", "wrong revocation")
			revoked.Add(1)
			w.WriteHeader(200)
		// Management-token endpoints must never be called by runtime provisioning.
		default:
			t.Errorf("unexpected management endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}
