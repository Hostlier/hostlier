package config

import (
	"hostlier/internal/model"
	"strings"
)

// normalizeWorkload lowers explicit v2 inputs into the shared deployment fields.
func normalizeWorkload(p model.Project) (model.Project, error) {
	// Version one must never silently accept topology that its runtime ignores.
	if p.Version != 2 {
		return p, rejectLegacyWorkload(p)
	}
	var err error
	p, err = normalizeSource(p)
	// A source must resolve before process validation.
	if err != nil {
		return p, err
	}
	// Omitted workload type denotes a request-serving process.
	if p.Workload == "" {
		p.Workload = "service"
	}
	// Only implemented process lifecycles may be deployed.
	if p.Workload != "service" && p.Workload != "worker" {
		return p, invalid("Workload must be service or worker")
	}
	// Normalize every named listener before the legacy-compatible primary fields.
	p, err = normalizeEndpoints(p)
	// Ambiguous or invalid listener maps cannot reach a runtime.
	if err != nil {
		return p, err
	}
	// Normalize absent exposure without ever generating a public route.
	if p.Endpoint == nil {
		p.Endpoint = &model.Endpoint{Exposure: "none"}
	}
	// Validate process and exposure invariants before recursive member validation.
	if err := validateEndpoint(p); err != nil {
		return p, err
	}
	p.Port, p.Route = p.Endpoint.Port, p.Endpoint.Route
	// Validate bounded commands and lifecycle settings on every member.
	if err := validateProcess(p); err != nil {
		return p, err
	}
	return normalizeSidecars(p)
}

// rejectLegacyWorkload prevents v2 fields from being ignored by the v1 runtime.
func rejectLegacyWorkload(p model.Project) error {
	// Legacy source and lifecycle semantics are intentionally unchanged.
	if hasManagedDeclarations(p) || legacyProcessFields(p) || p.Endpoint != nil || p.Workload != "" || len(p.Sidecars) > 0 || len(p.Command) > 0 || len(p.Args) > 0 || p.Shutdown != nil {
		return invalid("Workload fields require version 2")
	}
	return nil
}

// validateEndpoint defines explicit publication and private consumer boundaries.
func validateEndpoint(p model.Project) error {
	e := p.Endpoint
	// Listener shape must match the process lifecycle.
	if err := validateListener(p); err != nil {
		return err
	}
	// Public routes alone may request certificate and edge configuration.
	if e.Exposure == "public" {
		// Route selection can be completed interactively by the CLI.
		if p.Workload != "service" {
			return invalid("Only services may be public")
		}
	} else if e.Route != "" || p.Route != "" { // Private workloads cannot accidentally retain old routes.
		return invalid("Only public services may declare a route")
	}
	// Accept only the documented exposure values.
	if e.Exposure != "public" && e.Exposure != "private" && e.Exposure != "none" && e.Exposure != "loopback" {
		return invalid("Unknown endpoint exposure")
	}
	return validateEndpointPolicies(p)
}

// validateProcess bounds executable arguments and checks explicit private probes.
func validateProcess(p model.Project) error {
	// Bound termination waits for both runtime APIs.
	if p.Shutdown != nil && (p.Shutdown.GraceSeconds < 1 || p.Shutdown.GraceSeconds > 600) {
		return invalid("Shutdown graceSeconds must be 1 to 600")
	}
	// Exec vectors are bounded separately from process policy.
	if err := validateCommands(p); err != nil {
		return err
	}
	// Private listeners must be checked from inside their namespace.
	if requiresExecProbe(p) && len(probeCommand(p)) == 0 {
		return invalid("Private services, workers and primary protocols other than http require health.exec")
	}
	return validateProbe(p.Health)
}

// probeCommand safely extracts an optional exec probe.
func probeCommand(p model.Project) []string {
	// Missing health configuration has no executable probe.
	if p.Health == nil {
		return nil
	}
	return p.Health.Exec
}

