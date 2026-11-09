package providers

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

// transportFunc injects HTTP fixtures without opening network listeners.
type transportFunc func(*http.Request) (*http.Response, error)

// RoundTrip executes one provider contract fixture.
func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// request constructs an explicitly scoped direct Aiven service request.
func request() workflow.Request {
	return workflow.Request{Version: 1, Action: "plan", Key: "host/app/db", Step: workflow.Step{ID: "db", Package: "aiven", Config: map[string]any{"engine": "postgres", "mode": "new-instance", "instance": map[string]any{"project": "project", "service": "mydb", "plan": "startup-4", "cloud": "aws-eu-west-1"}}, Secrets: map[string]string{"apiToken": "aiven-token"}}, Secrets: map[string]string{"apiToken": "sentinel-token"}}
}

// fixture returns a bounded HTTP response containing one official API response shape.
func fixture(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// testProvider supplies authoritative preflight, create and readiness responses.
func testProvider(t *testing.T, req workflow.Request, posts *int, created *bool) Package {
	t.Helper()
	// Assert the fixture preserves the provider safety contract.
	return Package{Provider: "aiven", Client: Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		assertDestination(t, r)
		path := r.URL.Path
		// Assert the fixture preserves the provider safety contract.
		if strings.Contains(path, "pricing/") {
			return fixture(200, `{"base_price_usd":"0.2","cloud_name":"aws-eu-west-1","service_plan":"startup-4","service_type":"pg"}`), nil
		}
		// Assert the fixture preserves the provider safety contract.
		if strings.Contains(path, "service-types/") {
			return fixture(200, `{"disk_space_mb":1024,"node_count":1,"service_plan":"startup-4","service_type":"pg"}`), nil
		}
		// Assert the fixture preserves the provider safety contract.
		if strings.HasSuffix(path, "kms/ca") {
			return fixture(200, `{"certificate":"fixture-ca"}`), nil
		}
		// Assert the fixture preserves the provider safety contract.
		if r.Method == http.MethodPost {
			*posts++
			assertCreate(t, r, req)
			*created = true
		}
		// Assert the fixture preserves the provider safety contract.
		if !*created {
			return fixture(404, `{}`), nil
		}
		body, _ := json.Marshal(map[string]any{"service": service{Name: "mydb", Type: "pg", Created: "2026-10-29T01:02:03Z", URI: "postgres://avnadmin:secret@db.example:1234/defaultdb?sslmode=require", State: "RUNNING", Cloud: "aws-eu-west-1", Plan: "startup-4", Tags: map[string]string{"hostlier-owner": owner(req)}}})
		return fixture(200, string(body)), nil
	})}}
}

