package config

import (
	"hostlier/internal/model"
	"strings"
	"testing"
)

// grpcPolicyEndpoint supplies an explicitly scoped service with one permitted RPC.
func grpcPolicyEndpoint() model.Endpoint {
	return model.Endpoint{Exposure: "public", Route: "https://grpc.example.com", Port: 50051, Protocol: "grpc", GRPC: &model.GRPCPolicy{Services: map[string]model.GRPCServicePolicy{"engine.v1.EngineService": {IdleTimeoutSeconds: 300, Methods: map[string]model.GRPCMethodPolicy{"Connect": {IdleTimeoutSeconds: 60}}}}}}
}

// TestGRPCPolicyRejectsAmbiguousAndUnenforceablePermissions covers the real configuration boundary.
func TestGRPCPolicyRejectsAmbiguousAndUnenforceablePermissions(t *testing.T) {
	cases := map[string]func(*model.Endpoint){
		// Exercise this invalid declaration independently of the valid fixture.
		"HTTP upstream": func(e *model.Endpoint) { e.Protocol = "http" },
		// Exercise this invalid declaration independently of the valid fixture.
		"private listener": func(e *model.Endpoint) { e.Exposure = "private" },
		// Exercise this invalid declaration independently of the valid fixture.
		"empty policy": func(e *model.Endpoint) { e.GRPC.Services = nil },
		// Exercise this invalid declaration independently of the valid fixture.
		"service injection": func(e *model.Endpoint) { e.GRPC.Services["bad;service"] = e.GRPC.Services["engine.v1.EngineService"] },
		// Exercise this invalid declaration independently of the valid fixture.
		"wildcard method": func(e *model.Endpoint) {
			s := e.GRPC.Services["engine.v1.EngineService"]
			s.Methods["*"] = model.GRPCMethodPolicy{}
		},
		// Exercise this invalid declaration independently of the valid fixture.
		"empty methods": func(e *model.Endpoint) { e.GRPC.Services["empty.Service"] = model.GRPCServicePolicy{} },
		// Exercise this invalid declaration independently of the valid fixture.
		"negative timeout": func(e *model.Endpoint) {
			e.GRPC.Services["bad.Service"] = model.GRPCServicePolicy{IdleTimeoutSeconds: -1, Methods: map[string]model.GRPCMethodPolicy{"Call": {}}}
		},
		// Exercise this invalid declaration independently of the valid fixture.
		"outside scope": func(e *model.Endpoint) { e.Route += "/fused.bridge.v1.BridgeService" },
	}
	// Each malformed policy is isolated from the other mutations.
	for name, change := range cases {
		// Validating renderer input also protects direct edge-rendering callers.
		t.Run(name, func(t *testing.T) {
			e := grpcPolicyEndpoint()
			change(&e)
			// Invalid declarations must fail before plan approval or edge publication.
			if err := ValidateEndpointTransport(e); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

// TestGRPCPolicyIsReviewedAndIdempotent verifies permissions participate in immutable plan identity.
func TestGRPCPolicyIsReviewedAndIdempotent(t *testing.T) {
	p := endpointProject()
	p.Endpoints["grpc"] = grpcPolicyEndpoint()
	first, err := ValidateProject(p)
	// Policy validation preserves the existing deployment schema and primary readiness endpoint.
	if err != nil {
		t.Fatal(err)
	}
	again, err := ValidateProject(first)
	// Revalidation cannot silently widen a retained revision's permissions.
	if err != nil || Fingerprint(first) != Fingerprint(again) {
		t.Fatal("policy normalization changed approval identity", err)
	}
	before := Fingerprint(first)
	e := first.Endpoints["grpc"]
	s := e.GRPC.Services["engine.v1.EngineService"]
	s.Methods["SubscribeWebhooks"] = model.GRPCMethodPolicy{}
	// Adding even one method must invalidate previously reviewed deployment input.
	if before == Fingerprint(first) {
		t.Fatal("method permission missing from fingerprint")
	}
}

// httpPolicyEndpoint models distinct application transports under a single endpoint ownership boundary.
func httpPolicyEndpoint() model.Endpoint {
	return model.Endpoint{Exposure: "public", Route: "https://app.example.com", Port: 8081, Protocol: "http", HTTP: &model.HTTPPolicy{Routes: map[string]model.HTTPRoutePolicy{
		"socket": {Path: "/ws", Methods: []string{"GET"}, Protocol: "websocket"},
		"events": {Path: "/events", Methods: []string{"GET"}, Protocol: "sse", HTTPVersions: []string{"h2", "h3"}},
		"mcp":    {Path: "/mcp", Methods: []string{"GET", "POST", "DELETE"}, Protocol: "streamable-http"},
	}}}
}

// TestHTTPPolicyRejectsAmbiguousTransportContracts prevents successful plans for broken streaming routes.
func TestHTTPPolicyRejectsAmbiguousTransportContracts(t *testing.T) {
	cases := map[string]model.HTTPRoutePolicy{
		"wildcard path":     {Path: "/events/*", Methods: []string{"GET"}, Protocol: "sse"},
		"query path":        {Path: "/events?x=1", Methods: []string{"GET"}, Protocol: "sse"},
		"relative path":     {Path: "events", Methods: []string{"GET"}, Protocol: "sse"},
		"traversal":         {Path: "/a/../events", Methods: []string{"GET"}, Protocol: "sse"},
		"duplicate path":    {Path: "/ws", Methods: []string{"GET"}, Protocol: "http"},
		"SSE upload":        {Path: "/new", Methods: []string{"POST"}, Protocol: "sse"},
		"unsupported verb":  {Path: "/new", Methods: []string{"TRACE"}, Protocol: "http"},
		"missing methods":   {Path: "/new", Protocol: "http"},
		"websocket QUIC":    {Path: "/new", Methods: []string{"GET"}, Protocol: "websocket", HTTPVersions: []string{"h3"}},
		"excessive timeout": {Path: "/new", Methods: []string{"GET"}, Protocol: "http", IdleTimeoutSeconds: 86401},
	}
	// The unmodified rule set must first pass the same production validation boundary.
	if err := ValidateEndpointTransport(httpPolicyEndpoint()); err != nil {
		t.Fatal(err)
	}
	// Every rejected shape must fail independently of other invalid rules.
	for name, rule := range cases {
		// Identify the rejected transport contract by its fixture name.
		t.Run(name, func(t *testing.T) {
			e := httpPolicyEndpoint()
			e.HTTP.Routes["invalid"] = rule
			// Policy configuration cannot silently downgrade the requested protocol contract.
			if err := ValidateEndpointTransport(e); err == nil {
				t.Fatal("invalid HTTP policy accepted")
			}
		})
	}
}

// TestPolicyOwnershipReportsShadowingEndpoint prevents path capture within one deployment.
func TestPolicyOwnershipReportsShadowingEndpoint(t *testing.T) {
	p := endpointProject()
	p.Endpoints["grpc"] = grpcPolicyEndpoint()
	p.Endpoints["other"] = model.Endpoint{Exposure: "public", Port: 8082, Route: "https://grpc.example.com/engine.v1.EngineService", Protocol: "http"}
	_, err := ValidateProject(p)
	// The error must identify both owners and the exact permission that would be intercepted.
	if err == nil || !strings.Contains(err.Error(), `endpoint "other"`) || !strings.Contains(err.Error(), "/engine.v1.EngineService/Connect") {
		t.Fatalf("unhelpful ownership error: %v", err)
	}
}

// TestWebSocketExtendedPoliciesRequireMatchingWireMethods keeps protocol upgrades explicit in approval input.
func TestWebSocketExtendedPoliciesRequireMatchingWireMethods(t *testing.T) {
	// Every supported protocol combination must state the corresponding wire methods.
	for _, rule := range []model.HTTPRoutePolicy{
		{Path: "/ws", Protocol: "websocket", Methods: []string{"CONNECT"}, HTTPVersions: []string{"h2"}},
		{Path: "/ws", Protocol: "websocket", Methods: []string{"CONNECT"}, HTTPVersions: []string{"h3"}},
		{Path: "/ws", Protocol: "websocket", Methods: []string{"GET", "CONNECT"}, HTTPVersions: []string{"h1", "h2", "h3"}},
	} {
		e := httpPolicyEndpoint()
		e.HTTP.Routes["socket"] = rule
		// Valid extended CONNECT declarations retain the shared public endpoint boundary.
		if err := ValidateEndpointTransport(e); err != nil {
			t.Fatal(err)
		}
	}
	e := httpPolicyEndpoint()
	e.HTTP.Routes["socket"] = model.HTTPRoutePolicy{Path: "/ws", Protocol: "websocket", Methods: []string{"CONNECT"}}
	// CONNECT cannot be added to a legacy GET-only route without selecting H2/H3.
	if err := ValidateEndpointTransport(e); err == nil {
		t.Fatal("implicit extended CONNECT accepted")
	}
}
