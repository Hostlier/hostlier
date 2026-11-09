package config

import (
	"bytes"
	"encoding/json"
	"hostlier/internal/model"
)

// endpointReservations rejects duplicate routes and incompatible aliases of one socket.
type endpointReservations struct {
	routes map[string]bool
	ports  map[int]string
}

// normalizeEndpoints validates named listeners before lowering primary compatibility fields.
func normalizeEndpoints(p model.Project) (model.Project, error) {
	// Empty maps retain the original singular endpoint contract.
	if len(p.Endpoints) == 0 {
		return p, validateEmptyEndpointMap(p)
	}
	primary, err := selectedEndpoint(p)
	// Invalid selection cannot fall back to a different network exposure.
	if err != nil {
		return p, err
	}
	reservations := endpointReservations{map[string]bool{}, map[int]string{}}
	// Stable order produces deterministic canonical approval inputs.
	for _, name := range model.EndpointNames(p) {
		e, err := normalizeNamedEndpoint(p, name, p.Endpoints[name])
		// Reject the whole map on its first invalid listener.
		if err != nil {
			return p, err
		}
		// Socket aliases must agree on their actual upstream transport.
		if err = reservations.add(e); err != nil {
			return p, err
		}
		p.Endpoints[name] = e
	}
	// Explicit permissions cannot be intercepted by a more specific sibling endpoint.
	if err := validatePolicyOwnership(p); err != nil {
		return p, err
	}
	e := p.Endpoints[primary]
	// Primary exposure and legacy fields must remain unambiguous.
	if err = validatePrimaryEndpoint(p, e); err != nil {
		return p, err
	}
	p.PrimaryEndpoint = primary
	p.Endpoint = &e
	p.Port = e.Port
	p.Route = e.Route
	return p, nil
}

// validateEmptyEndpointMap rejects dangling primary names without changing legacy input.
func validateEmptyEndpointMap(p model.Project) error {
	// An explicit selector must name a real listener.
	if p.PrimaryEndpoint != "" {
		return invalid("primaryEndpoint requires endpoints")
	}
	return nil
}

// selectedEndpoint bounds the map and returns its canonical primary identity.
func selectedEndpoint(p model.Project) (string, error) {
	// Listenerless workers cannot request inbound socket ownership.
	if len(p.Endpoints) > 16 || p.Workload == "worker" {
		return "", invalid("Named endpoints require a service and at most 16 listeners")
	}
	name := model.PrimaryEndpointName(p)
	// Never silently replace a misspelled primary endpoint.
	if _, ok := p.Endpoints[name]; !ok {
		return "", invalid("primaryEndpoint does not exist")
	}
	return name, nil
}

// add reserves canonical host paths and the wire transport used by shared port aliases.
func (r *endpointReservations) add(e model.Endpoint) error {
	scheme := model.EndpointScheme(&e)
	// Duplicate public routes and contradictory socket transports are ambiguous authority.
	if r.ports[e.Port] != "" && r.ports[e.Port] != scheme || e.Route != "" && r.routes[e.Route] {
		return invalid("Duplicate endpoint route or incompatible port alias")
	}
	r.ports[e.Port] = scheme
	r.routes[e.Route] = true
	return nil
}

// validatePrimaryEndpoint keeps the existing URL/readiness contract consistent with named declarations.
func validatePrimaryEndpoint(p model.Project, e model.Endpoint) error {
	// A group with public routes needs a public primary compatibility URL.
	for _, endpoint := range p.Endpoints {
		// Any public secondary requires a publicly routed primary URL.
		if endpoint.Exposure == "public" && e.Exposure != "public" {
			return invalid("Select a public primaryEndpoint for a publicly routed workload")
		}
	}
	return validateEndpointCompatibility(p, e)
}

// normalizeNamedEndpoint applies routing and transport policy independently to every listener.
func normalizeNamedEndpoint(p model.Project, name string, e model.Endpoint) (model.Endpoint, error) {
	// Named listeners always carry explicit transport semantics, including exact resource paths.
	if e.Protocol == "" {
		e.Protocol = "http"
	}
	// Names and public method paths must have one explicit interpretation.
	if err := validateNamedEndpointPolicy(p, name, e); err != nil {
		return e, err
	}
	copy := p
	copy.Endpoints = nil
	copy.Endpoint = &e
	copy.Port = e.Port
	copy.Route = e.Route
	// Existing exposure and consumer boundaries apply to every named listener.
	if err := validateEndpoint(copy); err != nil {
		return e, err
	}
	// Canonical URLs are used for every reservation and approval fingerprint.
	if e.Route != "" {
		route, err := ParseRoute(e.Route, nil, "")
		// Invalid secondary addresses cannot enter the canonical route map.
		if err != nil {
			return e, err
		}
		e.Route = route.URL
	}
	return e, validateEndpointTransport(e)
}

