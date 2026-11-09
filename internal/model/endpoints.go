package model

import "sort"

// ConfigFile is immutable deployment input; secret contents remain referenced by encrypted revision.
type ConfigFile struct {
	Mount   string `json:"mount" yaml:"mount"`
	File    string `json:"file,omitempty" yaml:"file,omitempty"`
	Content string `json:"content,omitempty" yaml:"content,omitempty"`
	Secret  string `json:"secret,omitempty" yaml:"secret,omitempty"`
	Mode    uint32 `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// EndpointNames keeps named listeners deterministic for approval, mapping and route generation.
func EndpointNames(p Project) []string {
	names := make([]string, 0, len(p.Endpoints))
	// Map iteration never selects a primary listener or changes approval output order.
	for name := range p.Endpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PrimaryEndpointName preserves an explicit selection and prefers conventional HTTP readiness.
func PrimaryEndpointName(p Project) string {
	// An explicitly reviewed primary always wins.
	if p.PrimaryEndpoint != "" {
		return p.PrimaryEndpoint
	}
	// Conventional names avoid probing a gRPC listener using HTTP readiness.
	for _, name := range []string{"http", "main", "default"} {
		// Use the first declared conventional listener name.
		if _, ok := p.Endpoints[name]; ok {
			return name
		}
	}
	names := EndpointNames(p)
	// Empty maps retain the original singular endpoint contract.
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// EndpointProjects lowers each public listener into one independent edge route.
func EndpointProjects(p Project) map[string]Project {
	out := map[string]Project{}
	// Legacy projects continue to render exactly one primary route.
	if len(p.Endpoints) == 0 {
		// Only publicly exposed legacy workloads produce an edge route.
		if Public(p) {
			out[""] = p
		}
		return out
	}
	// Each copied route carries its protocol without mutating the signed project.
	for _, name := range EndpointNames(p) {
		endpoint := p.Endpoints[name]
		// Private endpoints never allocate certificates or public routing.
		if endpoint.Exposure != "public" {
			continue
		}
		copy := p
		copy.Port = endpoint.Port
		copy.Route = endpoint.Route
		copy.Endpoint = &endpoint
		copy.Endpoints = nil
		copy.PrimaryEndpoint = ""
		out[name] = copy
	}
	return out
}

// EndpointScheme describes the actual upstream protocol independently from edge HTTP versions.
func EndpointScheme(e *Endpoint) string {
	// An omitted endpoint retains HTTP/1 compatibility.
	if e == nil {
		return "http"
	}
	// Both native gRPC and generic cleartext HTTP/2 use an h2c transport.
	if e.Protocol == "grpc" || e.Protocol == "h2c" {
		return "h2c"
	}
	// TLS verification remains enabled for encrypted upstreams.
	if e.Protocol == "grpcs" || e.Protocol == "https" {
		return "https"
	}
	return "http"
}

// EndpointPreservesPath avoids redirecting exact RPC and Streamable HTTP addresses to a different resource.
func EndpointPreservesPath(p Project) bool { return p.Endpoint != nil && p.Endpoint.Protocol != "" }