// TestAivenPlanApply verifies read-only planning and the non-bindable administrative output contract.
func TestAivenPlanApply(t *testing.T) {
	req := request()
	posts := 0
	created := false
	p := testProvider(t, req, &posts, &created)
	plan, err := p.Handle(context.Background(), req)
	// Assert the fixture preserves the provider safety contract.
	if err != nil {
		t.Fatal(err)
	}
	// Assert the fixture preserves the provider safety contract.
	if posts != 0 || plan.Prepared == nil {
		t.Fatal("plan mutated provider")
	}
	req.Action = "apply"
	req.Prepared = plan.Prepared.Payload
	response, err := p.Handle(context.Background(), req)
	// Assert the fixture preserves the provider safety contract.
	if err != nil {
		t.Fatal(err)
	}
	// Assert the fixture preserves the provider safety contract.
	if posts != 1 || response.Result == nil {
		t.Fatal("expected single create")
	}
	assertPrivateResult(t, response)
	req.Previous = response.Result.State
	req.Action = "plan"
	req.Prepared = nil
	plan, err = p.Handle(context.Background(), req)
	// Assert the fixture preserves the provider safety contract.
	if err != nil {
		t.Fatal(err)
	}
	req.Action = "apply"
	req.Prepared = plan.Prepared.Payload
	// Assert the fixture preserves the provider safety contract.
	if _, err = p.Handle(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Assert the fixture preserves the provider safety contract.
	if posts != 1 {
		t.Fatal("owned redeploy created another service")
	}
}

// TestRefusesAdoption ensures names and matching tags never authorize taking over resources.
func TestRefusesAdoption(t *testing.T) {
	req := request()
	posts := 0
	created := true
	p := testProvider(t, req, &posts, &created)
	// Assert the fixture preserves the provider safety contract.
	if _, err := p.Handle(context.Background(), req); err == nil {
		t.Fatal("unowned service adopted")
	}
	// Assert the fixture preserves the provider safety contract.
	if posts != 0 {
		t.Fatal("adoption mutated provider")
	}
}

// TestAivenRejectsChangedApproval prevents mutation after placement or pricing drift.
func TestAivenRejectsChangedApproval(t *testing.T) {
	req := request()
	posts := 0
	created := false
	p := testProvider(t, req, &posts, &created)
	plan, err := p.Handle(context.Background(), req)
	// Assert the fixture preserves the provider safety contract.
	if err != nil {
		t.Fatal(err)
	}
	req.Action = "apply"
	req.Prepared = plan.Prepared.Payload
	req.Key = "host/another/db"
	// Assert the fixture preserves the provider safety contract.
	if _, err = p.Handle(context.Background(), req); err == nil {
		t.Fatal("changed approval accepted")
	}
	// Assert the fixture preserves the provider safety contract.
	if posts != 0 {
		t.Fatal("changed approval mutated provider")
	}
}

// TestUnsafeRailwayTemplateFailsBeforeNetwork protects templates lacking verified TLS guarantees.
func TestUnsafeRailwayTemplateFailsBeforeNetwork(t *testing.T) {
	req := request()
	req.Step.Package = "railway"
	// Assert the fixture preserves the provider safety contract.
	p := Package{Provider: "railway", Client: Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsafe template reached network")
		return nil, errors.New("unexpected network")
	})}}
	// Assert the fixture preserves the provider safety contract.
	if _, err := p.Handle(context.Background(), req); err == nil {
		t.Fatal("unverified template accepted")
	}
}

// TestProviderErrorsNeverExposeResponses bounds error messages even when APIs echo secrets.
func TestProviderErrorsNeverExposeResponses(t *testing.T) {
	// Assert the fixture preserves the provider safety contract.
	c := Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return fixture(401, `{"message":"sentinel-token postgres://admin:secret@db"}`), nil
	})}
	_, err := c.call(context.Background(), http.MethodGet, "/project/a/service", "sentinel-token", nil, new(any))
	// Assert the fixture preserves the provider safety contract.
	if err == nil || strings.Contains(err.Error(), "sentinel") || strings.Contains(err.Error(), "secret") {
		t.Fatal("provider credentials leaked")
	}
}

// TestServeRejectsMultipleDocuments keeps the executable protocol unambiguous.
func TestServeRejectsMultipleDocuments(t *testing.T) {
	// Assert the fixture preserves the provider safety contract.
	if err := (Package{Provider: "aiven"}).Serve(context.Background(), strings.NewReader(`{} {}`), io.Discard); err == nil {
		t.Fatal("multiple documents accepted")
	}
}

// assertDestination prevents fixtures from silently accepting credential forwarding.
func assertDestination(t *testing.T, r *http.Request) {
	t.Helper()
	// Assert the fixture preserves the provider safety contract.
	if r.URL.Scheme != "https" || r.URL.Host != "api.aiven.io" || r.Header.Get("Authorization") != "aivenv1 sentinel-token" {
		t.Fatalf("unexpected credential destination: %s", r.URL)
	}
}

// assertCreate checks the documented mutation shape and persistent owner metadata.
func assertCreate(t *testing.T, r *http.Request, req workflow.Request) {
	t.Helper()
	var body map[string]any
	// Assert the fixture preserves the provider safety contract.
	if json.NewDecoder(r.Body).Decode(&body) != nil || body["service_type"] != "pg" || body["termination_protection"] != true {
		t.Fatal("incorrect create contract")
	}
	// Assert the fixture preserves the provider safety contract.
	if body["tags"].(map[string]any)["hostlier-owner"] != owner(req) {
		t.Fatal("missing ownership tag")
	}
}