// validateProbe prevents ambiguous probes and unbounded readiness waits.
func validateProbe(h *model.Health) error {
	// Public services can use the existing default HTTP health check.
	if h == nil {
		return nil
	}
	// A probe selects one transport and has a bounded timeout.
	if len(h.Exec) > 0 && h.Path != "" || h.TimeoutSeconds < 0 || h.TimeoutSeconds > 600 {
		return invalid("Select one health probe and a timeout up to 600 seconds")
	}
	return nil
}

// normalizeSidecars validates members independently while reserving shared ports.
func normalizeSidecars(p model.Project) (model.Project, error) {
	// Bound group size before recursively validating members.
	if len(p.Sidecars) > 16 {
		return p, invalid("At most 16 sidecars are supported")
	}
	ports := map[int]bool{p.Port: true}
	// Named primary listeners share the sidecar namespace.
	for _, e := range p.Endpoints {
		ports[e.Port] = true
	}
	// Deterministic names also make validation failures reproducible.
	for _, name := range model.MemberNames(p) {
		m := p.Sidecars[name]
		// A sidecar cannot create another ownership or scheduling hierarchy.
		if invalidMember(name, m) {
			return p, invalid("Invalid sidecar identity or nested deployment")
		}
		m.Version, m.Name = 2, name
		m.Dependencies = p.Dependencies
		normalized, err := ValidateProject(m)
		// Reject invalid members before changing the parent specification.
		if err != nil {
			return p, err
		}
		// Reserve every member socket and reject publication outside the shared group.
		if err := reserveMemberEndpoints(normalized, ports); err != nil {
			return p, err
		}
		normalized.Dependencies = nil
		p.Sidecars[name] = normalized
	}
	return p, nil
}

// workloadBinding recognizes bounded endpoint references without granting access.
func workloadBinding(ref string) bool {
	parts := strings.Split(ref, ".")
	return len(parts) == 3 && (parts[0] == "applications" || parts[0] == "sidecars") && nameRE.MatchString(parts[1]) && parts[2] == "url"
}

// normalizeSource keeps authored and immutable image references consistent.
func normalizeSource(p model.Project) (model.Project, error) {
	// Sources remain explicit and mutually exclusive.
	if p.Source == nil {
		return p, invalid("Version 2 requires source")
	}
	// Artifact preparation replaces this source with its immutable image before planning.
	if p.Source.Container != nil {
		return p, invalid("Prepare local artifacts with the Hostlier CLI before validation")
	}
	// Preserve a previously pinned image only when it matches the source.
	if p.Image != "" && p.Image != p.Source.Image {
		return p, invalid("Conflicting image and source")
	}
	p.Image = p.Source.Image
	return p, nil
}

// validateConsumers restricts private access declarations to canonical identities.
func validateConsumers(p model.Project) error {
	e := p.Endpoint
	// Consumers only apply to independently addressable private services.
	if len(e.Consumers) > 64 || len(e.Consumers) > 0 && e.Exposure != "private" {
		return invalid("Consumers require a private endpoint")
	}
	seen := map[string]bool{}
	// Bound private access to named applications without duplicate grants.
	for _, name := range e.Consumers {
		// Self grants and invalid identities cannot authorize network attachment.
		if !nameRE.MatchString(name) || name == p.Name || seen[name] {
			return invalid("Invalid or duplicate private consumer")
		}
		seen[name] = true
	}
	return nil
}

// validateListener separates workers from request-serving processes.
func validateListener(p model.Project) error {
	e := p.Endpoint
	// Workers have no inbound listener; services declare a usable port.
	if p.Workload == "worker" {
		// Reject contradictory inbound declarations before any route allocation.
		if e.Port != 0 || e.Exposure != "none" {
			return invalid("Workers require exposure none and no port")
		}
	} else if e.Port < 1 || e.Port > 65535 { // Services need a concrete listener.
		return invalid("Services require a port between 1 and 65535")
	}
	return nil
}

