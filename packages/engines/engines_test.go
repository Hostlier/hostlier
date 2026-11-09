package engines

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"hostlier/pkg/workflow"
)

// remoteRequest builds a TLS-only planning fixture without making network requests.
func remoteRequest(engine string) workflow.Request {
	mode, connection := "new-database", "postgresql://admin:secret@example.com/admin?sslmode=verify-full"
	config := map[string]any{"mode": mode, "database": "application"}
	if engine == "redis" { // Select the native engine protocol and its isolation rules.
		connection = "rediss://admin:secret@example.com:6379/0"
		config = map[string]any{"mode": "existing", "keyPrefix": "cache:"}
	}
	return workflow.Request{Version: 1, Action: "plan", Key: "app/cache", Step: workflow.Step{ID: "cache", Config: config}, Secrets: map[string]string{"connection": connection}}
}

// TestPlanCredentialsAndBinding verifies private credentials are stable through the approved payload.
func TestPlanCredentialsAndBinding(t *testing.T) {
	for _, engine := range []string{"postgres", "redis"} { // Check each configured engine, namespace, or command independently.
		req := remoteRequest(engine)
		p := Package{Engine: engine}
		response, err := p.Handle(context.Background(), req)
		if err != nil { // Require successful completion before using the returned engine state.
			t.Fatal(err)
		}
		var prepared payload
		if json.Unmarshal(response.Prepared.Payload, &prepared) != nil || len(prepared.Credentials.Password) != 64 { // Reject the operation: missing protected credentials.
			t.Fatal("missing protected credentials")
		}
		if strings.Contains(strings.Join(response.Prepared.Summary, " "), prepared.Credentials.Password) { // Reject the operation: public summary leaks credential.
			t.Fatal("public summary leaks credential")
		}
		req.Prepared = response.Prepared.Payload
		req.Action = "apply"
		req.Key = "other"
		if _, err = p.Handle(context.Background(), req); err == nil { // Reject the operation: changed identity accepted.
			t.Fatal("changed identity accepted")
		}
	}
}

// TestRejectUnsafeConnectionsAndInlineCredentials checks strict boundaries without dialing.
func TestRejectUnsafeConnectionsAndInlineCredentials(t *testing.T) {
	req := remoteRequest("postgres")
	for _, connection := range []string{"postgres://admin:secret@example.com/db", "postgres://admin:secret@example.com/db?sslmode=require", "redis://admin:secret@example.com"} { // Reject the operation: unverified transport accepted.
		req.Secrets["connection"] = connection
		if _, err := (Package{Engine: "postgres"}).Handle(context.Background(), req); err == nil { // Reject the operation: unverified transport accepted.
			t.Fatal("unverified transport accepted")
		}
	}
	req = remoteRequest("redis")
	req.Step.Config["password"] = "secret"
	if _, err := (Package{Engine: "redis"}).Handle(context.Background(), req); err == nil { // Reject the operation: inline credential accepted.
		t.Fatal("inline credential accepted")
	}
}

// TestRedisNamespaceCannotOverlap proves caller labels cannot choose another dependency namespace.
func TestRedisNamespaceCannotOverlap(t *testing.T) {
	req := remoteRequest("redis")
	first := redisPrefix(req, Config{KeyPrefix: "cache:"})
	req.Key = "other"
	second := redisPrefix(req, Config{KeyPrefix: "cache:"})
	if first == second || strings.ContainsAny(first, "*?[]\\") { // Reject the operation: unsafe namespace.
		t.Fatal("unsafe namespace")
	}
}

// TestDeferredInputsBindReferences accepts private upstream values only after review of their source references.
func TestDeferredInputsBindReferences(t *testing.T) {
	req := remoteRequest("postgres")
	req.Secrets = nil
	req.Step.Inputs = map[string]string{"connection": "provider.connection", "caCertificatePEM": "provider.caCertificatePEM"}
	p := Package{Engine: "postgres"}
	response, err := p.Handle(context.Background(), req)
	// Planning must succeed without obtaining provider administrative credentials.
	if err != nil {
		t.Fatal(err)
	}
	req.Inputs = map[string]string{"connection": "postgresql://admin:secret@example.com/postgres?sslmode=verify-full", "caCertificatePEM": "private upstream CA"}
	var prepared payload
	// Deferred values cannot invalidate a review that binds their immutable source references.
	if json.Unmarshal(response.Prepared.Payload, &prepared) != nil || prepared.Fingerprint != fingerprint("postgres", req) {
		t.Fatal("deferred input invalidated review")
	}
	req.Step.Inputs["connection"] = "different.connection"
	// Changing the upstream source requires a new review even if its current value matches.
	if prepared.Fingerprint == fingerprint("postgres", req) {
		t.Fatal("changed input source retained approval")
	}
}

// TestRejectPostgresCredentialQuery prevents administrative identity leaking through URL query overrides.
func TestRejectPostgresCredentialQuery(t *testing.T) {
	req := remoteRequest("postgres")
	req.Secrets["connection"] += "&password=administrative-secret"
	// The driver must never interpret hidden query credentials after app URL user replacement.
	if _, err := (Package{Engine: "postgres"}).Handle(context.Background(), req); err == nil {
		t.Fatal("credential override accepted")
	}
}
