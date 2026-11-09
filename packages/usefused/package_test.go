package usefused

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"hostlier/pkg/workflow"
)

// transportFunc permits real HTTP request/response validation without listening on a socket.
type transportFunc func(*http.Request) (*http.Response, error)

// RoundTrip executes the in-memory Engine transport fixture.
func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// executorConfig is a deliberately non-routable fixture with a pinned consumer version.
func executorConfig() Config {
	return Config{EngineURL: "https://engine.example", ConsumerVersionID: "11111111-1111-1111-1111-111111111111", Operation: "unified_app:database_deploy", UserRef: "server-one"}
}

// requestFixture declares provider deployment intent without any provider API credentials.
func requestFixture(provider string) workflow.Request {
	return workflow.Request{Version: 1, Action: "plan", Key: "namespace/analytics/database",
		Step: workflow.Step{ID: "database", Package: provider,
			Config:  map[string]any{"engine": "postgres", "mode": "new-database", "database": "analytics"},
			Secrets: map[string]string{"executionToken": "fused-token", "connection": "admin-url"}},
		Secrets: map[string]string{"executionToken": "private-execution-token", "connection": "postgres://admin:private@db.example/postgres?sslmode=verify-full"}}
}

// TestPackagePlanAndApply verifies both provider packages use the exact Fused envelope and private credential routing.
func TestPackagePlanAndApply(t *testing.T) {
	// Aiven and Railway share the remote contract but retain separate provider identities.
	for _, provider := range []string{"aiven", "railway"} {
		calls := 0
		// Inspect every outgoing request before returning a hosted workflow response.
		transport := transportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return engineFixture(t, r)
		})
		p := Package{Provider: provider, Client: Client{Config: executorConfig(), HTTP: &http.Client{Transport: transport}}}
		req := requestFixture(provider)
		planned, err := p.Handle(context.Background(), req)
		// Read-only planning must produce a concrete payload before any apply call.
		if err != nil || planned.Prepared == nil {
			t.Fatal(planned, err)
		}
		req.Action, req.Prepared = "apply", planned.Prepared.Payload
		applied, err := p.Handle(context.Background(), req)
		// Only a ready, successful result may be handed to Hostlier for binding.
		if err != nil || applied.Result == nil || applied.Result.Outputs["url"] == "" || calls != 2 {
			t.Fatal(applied, err, calls)
		}
		p.Client.Config.UserRef = "another-user"
		// Endpoint and connected-user changes require fresh review, before any remote mutation.
		if _, err := p.Handle(context.Background(), req); err == nil || calls != 2 {
			t.Fatal("changed invocation configuration executed")
		}
	}
}

// engineFixture checks wire-level behavior and returns a valid versioned Unified App response.
func engineFixture(t *testing.T, r *http.Request) (*http.Response, error) {
	t.Helper()
	// Tokens travel only in Authorization, while version and operation selection are explicit.
	if r.Method != "POST" || r.URL.Path != "/v1/apps/11111111-1111-1111-1111-111111111111/executions" || r.Header.Get("Authorization") != "Bearer private-execution-token" {
		t.Fatal("incorrect execution request")
	}
	raw, _ := io.ReadAll(r.Body)
	// The Fused execution token must not enter app input or private provider diagnostics.
	if strings.Contains(string(raw), "private-execution-token") {
		t.Fatal("execution token copied into input")
	}
	var body struct {
		Operation string `json:"operation"`
		Input     Input  `json:"input"`
	}
	// Validate the actual envelope consumed by Engine.
	if json.Unmarshal(raw, &body) != nil || body.Operation != "unified_app:database_deploy" || body.Input.UserRef != "server-one" {
		t.Fatal("incorrect Unified App selection")
	}
	input := body.Input
	output := Output{Version: 1, Action: input.Action, Provider: input.Provider, Key: input.Key}
	// Planning returns an opaque provider plan but no created resources.
	if input.Action == "plan" {
		output.Summary = []string{"Create an application database in the selected existing instance"}
		output.Plan = json.RawMessage(`{"providerPlan":"reviewed"}`)
	} else { // Apply returns readiness only after the hosted workflow has verified the connection.
		output.Ready = true
		output.Result = &workflow.Result{State: json.RawMessage(`{"resourceId":"owned"}`), Outputs: map[string]string{"url": "postgres://app:generated@db.example/analytics?sslmode=verify-full"}}
	}
	encoded, _ := json.Marshal(map[string]any{"status": "succeeded", "output": output})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(encoded))), Header: http.Header{}}, nil
}