// assertPrivateResult proves administrative credentials cannot become application bindings.
func assertPrivateResult(t *testing.T, response workflow.Response) {
	t.Helper()
	// Assert the fixture preserves the provider safety contract.
	if response.Result.Outputs["url"] != "" || response.Result.Outputs["connection"] != "" {
		t.Fatal("administrative credentials became app outputs")
	}
	// Assert the fixture preserves the provider safety contract.
	if !strings.Contains(response.Result.PrivateOutputs["connection"], "sslmode=verify-full") {
		t.Fatal("missing verified TLS")
	}
	// Assert the fixture preserves the provider safety contract.
	if response.Result.PrivateOutputs["caCertificatePEM"] == "" {
		t.Fatal("missing protected CA")
	}
}

// TestUncertainCreateIsNeverRetried verifies one network failure cannot duplicate a paid service.
func TestUncertainCreateIsNeverRetried(t *testing.T) {
	req := request()
	posts := 0
	created := false
	p := testProvider(t, req, &posts, &created)
	plan, err := p.Handle(context.Background(), req)
	// A valid read-only plan is required before simulating mutation uncertainty.
	if err != nil {
		t.Fatal(err)
	}
	original := p.Client.Transport
	// Simulate a connection loss after the provider accepted the create request.
	p.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		// Only the mutating request fails; no fixture may retry it implicitly.
		if r.Method == http.MethodPost {
			posts++
			return nil, errors.New("uncertain mutation containing sentinel-token")
		}
		return original.RoundTrip(r)
	})
	req.Action = "apply"
	req.Prepared = plan.Prepared.Payload
	_, err = p.Handle(context.Background(), req)
	// The controller receives a safe error and retains its uncertain operation marker.
	if err == nil || posts != 1 || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("unsafe uncertain-create behavior: calls=%d error=%v", posts, err)
	}
}

// TestRedirectDoesNotForwardToken protects the fixed provider credential destination.
func TestRedirectDoesNotForwardToken(t *testing.T) {
	calls := 0
	// Return a cross-origin redirect instead of the expected provider response.
	c := Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		r := fixture(302, `{}`)
		r.Header.Set("Location", "https://untrusted.example/collect")
		return r, nil
	})}
	_, err := c.call(context.Background(), http.MethodGet, "/project/a/service", "sentinel-token", nil, new(any))
	// No request is dispatched to the redirect target.
	if err == nil || calls != 1 {
		t.Fatal("provider redirect followed")
	}
}

// TestReadinessCancellationDoesNotCreate verifies bounded readiness exits without another mutation.
func TestReadinessCancellationDoesNotCreate(t *testing.T) {
	req := request()
	posts := 0
	// Keep the service pending so cancellation must end readiness polling.
	p := Package{Provider: "aiven", Client: Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		// Readiness is strictly read-only, regardless of cancellation.
		if r.Method != http.MethodGet {
			posts++
			t.Fatal("readiness attempted mutation")
		}
		body, _ := json.Marshal(map[string]any{"service": service{Name: "mydb", Type: "pg", Created: "created", State: "REBUILDING", Cloud: "aws-eu-west-1", Plan: "startup-4", Tags: map[string]string{"hostlier-owner": owner(req)}}})
		return fixture(200, string(body)), nil
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.waitReady(ctx, req, config{Engine: "postgres", Instance: &instance{Project: "project", Service: "mydb", Plan: "startup-4", Cloud: "aws-eu-west-1"}}, owned{Name: "mydb", Created: "created", Owner: owner(req)})
	// A canceled readiness wait must return a fixed error without creating resources.
	if err == nil || posts != 0 {
		t.Fatal("readiness cancellation did not stop safely")
	}
}