// validateCommands bounds argv without invoking a shell or inspecting values in errors.
func validateCommands(p model.Project) error {
	// Reject unbounded command vectors and embedded NULs without executing a shell.
	for _, args := range [][]string{p.Command, p.Args, probeCommand(p)} {
		// Bound vector length independently of argument bytes.
		if len(args) > 128 {
			return invalid("Too many command arguments")
		}
		// Validate each argument as a literal exec-vector element.
		for _, arg := range args {
			// NUL cannot be represented in process arguments.
			if len(arg) > 8192 || strings.ContainsRune(arg, 0) {
				return invalid("Invalid command argument")
			}
		}
	}
	return validateExecutables(p)
}

// invalidMember excludes nested ownership and scheduling from sidecar declarations.
func invalidMember(name string, m model.Project) bool {
	return !nameRE.MatchString(name) || name == "app" || len(m.Sidecars) > 0 || m.Replicas != nil || m.Workflow != nil || len(m.Dependencies) > 0
}

// legacyProcessFields groups v2-only process data so legacy validation stays explicit.
func legacyProcessFields(p model.Project) bool {
	return p.Source != nil || p.Required != nil || p.NamespaceImage != "" || len(probeCommand(p)) > 0
}

// validateExecutables prevents empty executable names while preserving empty ordinary arguments.
func validateExecutables(p model.Project) error {
	// Only argv zero is interpreted as the executable path.
	for _, argv := range [][]string{p.Command, probeCommand(p)} {
		// An explicitly supplied executable must be nonempty.
		if len(argv) > 0 && strings.TrimSpace(argv[0]) == "" {
			return invalid("Executable name is empty")
		}
	}
	return nil
}

// hasManagedDeclarations identifies version-two endpoint maps and immutable file inputs.
func hasManagedDeclarations(p model.Project) bool {
	return len(p.Endpoints) > 0 || len(p.Configs) > 0 || p.PrimaryEndpoint != "" || p.Security != nil
}

// validateEndpointPolicies combines transport validation with provider-owned access declarations.
func validateEndpointPolicies(p model.Project) error {
	// Policies match the original public identity, including singular endpoint declarations.
	if err := validatePolicyPathRewrite(p); err != nil {
		return err
	}
	// Unknown protocols never fall back to HTTP during legacy-compatible normalization.
	if err := validateEndpointTransport(*p.Endpoint); err != nil {
		return err
	}
	return validateConsumers(p)
}

// reserveMemberEndpoints applies shared namespace port ownership to every member listener.
func reserveMemberEndpoints(p model.Project, ports map[int]bool) error {
	endpoints := p.Endpoints
	// Singular members retain the same namespace checks as named listeners.
	if len(endpoints) == 0 {
		endpoints = map[string]model.Endpoint{"": *p.Endpoint}
	}
	memberPorts := map[int]bool{}
	// Aliases within a member may share a port, but siblings cannot.
	for _, endpoint := range endpoints {
		// Sidecars cannot bypass the parent lifecycle with their own public or private service.
		if endpoint.Exposure != "loopback" && endpoint.Exposure != "none" {
			return invalid("Sidecars require loopback or none exposure")
		}
		// A listener cannot take a port already owned by another component.
		if endpoint.Port > 0 && ports[endpoint.Port] {
			return invalid("Sidecar port conflicts with another group listener")
		}
		memberPorts[endpoint.Port] = true
	}
	// Commit reservations only after the entire component has been validated.
	for port := range memberPorts {
		ports[port] = true
	}
	return nil
}

// requiresExecProbe avoids sending plaintext HTTP readiness requests to incompatible primary transports.
func requiresExecProbe(p model.Project) bool {
	return !model.Public(p) || p.Endpoint != nil && p.Endpoint.Protocol != "" && p.Endpoint.Protocol != "http"
}