// validateNamedEndpointPolicy rejects missing addresses and gRPC path rewrites before normalization.
func validateNamedEndpointPolicy(p model.Project, name string, e model.Endpoint) error {
	// Listener names become portable service and route identities.
	if !nameRE.MatchString(name) {
		return invalid("Invalid endpoint name")
	}
	// Only the primary can request interactive address selection in the CLI.
	if e.Exposure == "public" && e.Route == "" && name != model.PrimaryEndpointName(p) {
		return invalid("Secondary public endpoints require a route")
	}
	// A gRPC method path is part of its public protocol identity.
	if nativeGRPC(e) && p.Proxy != nil && p.Proxy.StripPrefix {
		return invalid("gRPC endpoints cannot strip method path prefixes")
	}
	// Private endpoint discovery currently retains the primary-only binding contract.
	if e.Exposure == "private" && name != model.PrimaryEndpointName(p) {
		return invalid("Private endpoint bindings require the primaryEndpoint; secondary endpoints support public or loopback exposure")
	}
	return nil
}

// ValidateEndpointTransport exposes renderer validation without normalizing an entire project.
func ValidateEndpointTransport(e model.Endpoint) error { return validateEndpointTransport(e) }

// validateEndpointTransport separates upstream wire protocols from client-facing HTTP versions.
func validateEndpointTransport(e model.Endpoint) error {
	// Application-level permissions are validated before any transport can be rendered.
	if err := validateTrafficPolicy(e); err != nil {
		return err
	}
	protocols := map[string]bool{"": true, "http": true, "https": true, "h2c": true, "grpc": true, "grpcs": true}
	// Unknown protocols never silently fall back to ordinary HTTP/1.
	if !protocols[e.Protocol] || e.IdleTimeoutSeconds < 0 || e.IdleTimeoutSeconds > 86400 {
		return invalid("Unsupported endpoint protocol or idle timeout")
	}
	return validateEndpointPeer(e)
}

// validateEndpointPeer checks certificate identity and upgrades independently of application permissions.
func validateEndpointPeer(e model.Endpoint) error {
	// Encrypted upstream identities cannot inject renderer configuration.
	if e.UpstreamServerName != "" {
		// Reject malformed upstream certificate identities before rendering.
		if _, err := NormalizeDomain(e.UpstreamServerName); err != nil {
			return err
		}
	}
	// A native gRPC listener cannot advertise a WebSocket upgrade on that socket.
	if (e.Protocol == "grpc" || e.Protocol == "grpcs") && e.WebSocket != nil && *e.WebSocket {
		return invalid("WebSocket upgrade applies to HTTP endpoints")
	}
	return validateHTTPVersions(e.HTTPVersions)
}

// validateHTTPVersions bounds edge transport declarations and rejects duplicate capabilities.
func validateHTTPVersions(versions []string) error {
	seen := map[string]bool{}
	// HTTP/3 is a client-to-edge capability, independent from an upstream's protocol.
	for _, version := range versions {
		// Unknown or repeated edge versions cannot be advertised.
		if seen[version] || version != "h1" && version != "h2" && version != "h3" {
			return invalid("httpVersions supports unique h1, h2 and h3")
		}
		seen[version] = true
	}
	return nil
}

// nativeGRPC identifies transports whose method paths must not be rewritten.
func nativeGRPC(e model.Endpoint) bool { return e.Protocol == "grpc" || e.Protocol == "grpcs" }

// validateEndpointCompatibility rejects contradictory legacy fields while preserving canonical revalidation.
func validateEndpointCompatibility(p model.Project, e model.Endpoint) error {
	// An authored legacy port cannot silently select a different listener.
	if p.Port != 0 && p.Port != e.Port {
		return invalid("Conflicting primary endpoint port")
	}
	// An authored legacy route must identify the same canonical public address.
	if p.Route != "" {
		route, err := ParseRoute(p.Route, nil, "")
		// Invalid or contradictory legacy addresses cannot be discarded during normalization.
		if err != nil || route.URL != e.Route {
			return invalid("Conflicting primary endpoint route")
		}
	}
	// Internal normalized projects contain both representations and must remain idempotent.
	if p.Endpoint != nil {
		singular := *p.Endpoint
		// Hostname-only authored routes and normalized HTTPS routes have the same identity.
		if singular.Route != "" {
			route, err := ParseRoute(singular.Route, nil, "")
			// Invalid singular route input cannot be hidden by a valid named endpoint.
			if err != nil {
				return err
			}
			singular.Route = route.URL
		}
		left, _ := json.Marshal(singular)
		right, _ := json.Marshal(e)
		// Every typed transport setting participates in the compatibility check.
		if !bytes.Equal(left, right) {
			return invalid("Conflicting singular and named endpoints")
		}
	}
	return nil
}
