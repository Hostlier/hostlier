package controller

import (
	"hostlier/internal/model"
	"strings"
	"testing"
)

// TestSecondaryEndpointReservationsIncludePendingOperations guards gRPC routes independently of primary HTTP URLs.
func TestSecondaryEndpointReservationsIncludePendingOperations(t *testing.T) {
	p := model.Project{Version: 2, Name: "engine", Port: 8081, Route: "https://engine.example.com", Endpoint: &model.Endpoint{Exposure: "public", Port: 8081}, Endpoints: map[string]model.Endpoint{"http": {Exposure: "public", Port: 8081, Route: "https://engine.example.com"}, "grpc": {Exposure: "public", Port: 50051, Route: "https://grpc.example.com", Protocol: "grpc"}}}
	c := &Controller{state: State{Domains: []string{"example.com"}, Hostname: "controller.example.com", Operations: map[string]*Operation{"pending": {Kind: "deploy", Status: "queued", Name: p.Name, Spec: p}}}}
	next := model.Project{Version: 1, Name: "other", Route: "https://grpc.example.com/engine.v1.Service"}
	// A pending secondary reserves its complete hostname/path boundary before runtime execution.
	if _, err := c.routeFree(next); err == nil {
		t.Fatal("pending gRPC endpoint lost its reservation")
	}
	next.Name = p.Name
	// Replacing the same application retains ownership of all its existing endpoints.
	if _, err := c.routeFree(next); err != nil {
		t.Fatal(err)
	}
}

// TestRouteConflictIdentifiesPendingEndpointOwner makes path reuse actionable before any deployment mutation.
func TestRouteConflictIdentifiesPendingEndpointOwner(t *testing.T) {
	p := model.Project{Version: 2, Name: "event-service", Endpoints: map[string]model.Endpoint{"events": {Exposure: "public", Port: 8081, Route: "https://app.example.com/events", Protocol: "http"}}}
	c := &Controller{state: State{Domains: []string{"example.com"}, Operations: map[string]*Operation{"pending": {Kind: "deploy", Status: "queued", Name: p.Name, Spec: p}}}}
	next := model.Project{Version: 1, Name: "another-service", Route: "https://app.example.com/events/child"}
	_, err := c.routeFree(next)
	// A pending service holds the entire path scope regardless of the proposed protocol or verb.
	if err == nil {
		t.Fatal("pending endpoint path was available to another service")
	}
	// The user must be told who owns both the requested path and its conflicting parent scope.
	for _, want := range []string{"event-service", `endpoint "events"`, "https://app.example.com/events", "https://app.example.com/events/child"} {
		// Missing ownership data turns a safe rejection into an opaque deployment failure.
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %s: %v", want, err)
		}
	}
}

// TestPlanAcceptsBoundedEscapedConfiguration proves managed files fit the actual strict HTTP request boundary.
func TestPlanAcceptsBoundedEscapedConfiguration(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	p := project()
	p.Version = 2
	p.Source = &model.Source{Image: p.Image}
	p.Endpoint = &model.Endpoint{Exposure: "public", Port: p.Port, Route: p.Route}
	p.Configs = map[string]model.ConfigFile{"one": {Mount: "/app/one", Content: strings.Repeat("\x01", 65536)}, "two": {Mount: "/app/two", Content: strings.Repeat("\x02", 65536)}}
	status, out := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": p}, "")
	// JSON escaping expands these inputs beyond the old 128 KiB request limit.
	if status != 200 {
		t.Fatalf("plan status=%d response=%v", status, out)
	}
}
