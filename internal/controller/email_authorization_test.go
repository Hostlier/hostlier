package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/emailauth"
	"hostlier/internal/model"
)

// fixtureTransport tests the production device-signing client against real controller handlers.
type fixtureTransport struct {
	handler http.Handler
	last    *http.Request
}

// RoundTrip carries requests without bypassing controller authentication or request-body verification.
func (t *fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.last = r.Clone(r.Context())
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

// enrollmentMailFixture captures codes in memory without external delivery.
type enrollmentMailFixture struct{ message emailauth.Message }

// Send acknowledges simulated provider delivery for the enrollment integration test.
func (m *enrollmentMailFixture) Send(_ context.Context, message emailauth.Message) error {
	m.message = message
	return nil
}

// deviceFixture registers a device-bound identity and uses the actual signing client for requests.
func deviceFixture(t *testing.T, c *Controller) (*client.Client, string, string, *fixtureTransport) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	credential := c.credentials("device")
	id := credential["clientId"].(string)
	token := credential["token"].(string)
	c.state.Clients[id].PublicKey = base64.RawStdEncoding.EncodeToString(public)
	c.mu.Unlock()
	api, err := client.New(client.Options{URL: "https://controller.example.com", Token: client.EncodeDeviceCredential(token, private)})
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	transport := &fixtureTransport{handler: c.Handler()}
	api.HTTP = &http.Client{Transport: transport}
	return api, id, token, transport
}

// TestEmailControllerEnrollmentAndSignedReconnect exercises enrollment, token-theft resistance and read-only reconnection.
func TestEmailControllerEnrollmentAndSignedReconnect(t *testing.T) {
	f := newHumanFixture(t)
	mail := &enrollmentMailFixture{}
	service, err := emailauth.New([]string{"owner@example.com"}, mail, "controller.example.com")
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	f.c.opts.EmailEnrollment = service
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	_, receipt := humanHTTP(t, f.c, false, "POST", "/v1/email/begin", emailauth.Request{Email: "owner@example.com", Name: "Laptop", PublicKey: base64.RawStdEncoding.EncodeToString(public)}, "", "")
	id := receipt["id"].(string)
	proof := emailauth.Proof{ID: id, Code: mail.message.Code, Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, emailauth.EnrollmentMessage(service.Realm, id)))}
	status, credential := humanHTTP(t, f.c, false, "POST", "/v1/email/verify", proof, "", "")
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, credential)
	}
	token := credential["token"].(string)
	status, _ = humanHTTP(t, f.c, false, "GET", "/v1/status", nil, token, "")
	// Fail if the fixture violates this invariant: stolen bearer alone authenticated.
	if status == 200 {
		t.Fatal("stolen bearer alone authenticated")
	}
	api, err := client.New(client.Options{URL: "https://controller.example.com", Token: client.EncodeDeviceCredential(token, private)})
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	transport := &fixtureTransport{handler: f.c.Handler()}
	api.HTTP = &http.Client{Transport: transport}
	var result any
	// Stop when fixture setup or the exercised operation fails.
	if err = api.Request(context.Background(), "GET", "/v1/clients", nil, &result); err != nil {
		t.Fatal(err)
	}
	// Replaying the same signed request in a fresh HTTP context must fail.
	replay := transport.last.Clone(context.Background())
	w := httptest.NewRecorder()
	f.c.Handler().ServeHTTP(w, replay)
	// Fail if the fixture violates this invariant: signed request replay accepted.
	if w.Code == 200 {
		t.Fatal("signed request replay accepted")
	}
	f.c.deviceEpoch = random()
	// Fail if the fixture violates this invariant: reconnect after epoch change.
	if err = api.Request(context.Background(), "GET", "/v1/clients", nil, &result); err != nil {
		t.Fatal("reconnect after epoch change", err)
	}
	cl := f.c.state.Clients[credential["clientId"].(string)]
	cl.Revoked = true
	// Fail if the fixture violates this invariant: revoked device authenticated.
	if err = api.Request(context.Background(), "GET", "/v1/clients", nil, &result); err == nil {
		t.Fatal("revoked device authenticated")
	}
}

