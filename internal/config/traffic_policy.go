package config

import (
	"hostlier/internal/model"
	"regexp"
	"slices"
	"strings"
)

var grpcServiceRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*\.)*[A-Za-z_][A-Za-z0-9_]*$`)
var grpcMethodRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateTrafficPolicy rejects policies that an endpoint cannot actually enforce.
func validateTrafficPolicy(e model.Endpoint) error {
	// Both protocol families cannot own the same endpoint's admission decision.
	if e.GRPC != nil && e.HTTP != nil {
		return invalid("Use either grpc or http policy on an endpoint")
	}
	// Permissions are edge policies; private listeners have no enforcing edge.
	if e.GRPC == nil && e.HTTP == nil {
		return nil
	}
	// A false claim of enforcement is worse than rejecting a private policy.
	if e.Exposure != "public" {
		return invalid("Traffic policies require a public endpoint")
	}
	route, err := ParseRoute(e.Route, nil, "")
	// The enclosing route remains the complete ownership and denial boundary.
	if err != nil {
		return err
	}
	// Native RPC permissions are never applied to ordinary HTTP proxying.
	if e.GRPC != nil {
		return validateGRPCPolicy(e, route.Path)
	}
	return validateHTTPPolicy(e, route.Path)
}

// validateGRPCPolicy bounds configuration size and validates every protobuf identity before rendering.
func validateGRPCPolicy(e model.Endpoint, scope string) error {
	// Empty policies fail closed instead of accidentally exposing every registered service.
	if !nativeGRPC(e) || len(e.GRPC.Services) == 0 || len(e.GRPC.Services) > 32 {
		return invalid("grpc policy requires native gRPC and 1..32 services")
	}
	total := 0
	// Service names use protobuf identifiers, never proxy syntax or URL rewrites.
	for name, service := range e.GRPC.Services {
		// A bounded explicit method map makes unknown and newly added RPCs default-deny.
		if err := validateGRPCService(name, service); err != nil {
			return err
		}
		// Each exact method must remain inside the endpoint's reserved path scope.
		for method, policy := range service.Methods {
			// Wildcards, nested paths and renderer fragments are not protobuf method names.
			if err := validateGRPCMethod(name, method, policy, scope); err != nil {
				return err
			}
			total++
		}
	}
	// One endpoint cannot expand an otherwise small declaration into unbounded edge rules.
	if total > 256 {
		return invalid("gRPC policy permits at most 256 methods per endpoint")
	}
	return nil
}

// validateGRPCService bounds one explicit service permission without accepting proxy syntax.
func validateGRPCService(name string, service model.GRPCServicePolicy) error {
	// Newly added RPCs stay denied until their exact names are included in a reviewed deployment.
	if len(name) > 256 || !grpcServiceRE.MatchString(name) || len(service.Methods) == 0 || len(service.Methods) > 64 || !validIdleTimeout(service.IdleTimeoutSeconds) {
		return invalid("gRPC services require a valid name, 1..64 methods and a bounded idle timeout")
	}
	return nil
}

// validateGRPCMethod preserves canonical RPC identity and the enclosing route reservation.
func validateGRPCMethod(service, method string, policy model.GRPCMethodPolicy, scope string) error {
	// Method names cannot contain wildcards, nested paths or configuration fragments.
	if len(method) > 128 || !grpcMethodRE.MatchString(method) || !withinTrafficScope("/"+service+"/"+method, scope) || !validIdleTimeout(policy.IdleTimeoutSeconds) {
		return invalid("Invalid or out-of-scope gRPC method policy")
	}
	return nil
}

// validateHTTPPolicy gives every exact application path one unambiguous transport policy.
func validateHTTPPolicy(e model.Endpoint, scope string) error {
	// HTTP policies cannot translate native gRPC or hide an upstream protocol mismatch.
	if nativeGRPC(e) || len(e.HTTP.Routes) == 0 || len(e.HTTP.Routes) > 64 {
		return invalid("http policy requires an HTTP upstream and 1..64 routes")
	}
	seen := map[string]bool{}
	// An exact path belongs to one rule so declaration order cannot widen permission.
	for name, rule := range e.HTTP.Routes {
		// Names and paths are bounded independently of arbitrary request data.
		if !nameRE.MatchString(name) || seen[rule.Path] || !withinTrafficScope(rule.Path, scope) {
			return invalid("Invalid, duplicate or out-of-scope HTTP policy route")
		}
		// Strict canonical paths prevent escapes, wildcards and traversal from altering policy matching.
		if err := validateHTTPPolicyRoute(e, rule); err != nil {
			return err
		}
		seen[rule.Path] = true
	}
	return nil
}

// validateHTTPPolicyRoute checks client protocol requirements and transport-specific method support.
func validateHTTPPolicyRoute(e model.Endpoint, rule model.HTTPRoutePolicy) error {
	parsed, err := ParseRoute("https://policy.invalid"+rule.Path, nil, "")
	// Paths must be absolute, canonical and case-preserving without query strings or fragments.
	if err != nil || !strings.HasPrefix(rule.Path, "/") || parsed.Path != rule.Path || !validIdleTimeout(rule.IdleTimeoutSeconds) {
		return invalid("HTTP policy paths must be canonical absolute paths with bounded idle timeouts")
	}
	// Every selected public protocol must be supported by the proxy adapter.
	if err := validateHTTPVersions(rule.HTTPVersions); err != nil {
		return err
	}
	// Explicit buffering or upgrade denial cannot contradict a streaming route.
	if err := validateHTTPPolicyStreaming(e, rule); err != nil {
		return err
	}
	return validateHTTPPolicyMethods(rule)
}

// validateHTTPPolicyStreaming refuses configurations that would break the declared application transport.
func validateHTTPPolicyStreaming(e model.Endpoint, rule model.HTTPRoutePolicy) error {
	// WebSocket handshakes must agree with their explicitly permitted wire methods.
	if rule.Protocol == "websocket" {
		return validateWebSocketPolicy(e, rule)
	}
	// SSE and Streamable HTTP must flush data while the request remains active.
	if (rule.Protocol == "sse" || rule.Protocol == "streamable-http") && e.Streaming != nil && !*e.Streaming {
		return invalid("Streaming routes cannot disable endpoint streaming")
	}
	return nil
}

// validateWebSocketPolicy binds HTTP/1 Upgrade and extended CONNECT to explicit method permissions.
func validateWebSocketPolicy(e model.Endpoint, rule model.HTTPRoutePolicy) error {
	// An h2c-only upstream or disabled upgrade cannot satisfy the HTTP/1 handshake.
	if e.Protocol == "h2c" || e.WebSocket != nil && !*e.WebSocket {
		return invalid("WebSocket policy requires an upgrade-enabled HTTP upstream")
	}
	versions := rule.HTTPVersions
	// Existing GET-only routes retain their HTTP/1 handshake without widening access.
	if len(versions) == 0 {
		versions = []string{"h1"}
	}
	h1 := slices.Contains(versions, "h1")
	extended := slices.Contains(versions, "h2") || slices.Contains(versions, "h3")
	// Every advertised handshake needs its actual method, with no extra method silently admitted.
	if h1 != slices.Contains(rule.Methods, "GET") || extended != slices.Contains(rule.Methods, "CONNECT") {
		return invalid("WebSocket h1 requires GET; h2/h3 require CONNECT and explicit httpVersions")
	}
	return nil
}

// validatePolicyPathRewrite rejects prefix rewriting for both singular and named policy endpoints.
func validatePolicyPathRewrite(p model.Project) error {
	// A policy must evaluate the same public path that the application receives.
	if model.HasTrafficPolicy(p.Endpoint) && p.Proxy != nil && p.Proxy.StripPrefix {
		return invalid("Traffic policies cannot strip path prefixes")
	}
	return nil
}

// validateHTTPPolicyMethods accepts only explicit uppercase verbs meaningful for the declared transport.
func validateHTTPPolicyMethods(rule model.HTTPRoutePolicy) error {
	allowed := map[string]string{"http": " GET HEAD POST PUT PATCH DELETE OPTIONS ", "websocket": " GET CONNECT ", "sse": " GET OPTIONS ", "streamable-http": " GET POST DELETE OPTIONS "}
	verbs, ok := allowed[rule.Protocol]
	// Every rule must state its request contract rather than inherit an unrestricted handler.
	if !ok || len(rule.Methods) == 0 || len(rule.Methods) > 8 {
		return invalid("HTTP policy requires a known protocol and explicit allowed methods")
	}
	seen := map[string]bool{}
	// Reject typos and duplicates instead of broadening a method selector.
	for _, method := range rule.Methods {
		// Matching uses whole tokens so partial strings cannot become HTTP verbs.
		if seen[method] || method == "" || strings.ContainsAny(method, " \t\r\n") || !strings.Contains(verbs, " "+method+" ") {
			return invalid("Unsupported or duplicate HTTP policy method")
		}
		seen[method] = true
	}
	return nil
}

// withinTrafficScope enforces segment boundaries for the endpoint's existing route reservation.
func withinTrafficScope(path, scope string) bool {
	return scope == "/" || path == scope || strings.HasPrefix(path, scope+"/")
}

// validIdleTimeout retains the endpoint's inherited zero and bounded one-day I/O contract.
func validIdleTimeout(value int) bool { return value >= 0 && value <= 86400 }
