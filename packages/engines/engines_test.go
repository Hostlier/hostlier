package engines

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"hostlier/internal/model"
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

// TestPostgresDerivesOmittedDatabaseName binds the default to a retained workflow key.
func TestPostgresDerivesOmittedDatabaseName(t *testing.T) {
	req := remoteRequest("postgres")
	delete(req.Step.Config, "database")
	p := Package{Engine: "postgres"}
	config, err := p.validate(req)
	if err != nil || config.Database != identity(req) || !identifier.MatchString(config.Database) {
		t.Fatalf("invalid derived database: %q %v", config.Database, err)
	}
	prepared, err := p.Handle(context.Background(), req)
	if err != nil || !strings.Contains(strings.Join(prepared.Prepared.Summary, " "), config.Database) {
		t.Fatalf("plan did not review derived database: %v %v", prepared, err)
	}
	req.Action = "inspect"
	req.Prepared = prepared.Prepared.Payload
	recovered, err := p.validate(req)
	var retained payload
	if err != nil || recovered.Database != config.Database || json.Unmarshal(req.Prepared, &retained) != nil || retained.Credentials.Database != config.Database || retained.Fingerprint != fingerprint("postgres", req) || validatePrepared(req, recovered, retained) != nil {
		t.Fatalf("recovery changed database identity: %q %v", recovered.Database, err)
	}
	other := req
	other.Key = "other-app/cache"
	second, err := p.validate(other)
	if err != nil || second.Database == config.Database {
		t.Fatalf("different workflow reused database: %q %v", second.Database, err)
	}
	req.Step.Config["database"] = "stable_workspace_db"
	explicit, err := p.validate(req)
	if err != nil || explicit.Database != "stable_workspace_db" {
		t.Fatalf("explicit database changed: %q %v", explicit.Database, err)
	}
	req.Step.Config["database"] = "invalid-name"
	if _, err := p.validate(req); err == nil {
		t.Fatal("invalid explicit database accepted")
	}
	delete(req.Step.Config, "database")
	req.Step.Phase = "beforeDeploy"
	if _, err := p.validate(req); err == nil {
		t.Fatal("per-deployment hook accepted an unstable derived database")
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

func TestPostgresRequiredChannelBindingSurvivesScopedURL(t *testing.T) {
	req := remoteRequest("postgres")
	req.Secrets["connection"] = "postgresql://admin:secret@example.com/admin?channel_binding=require&sslmode=verify-full"
	if _, err := (Package{Engine: "postgres"}).Handle(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	admin, err := connection("postgres", req)
	if err != nil {
		t.Fatal(err)
	}
	app := scopedPostgresURL(admin, Config{Database: "workspace_acme"}, payload{Credentials: model.ResourceCredentials{Username: "scoped_user", Password: "scoped_password"}})
	if app.Query().Get("channel_binding") != "require" || app.Query().Get("sslmode") != "verify-full" || app.Path != "/workspace_acme" || app.User.Username() != "scoped_user" {
		t.Fatalf("scoped URL lost verified connection settings: %s", app.Redacted())
	}
	parsed, err := pgx.ParseConfig(app.String())
	if err != nil || parsed.ChannelBinding != "require" {
		t.Fatalf("pgx did not retain required channel binding: %v", err)
	}
}

func TestPostgresRejectsOtherChannelBindingValues(t *testing.T) {
	for _, option := range []string{
		"channel_binding=disable", "channel_binding=prefer", "channel_binding=other", "channel_binding=",
		"channel_binding=require&channel_binding=disable", "channel_binding=require&sslmode=disable",
	} {
		req := remoteRequest("postgres")
		req.Secrets["connection"] = "postgresql://admin:secret@example.com/admin?sslmode=verify-full&" + option
		if _, err := (Package{Engine: "postgres"}).Handle(context.Background(), req); err == nil {
			t.Fatalf("unsafe channel binding accepted: %s", option)
		}
	}
}

func TestDefaultEnginePortProducesValidPackageResult(t *testing.T) {
	for _, test := range []struct {
		uri  string
		port string
	}{
		{uri: "postgresql://scope:secret@db.example/app?sslmode=verify-full", port: "5432"},
		{uri: "rediss://scope:secret@cache.example/0", port: "6379"},
		{uri: "postgresql://scope:secret@db.example:6432/app?sslmode=verify-full", port: "6432"},
	} {
		outputs := map[string]string{"url": test.uri}
		enrich(outputs, model.ResourceCredentials{Username: "scope", Password: "secret", Database: "app"})
		if outputs["port"] != test.port || !workflow.ValidResult(workflow.Result{State: json.RawMessage(`{}`), Outputs: outputs}) {
			t.Fatalf("invalid result for %q: port %q", test.uri, outputs["port"])
		}
	}
}
