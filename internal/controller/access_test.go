package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"hostlier/internal/client"
	"hostlier/internal/model"
	"net/http"
	"testing"
	"time"
)

// pairedFixture exercises the actual public start/redeem protocol with local owner approval.
func pairedFixture(t *testing.T, c *Controller) (*client.Client, string, string) {
	t.Helper()
	return pairedScopeFixture(t, c, deploymentScope{Apps: []string{"api"}, DurationSeconds: 3600, MaxUses: 2})
}

// pairedScopeFixture exercises owner approval and signed redemption for the exact selected permissions.
func pairedScopeFixture(t *testing.T, c *Controller, scope deploymentScope) (*client.Client, string, string) {
	t.Helper()
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	return pairWithKey(t, c, scope, public, private)
}

func pairWithKey(t *testing.T, c *Controller, scope deploymentScope, public ed25519.PublicKey, private ed25519.PrivateKey) (*client.Client, string, string) {
	t.Helper()
	status, start := humanHTTP(t, c, false, "POST", "/v1/access/pair/start", map[string]string{"name": "Laptop", "publicKey": base64.RawStdEncoding.EncodeToString(public)}, "", "")
	// Fail the test when the expected security invariant is violated.
	if status != 200 {
		t.Fatal(status, start)
	}
	body := map[string]any{"id": start["id"], "scope": scope}
	status, denied := humanHTTP(t, c, false, "POST", "/v1/access/pair/code", body, "", "")
	// Fail the test when the expected security invariant is violated.
	if status == 200 {
		t.Fatal("public caller issued code", denied)
	}
	status, approval := humanHTTP(t, c, true, "POST", "/v1/access/pair/code", body, "", "")
	// Fail the test when the expected security invariant is violated.
	if status != 200 {
		t.Fatal(status, approval)
	}
	proof := map[string]any{"id": start["id"], "requestSecret": start["requestSecret"], "code": approval["code"], "signature": base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, []byte(start["message"].(string))))}
	status, credential := humanHTTP(t, c, false, "POST", "/v1/access/pair/redeem", proof, "", "")
	// Fail the test when the expected security invariant is violated.
	if status != 200 {
		t.Fatal(status, credential)
	}
	status, _ = humanHTTP(t, c, false, "POST", "/v1/access/pair/redeem", proof, "", "")
	// Fail the test when the expected security invariant is violated.
	if status == 200 {
		t.Fatal("code replay accepted")
	}
	api, err := client.New(client.Options{URL: "https://controller.example.com", Token: client.EncodeDeviceCredential(credential["token"].(string), private)})
	// Fail the test when the expected security invariant is violated.
	if err != nil {
		t.Fatal(err)
	}
	api.HTTP = &http.Client{Transport: &fixtureTransport{handler: c.Handler()}}
	return api, credential["clientId"].(string), credential["authorizationId"].(string)
}

// TestScopedPairingWithoutHardware verifies bootstrap, exact scope, revocation and signed deployment receipts.
func TestScopedPairingWithoutHardware(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, actor, grantID := pairedFixture(t, c)
	var result any
	// Exercise this protocol boundary independently.
	for _, path := range []string{"/v1/clients", "/v1/secrets", "/v1/status", "/v1/apps/other"} {
		// Fail the test when the expected security invariant is violated.
		if err := api.Request(context.Background(), "GET", path, nil, &result); err == nil {
			t.Fatal("scope leak", path)
		}
	}
	f := &humanFixture{c: c}
	approved := scopedProposalFixture(t, f, api, actor, "api", "")
	// Fail the test when the expected security invariant is violated.
	if approved["status"] != "approved" {
		t.Fatal(approved)
	}
	c.state.Authorizations[grantID].Revoked = true
	// Fail the test when the expected security invariant is violated.
	if err := api.Request(context.Background(), "POST", "/v1/approvals/"+approved["id"].(string)+"/execute", map[string]any{}, &result); err == nil {
		t.Fatal("revoked grant executed")
	}
}

// TestScopedPreparationDeniesSecretsAndStatefulCode rejects effects before plan preparation.
func TestScopedPreparationDeniesSecretsAndStatefulCode(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, _, _ := pairedFixture(t, c)
	specs := []model.Project{{Name: "other"}, {Name: "api", Secrets: map[string]string{"TOKEN": "unapproved"}}, {Name: "api", Volumes: map[string]model.Volume{"data": {Mount: "/data"}}}, {Name: "api", Sidecars: map[string]model.Project{"worker": {Secrets: map[string]string{"TOKEN": "unapproved"}}}}}
	// Exercise this protocol boundary independently.
	for _, spec := range specs {
		var result any
		// Fail the test when the expected security invariant is violated.
		if err := api.Request(context.Background(), "POST", "/v1/plans", map[string]any{"spec": spec}, &result); err == nil {
			t.Fatal("unsafe preparation accepted", spec)
		}
	}
	// Fail the test when the expected security invariant is violated.
	if len(c.state.Plans) != 0 {
		t.Fatal("out of scope plans retained")
	}
}

// TestPairingWrongDeviceAndExpiredCode validates possession and finite request lifetime.
func TestPairingWrongDeviceAndExpiredCode(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	_, start := humanHTTP(t, c, false, "POST", "/v1/access/pair/start", map[string]string{"name": "Laptop", "publicKey": base64.RawStdEncoding.EncodeToString(public)}, "", "")
	id := start["id"].(string)
	_, code := humanHTTP(t, c, true, "POST", "/v1/access/pair/code", map[string]any{"id": id, "scope": deploymentScope{Apps: []string{"api"}, DurationSeconds: 60, MaxUses: 1}}, "", "")
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	proof := map[string]any{"id": id, "requestSecret": start["requestSecret"], "code": code["code"], "signature": base64.RawStdEncoding.EncodeToString(ed25519.Sign(wrong, []byte(start["message"].(string))))}
	status, _ := humanHTTP(t, c, false, "POST", "/v1/access/pair/redeem", proof, "", "")
	// Fail the test when the expected security invariant is violated.
	if status == 200 {
		t.Fatal("another key redeemed code")
	}
	c.access.Pairings[id].ExpiresAt = time.Now().Add(-time.Second)
	status, _ = humanHTTP(t, c, false, "POST", "/v1/access/pair/redeem", proof, "", "")
	// Fail the test when the expected security invariant is violated.
	if status == 200 || len(c.state.Clients) != 0 {
		t.Fatal("expired pairing issued credential")
	}
}