// TestEngineFailureDoesNotRetryOrExposeDiagnostics covers HTTP, envelope, target and transport failures.
func TestEngineFailureDoesNotRetryOrExposeDiagnostics(t *testing.T) {
	bodies := []string{
		`{"status":"failed","error":"private-provider-secret"}`,
		`{"status":"running"}`,
		`{"status":"succeeded","output":{"version":1,"action":"apply","provider":"other","key":"wrong"}}`,
		`not-json-private-provider-secret`,
	}
	// Every failed execution must remain a single uncertain call.
	for _, body := range bodies {
		calls := 0
		// Return provider diagnostics to prove the adapter does not expose them.
		transport := transportFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		client := Client{Config: executorConfig(), HTTP: &http.Client{Transport: transport}}
		_, err := client.Call(context.Background(), "private-execution-token", Input{Action: "apply", Provider: "aiven", Key: "owned"})
		// Public errors must not echo the Engine's private diagnostic payload.
		if err == nil || calls != 1 || strings.Contains(err.Error(), "private-provider-secret") {
			t.Fatal("unsafe retry or diagnostic leak", err)
		}
	}
}

// TestInvalidEngineOriginsAndModesFailBeforeHTTP guards credential destinations and provider capability validation.
func TestInvalidEngineOriginsAndModesFailBeforeHTTP(t *testing.T) {
	// Insecure origins and alternate request paths cannot receive credentials.
	for _, origin := range []string{"http://engine.example", "https://token@engine.example", "https://engine.example/other", "https://engine.example?token=value"} {
		config := executorConfig()
		config.EngineURL = origin
		// Server-side installation must reject invalid origins.
		if config.Validate() == nil {
			t.Fatal("invalid origin accepted")
		}
	}
	calls := 0
	// This transport must never run for locally unsupported configurations.
	transport := transportFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected network") })
	p := Package{Provider: "railway", Client: Client{Config: executorConfig(), HTTP: &http.Client{Transport: transport}}}
	req := requestFixture("railway")
	req.Step.Config["engine"] = "redis"
	// Redis namespaces must never masquerade as a named application database.
	if _, err := p.Handle(context.Background(), req); err == nil || calls != 0 {
		t.Fatal("unsupported mode reached Engine")
	}
}

// TestEngineHTTPFailuresNeverRetry covers redirects, throttling and unavailable Engine responses.
func TestEngineHTTPFailuresNeverRetry(t *testing.T) {
	// Each response could represent an uncertain remote mutation, so no retry is safe by default.
	for _, status := range []int{302, 401, 429, 500} {
		calls := 0
		// A redirect points at another origin to prove credentials are not forwarded.
		transport := transportFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://other.example"}}, Body: io.NopCloser(strings.NewReader("private-response"))}, nil
		})
		client := Client{Config: executorConfig(), HTTP: &http.Client{Transport: transport}}
		_, err := client.Call(context.Background(), "private-execution-token", Input{Action: "apply", Provider: "aiven", Key: "owned"})
		// Exactly one request is permitted even for provider-directed retry or redirect responses.
		if err == nil || calls != 1 || strings.Contains(err.Error(), "private-response") {
			t.Fatal("unsafe HTTP retry, redirect or diagnostics", status, calls, err)
		}
	}
}

// TestManagedProviderAuthenticationRejectsUnusedCredentials ensures provider tokens stay in Fused buckets.
func TestManagedProviderAuthenticationRejectsUnusedCredentials(t *testing.T) {
	p := Package{Provider: "aiven"}
	req := requestFixture("aiven")
	req.Step.Secrets["providerToken"] = "unexpected-provider-token"
	// Unknown private aliases cannot silently cross the package boundary.
	if p.validateDeployment(req) == nil {
		t.Fatal("unused provider token accepted")
	}
	delete(req.Step.Secrets, "providerToken")
	req.Step.Config["mode"] = "new-instance"
	req.Step.Config["instance"] = map[string]any{"project": "project", "service": "service", "plan": "plan", "tokenSecret": "provider-token"}
	// Authentication references cannot be passed as ordinary hosted deployment fields.
	if p.validateDeployment(req) == nil {
		t.Fatal("inline provider authentication accepted")
	}
}
