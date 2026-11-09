package config

import (
	"hostlier/internal/model"
	"strings"
	"testing"
)

// endpointProject models an HTTP application with a separate native gRPC listener.
func endpointProject() model.Project {
	return model.Project{Version: 2, Name: "engine", Source: &model.Source{Image: "example/engine:1"}, Endpoints: map[string]model.Endpoint{
		"http": {Port: 8081, Exposure: "public", Route: "engine.example.com", Protocol: "http", HTTPVersions: []string{"h1", "h2", "h3"}},
		"grpc": {Port: 50051, Exposure: "public", Route: "grpc.example.com", Protocol: "grpc"},
	}, Configs: map[string]model.ConfigFile{"engine": {Mount: "/etc/fused/engine.yaml", Content: "server:\n  http_port: 8081\n"}}, Health: &model.Health{Path: "/health"}}
}

// TestNamedEndpointsRemainStableAcrossApprovalValidation covers canonicalization and compatibility.
func TestNamedEndpointsRemainStableAcrossApprovalValidation(t *testing.T) {
	p, err := ValidateProject(endpointProject())
	// The HTTP listener owns readiness and the legacy URL independently from gRPC naming order.
	if err != nil || p.Port != 8081 || p.PrimaryEndpoint != "http" {
		t.Fatalf("%+v %v", p, err)
	}
	again, err := ValidateProject(p)
	// Revalidation cannot alter the signed listener map or file mode defaults.
	if err != nil || Fingerprint(p) != Fingerprint(again) || p.Configs["engine"].Mode != 0444 {
		t.Fatalf("non-idempotent normalization %v", err)
	}
}

// TestEndpointAndConfigValidationRejectsAmbiguity exercises the actual schema validation boundary.
func TestEndpointAndConfigValidationRejectsAmbiguity(t *testing.T) {
	cases := map[string]func(*model.Project){
		// Construct the unknown protocol rejection case.
		"unknown protocol": func(p *model.Project) { e := p.Endpoints["grpc"]; e.Protocol = "raw"; p.Endpoints["grpc"] = e },
		// Construct the unknown HTTP version rejection case.
		"unknown HTTP version": func(p *model.Project) {
			e := p.Endpoints["http"]
			e.HTTPVersions = []string{"h4"}
			p.Endpoints["http"] = e
		},
		// Construct the duplicate route rejection case.
		"duplicate route": func(p *model.Project) {
			e := p.Endpoints["grpc"]
			e.Route = "engine.example.com"
			p.Endpoints["grpc"] = e
		},
		// A legacy port cannot contradict the selected named listener.
		"conflicting primary port": func(p *model.Project) { p.Port = 12345 },
		// A legacy URL cannot be discarded during named normalization.
		"conflicting primary route": func(p *model.Project) { p.Route = "other.example.com" },
		// Mixed declarations must agree on their actual upstream transport.
		"conflicting singular transport": func(p *model.Project) { e := p.Endpoints["http"]; e.Protocol = "https"; p.Endpoint = &e },
		// Every sidecar listener shares the parent network namespace's port reservations.
		"sidecar secondary port conflict": func(p *model.Project) {
			p.Sidecars = map[string]model.Project{"agent": {Source: &model.Source{Image: "example/agent:1"}, PrimaryEndpoint: "main", Endpoints: map[string]model.Endpoint{"main": {Port: 1907, Exposure: "loopback"}, "metrics": {Port: 8081, Exposure: "loopback"}}, Health: &model.Health{Exec: []string{"true"}}}}
		},
		// Construct the missing primary rejection case.
		"missing primary": func(p *model.Project) { p.PrimaryEndpoint = "missing" },
		// Construct the config traversal rejection case.
		"config traversal": func(p *model.Project) { p.Configs["engine"] = model.ConfigFile{Mount: "/etc/../secret", Content: "x"} },
		// Construct the config source unresolved rejection case.
		"config source unresolved": func(p *model.Project) {
			p.Configs["engine"] = model.ConfigFile{Mount: "/etc/engine", File: "engine.yaml"}
		},
		// Construct the config writable rejection case.
		"config writable": func(p *model.Project) { p.Configs["engine"] = model.ConfigFile{Mount: "/etc/engine", Mode: 0644} },
		// Construct the config too large rejection case.
		"config too large": func(p *model.Project) {
			p.Configs["engine"] = model.ConfigFile{Mount: "/etc/engine", Content: strings.Repeat("x", 65537)}
		},
		// Construct the config hides volume rejection case.
		"config hides volume": func(p *model.Project) {
			p.Volumes = map[string]model.Volume{"data": {Mount: "/etc/fused", Size: "1Gi"}}
		},
	}
	// Each mutation is applied to a new input so failures remain independent.
	for name, change := range cases {
		// Validate each mutation through the full public schema boundary.
		t.Run(name, func(t *testing.T) {
			p := endpointProject()
			change(&p)
			// Every ambiguous declaration must fail before approval.
			if _, err := ValidateProject(p); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
}
