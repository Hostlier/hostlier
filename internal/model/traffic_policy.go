package model

import "sort"

// GRPCPolicy permits only explicitly declared protobuf services and methods on an endpoint.
type GRPCPolicy struct {
	Services map[string]GRPCServicePolicy `json:"services" yaml:"services"`
}

// GRPCServicePolicy supplies inherited I/O budgets and an explicit method allowlist.
type GRPCServicePolicy struct {
	IdleTimeoutSeconds int                         `json:"idleTimeoutSeconds,omitempty" yaml:"idleTimeoutSeconds,omitempty"`
	Methods            map[string]GRPCMethodPolicy `json:"methods" yaml:"methods"`
}

// GRPCMethodPolicy overrides the service's idle I/O budget for one exact RPC identity.
type GRPCMethodPolicy struct {
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds,omitempty" yaml:"idleTimeoutSeconds,omitempty"`
}

// GRPCRule is the deterministic edge representation of one reviewed RPC permission.
type GRPCRule struct {
	Path               string
	IdleTimeoutSeconds int
}

// HTTPPolicy permits only declared exact paths, methods and streaming transports.
type HTTPPolicy struct {
	Routes map[string]HTTPRoutePolicy `json:"routes" yaml:"routes"`
}

// HTTPRoutePolicy binds an application transport to an exact URL and allowed client protocols.
type HTTPRoutePolicy struct {
	Path               string   `json:"path" yaml:"path"`
	Methods            []string `json:"methods" yaml:"methods"`
	Protocol           string   `json:"protocol" yaml:"protocol"`
	HTTPVersions       []string `json:"httpVersions,omitempty" yaml:"httpVersions,omitempty"`
	IdleTimeoutSeconds int      `json:"idleTimeoutSeconds,omitempty" yaml:"idleTimeoutSeconds,omitempty"`
}

// GRPCRules resolves method, service and endpoint budgets without changing approval inputs.
func GRPCRules(e Endpoint) []GRPCRule {
	rules := []GRPCRule{}
	// An absent policy preserves the existing unrestricted native gRPC contract.
	if e.GRPC == nil {
		return rules
	}
	// Each permission retains the case-sensitive protobuf service and method names.
	for service, policy := range e.GRPC.Services {
		// Method budgets override service budgets, then inherit the endpoint default.
		for method, override := range policy.Methods {
			timeout := InheritedIdleTimeout(override.IdleTimeoutSeconds, policy.IdleTimeoutSeconds, e.IdleTimeoutSeconds)
			rules = append(rules, GRPCRule{Path: "/" + service + "/" + method, IdleTimeoutSeconds: timeout})
		}
	}
	// Exact lexical order keeps retained permissions independent of map iteration.
	sort.Slice(rules, func(i, j int) bool { return rules[i].Path < rules[j].Path })
	return rules
}

// HTTPRouteNames keeps edge rendering independent of map iteration order.
func HTTPRouteNames(p HTTPPolicy) []string {
	names := make([]string, 0, len(p.Routes))
	// Route names are stable identities in plans, rollback and generated proxy configuration.
	for name := range p.Routes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// InheritedIdleTimeout selects the most specific nonzero budget, retaining the proxy default otherwise.
func InheritedIdleTimeout(values ...int) int {
	// Zero means inherit, never silently disable the enclosing idle budget.
	for _, value := range values {
		// The first explicit value belongs to the most specific declaration.
		if value != 0 {
			return value
		}
	}
	return 0
}

// HasTrafficPolicy identifies endpoints whose admission decision is explicitly default-deny.
func HasTrafficPolicy(e *Endpoint) bool { return e != nil && (e.GRPC != nil || e.HTTP != nil) }