// grantFixture obtains a genuine hardware-approved bounded authorization for a separate signed device.
func grantFixture(t *testing.T, f *humanFixture, device string, maxUses int) *Authorization {
	t.Helper()
	a := f.propose(t, "POST", "/v1/authorizations", map[string]any{"device": device, "host": "local", "apps": []string{"api"}, "secrets": []string{"allowed-secret"}, "scope": "deploy", "durationSeconds": 3600, "maxUses": maxUses})
	status, result := approveFixtureRequest(t, f, a)
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, result)
	}
	return f.c.state.Authorizations[result["id"].(string)]
}

// scopedProposalFixture submits a real actor-owned exact-plan proposal through signed device authentication.
func scopedProposalFixture(t *testing.T, f *humanFixture, api *client.Client, actor, name, secret string) map[string]any {
	t.Helper()
	id := random()
	plan := &Plan{ID: id, Actor: actor, Fingerprint: "fingerprint-" + id, Generation: f.c.state.Generation, ExpiresAt: time.Now().Add(time.Minute).UnixMilli(), Spec: model.Project{Name: name}}
	// Select the fixture behavior required to exercise this boundary.
	if secret != "" {
		plan.SecretRefs = map[string]SecretRef{"allowed-secret": {Name: secret, Revision: "1"}}
	}
	f.c.mu.Lock()
	f.c.state.Plans[id] = plan
	f.c.mu.Unlock()
	var result map[string]any
	err := api.Request(context.Background(), "POST", "/v1/approvals", map[string]any{"method": "POST", "path": "/v1/plans/" + id + "/apply", "body": map[string]any{"approved": true, "fingerprint": plan.Fingerprint, "requestId": random()}}, &result)
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// TestAuthorizationScopesRejectPrivilegeExpansion verifies exact apps, secret references, sensitive paths and use limits.
func TestAuthorizationScopesRejectPrivilegeExpansion(t *testing.T) {
	f := newHumanFixture(t)
	api, actor, _, _ := deviceFixture(t, f.c)
	grant := grantFixture(t, f, actor, 2)
	// Exercise every case independently, including denied inputs.
	for _, input := range []struct {
		name, secret string
		approved     bool
	}{{"other-app", "", false}, {"api", "unrelated-secret", false}, {"api", "allowed-secret", true}, {"api", "", true}, {"api", "", false}} {
		a := scopedProposalFixture(t, f, api, actor, input.name, input.secret)
		// Stop when fixture setup or the exercised operation fails.
		if (a["status"] == "approved") != input.approved {
			t.Fatal(input, a)
		}
	}
	// Fail if the fixture violates this invariant: use budget incorrect.
	if grant.Uses != 2 {
		t.Fatal("use budget incorrect", grant.Uses)
	}
	// Exercise every case independently, including denied inputs.
	for _, path := range []string{"/v1/secrets/key", "/v1/hosts", "/v1/authorizations", "/v1/resources/db/action"} {
		var result map[string]any
		err := api.Request(context.Background(), "POST", "/v1/approvals", map[string]any{"method": "POST", "path": path, "body": map[string]any{}}, &result)
		// Fail if the fixture violates this invariant: sensitive action inherited window.
		if err != nil || result["status"] != "pending" {
			t.Fatal("sensitive action inherited window", path, result, err)
		}
	}
}

// TestAuthorizationExpiryRevocationAndKeyLoss invalidate outstanding automatic approvals at execution time.
func TestAuthorizationExpiryRevocationAndKeyLoss(t *testing.T) {
	// Exercise every case independently, including denied inputs.
	for _, reason := range []string{"expired", "revoked", "key"} {
		// Isolate this case so credentials and mutable state cannot leak between scenarios.
		t.Run(reason, func(t *testing.T) {
			f := newHumanFixture(t)
			api, actor, _, _ := deviceFixture(t, f.c)
			grant := grantFixture(t, f, actor, 2)
			a := scopedProposalFixture(t, f, api, actor, "api", "")
			// Stop when fixture setup or the exercised operation fails.
			if a["status"] != "approved" {
				t.Fatal(a)
			}
			// Keep distinct protocol outcomes on explicit execution paths.
			switch reason {
			// Handle only this explicit protocol outcome.
			case "expired":
				grant.ExpiresAt = 1
			// Handle only this explicit protocol outcome.
			case "revoked":
				grant.Revoked = true
			// Handle only this explicit protocol outcome.
			case "key":
				key := f.c.state.Human.Keys[grant.KeyFingerprint]
				key.Revoked = true
				f.c.state.Human.Keys[grant.KeyFingerprint] = key
			}
			var result any
			err := api.Request(context.Background(), "POST", "/v1/approvals/"+a["id"].(string)+"/execute", map[string]any{}, &result)
			// Fail if the fixture violates this invariant: invalid authorization reached execution.
			if err == nil || strings.Contains(err.Error(), "runtime") {
				t.Fatal("invalid authorization reached execution", err)
			}
		})
	}
}

// deviceCall exercises the actual signed client and returns only successfully decoded controller results.
func deviceCall(t *testing.T, api *client.Client, method, path string, body any) map[string]any {
	t.Helper()
	var result map[string]any
	// Fixture failures must not be mistaken for rejected production authority.
	if err := api.Request(context.Background(), method, path, body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// TestAuthorizationExecutesStatelessPlanAndReplaysReceipt covers the complete window-to-runtime path.
func TestAuthorizationExecutesStatelessPlanAndReplaysReceipt(t *testing.T) {
	f := newHumanFixture(t)
	api, actor, _, _ := deviceFixture(t, f.c)
	grant := grantFixture(t, f, actor, 2)
	signatures := f.counter
	// The CLI hint is available only after actual independent grant approval.
	if !api.HasDeploymentAuthorization(context.Background()) {
		t.Fatal("missing live-window hint")
	}
	spec := project()
	spec.Name = "api"
	spec.Route = "api.example.com"
	plan := deviceCall(t, api, "POST", "/v1/plans", map[string]any{"spec": spec})
	a := deviceCall(t, api, "POST", "/v1/approvals", map[string]any{"method": "POST", "path": "/v1/plans/" + plan["id"].(string) + "/apply", "body": map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": "window-deploy"}})
	// A matching stateless plan must receive retained window authority without another ceremony.
	if a["status"] != "approved" {
		t.Fatal(a)
	}
	path := "/v1/approvals/" + a["id"].(string) + "/execute"
	result := deviceCall(t, api, "POST", path, map[string]any{})
	operation := wait(t, f.c, result["operationId"].(string))
	// Completion, rather than proposal acceptance, proves the execution path works.
	if operation["status"] != "succeeded" {
		t.Fatal(operation)
	}
	replay := deviceCall(t, api, "POST", path, map[string]any{})
	// Result retrieval cannot consume another window use or create another deployment.
	if replay["operationId"] != result["operationId"] || grant.Uses != 1 || f.counter != signatures {
		t.Fatal("receipt replay changed authority", replay, grant.Uses)
	}
}

// TestEmailDeviceStatusStreamUsesSignedRequests verifies reconnect and revocation using the real TLS streaming client.
func TestEmailDeviceStatusStreamUsesSignedRequests(t *testing.T) {
	runtime := &observationRuntime{gate: make(chan struct{}), started: make(chan struct{}, 32)}
	close(runtime.gate)
	c := observationController(t, runtime)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	// Device construction must succeed before issuing its reusable credential.
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	credential := c.credentials("device-observer")
	id := credential["clientId"].(string)
	c.state.Clients[id].PublicKey = base64.RawStdEncoding.EncodeToString(public)
	c.mu.Unlock()
	remote, _, _ := grpcFixture(t, c, client.EncodeDeviceCredential(credential["token"].(string), private))
	receiveStatusSample(t, remote)
	receiveStatusSample(t, remote)
	checkStatusRevocation(t, c, remote, id)
}
