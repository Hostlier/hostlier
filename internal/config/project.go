package config

import (
	"math"
	"strconv"
	"strings"

	"hostlier/internal/model"
)

// validateProjectIdentity validates schema version, application name, image syntax and TCP port.
func validateProjectIdentity(p model.Project) (model.Project, error) {
	// Keep application schema migrations explicit.
	if p.Version != 1 && p.Version != 2 {
		return p, invalid("Unsupported project configuration version")
	}
	// Restrict application names to portable owned-resource identifiers.
	if !nameRE.MatchString(p.Name) {
		return p, invalid("Application name must be lowercase with letters, digits, and hyphens")
	}
	// Bound image references and exclude shell or URL metacharacters.
	if len(p.Image) > 512 || !imageRE.MatchString(p.Image) {
		return p, invalid("Invalid image reference")
	}
	// Require a usable TCP application port.
	if (p.Port < 1 || p.Port > 65535) && p.Workload != "worker" {
		return p, invalid("Application port must be between 1 and 65535")
	}
	return p, nil
}

// normalizeProjectRoute canonicalizes explicit routes and checks prefix stripping.
func normalizeProjectRoute(p model.Project) (model.Project, error) {
	// Normalize only explicitly supplied routes; planning may supply an absent route later.
	if p.Route != "" {
		r, err := ParseRoute(p.Route, nil, "")
		// Preserve route validation errors before rewriting the project URL.
		if err != nil {
			return p, err
		}
		p.Route = strings.TrimPrefix(r.URL, "https://")
		// A root route has no nonempty prefix to strip.
		if p.Proxy != nil && p.Proxy.StripPrefix && r.Path == "/" {
			return p, invalid("Prefix stripping requires a non-root route path")
		}
	}
	return p, nil
}

// validateProjectHealth bounds optional HTTP probe paths and readiness timeouts.
func validateProjectHealth(p model.Project) (model.Project, error) {
	// Validate health settings only when the optional probe is declared.
	if p.Health != nil && len(p.Health.Exec) == 0 {
		// Bound readiness timeouts and exclude ambiguous probe path delimiters.
		if !strings.HasPrefix(p.Health.Path, "/") || strings.ContainsAny(p.Health.Path, " \t\r\n\\?#") || p.Health.TimeoutSeconds < 0 || p.Health.TimeoutSeconds > 600 {
			return p, invalid("Invalid health path or timeout")
		}
	}
	return p, nil
}

// validateProjectResources checks CPU precision and positive representable memory quantities.
func validateProjectResources(p model.Project) (model.Project, error) {
	// Validate scheduling and storage concurrency independently of resource limits.
	if err := validateReplicaCount(p); err != nil {
		return p, err
	}
	// Apply CPU and memory constraints only when resource limits are declared.
	if p.Resources != nil {
		// An omitted CPU limit retains backend defaults.
		if p.Resources.CPU != "" {
			cpu, err := strconv.ParseFloat(p.Resources.CPU, 64)
			// Require finite CPU values expressible at the shared millicore precision.
			if err != nil || !(cpu >= 0.001 && cpu <= 1024) || math.Abs(math.Round(cpu*1000)-cpu*1000) > 0.000001 {
				return p, invalid("CPU must be 0.001 to 1024 cores in increments of 0.001")
			}
		}
		// Require a positive memory quantity that cannot overflow backend byte counts.
		if p.Resources.Memory != "" && !ValidSize(p.Resources.Memory) {
			return p, invalid("Memory must use a positive quantity such as 512Mi")
		}
	}
	return p, nil
}

// validateProjectLimits bounds every independent project configuration collection.
func validateProjectLimits(p model.Project) (model.Project, error) {
	// Bound each independently user-controlled configuration collection.
	if len(p.Env) > 128 || len(p.Secrets) > 128 || len(p.Volumes) > 128 || len(p.Dependencies) > 128 || len(p.Bindings) > 128 {
		return p, invalid("Too many configuration entries")
	}
	return p, nil
}

// validateProjectEnvironment validates literal environment entries and excludes secret collisions.
func validateProjectEnvironment(p model.Project) (model.Project, error) {
	// Validate literal environment values before checking secret-name collisions.
	for key, value := range p.Env {
		// Exclude invalid names, oversized values and embedded NUL bytes.
		if !envRE.MatchString(key) || len(value) > 65536 || strings.ContainsRune(value, 0) {
			return p, invalid("Invalid environment name or value")
		}
		// A variable cannot resolve from both a literal value and a secret.
		if _, exists := p.Secrets[key]; exists {
			return p, invalid("Environment name cannot also reference a secret")
		}
	}
	return p, nil
}

// validateProjectSecrets validates environment destinations and secret-reference identifiers.
func validateProjectSecrets(p model.Project) (model.Project, error) {
	// Validate each secret reference without looking up secret values.
	for key, value := range p.Secrets {
		// Require portable environment identifiers and bounded secret names.
		if !envRE.MatchString(key) || !secretRE.MatchString(value) {
			return p, invalid("Invalid secret binding")
		}
	}
	return p, nil
}

// validateProjectVolumes checks persistent storage declarations and unique normalized mounts.
func validateProjectVolumes(p model.Project) (model.Project, error) {
	mounts := map[string]bool{}
	// Track mount destinations while validating each named persistent volume.
	for key, v := range p.Volumes {
		// Reject invalid volume identifiers, quantities and mount syntax.
		if invalidVolume(key, v) {
			return p, invalid("Invalid volume name, size, or mount")
		}
		// Inspect mount components before accepting the destination.
		for _, part := range strings.Split(v.Mount, "/") {
			// Reject path aliases that can name the same mount in different ways.
			if part == "." || part == ".." {
				return p, invalid("Volume mount must be normalized")
			}
		}
		// Prevent two volumes from claiming the same container path.
		if mounts[v.Mount] {
			return p, invalid("Duplicate volume mount")
		}
		mounts[v.Mount] = true
	}
	return p, nil
}

// validateProjectDependencies enforces supported engines and source-specific reference fields.
func validateProjectDependencies(p model.Project) (model.Project, error) {
	// Validate each dependency's identity before interpreting its source mode.
	for key, d := range p.Dependencies {
		// Limit dependencies to the supported named database engines.
		if !validDependencyIdentity(key, d.Type) {
			return p, invalid("Unsupported dependency engine or name")
		}
		// Provisioned resources and existing references have distinct required fields.
		switch d.Source {
		// A new dependency declares storage rather than an existing instance.
		case "provision":
			// Require storage and reject a conflicting instance reference.
			if !ValidSize(d.Storage) || d.Instance != "" {
				return p, invalid("Provisioned dependency requires storage")
			}
		// An existing dependency supplies an instance rather than a new storage request.
		case "existing":
			// Require a valid reference and reject provisioning-only storage.
			if !nameRE.MatchString(d.Instance) || d.Storage != "" {
				return p, invalid("Existing dependency requires an instance reference")
			}
		// Reject unrecognized dependency source modes.
		default:
			return p, invalid("Unsupported dependency source")
		}
	}
	return p, nil
}

// validateProjectBindings resolves distinct environment destinations against declared dependency outputs.
func validateProjectBindings(p model.Project) (model.Project, error) {
	// Resolve each binding against declared dependencies and distinct environment names.
	for key, ref := range p.Bindings {
		// Application and sidecar endpoints use their own reference contract.
		if endpointBinding(p, ref) {
			// Reject collisions even when no database output is involved.
			if invalidEndpointDestination(p, key) {
				return p, invalid("Invalid endpoint binding destination")
			}
			continue
		}
		ref = strings.TrimPrefix(ref, "dependencies.")
		dep, output, ok := strings.Cut(ref, ".")
		d, exists := p.Dependencies[dep]
		_, literal := p.Env[key]
		_, secret := p.Secrets[key]
		// Reject malformed references and literal or secret environment collisions.
		if !envRE.MatchString(key) || !ok || !exists || literal || secret {
			return p, invalid("Binding must reference a declared dependency and a distinct environment name")
		}
		// Check the selected engine's public output contract.
		if err := validateDependencyOutput(d.Type, output); err != nil {
			return p, err
		}
	}
	return p, nil
}

// invalidVolume rejects unsupported storage quantities and ambiguous mount syntax.
func invalidVolume(key string, v model.Volume) bool {
	return !nameRE.MatchString(key) || !ValidSize(v.Size) || !pathRE.MatchString(v.Mount) || v.Mount == "/" || strings.HasSuffix(v.Mount, "/") || strings.Contains(v.Mount, "//")
}

// validDependencyIdentity accepts named dependencies backed by supported engines.
func validDependencyIdentity(key, engine string) bool {
	return nameRE.MatchString(key) && (engine == "postgres" || engine == "redis" || engine == "neo4j")
}

// validateDependencyOutput checks the public output contract of each engine.
func validateDependencyOutput(engine, output string) error {
	// Neo4j exposes a URI and separate credential fields.
	if engine == "neo4j" {
		// Reject outputs that the Neo4j connection contract does not supply.
		if output != "uri" && output != "username" && output != "password" {
			return invalid("Unknown Neo4j output")
		}
	} else if output != "url" { // PostgreSQL and Redis each expose a single connection URL.
		return invalid("Unknown dependency output")
	}
	return nil
}

// validateReplicaCount rejects invalid scheduling and unsafe persistent concurrency.
func validateReplicaCount(p model.Project) error {
	// Reject negative replica counts before runtime planning.
	if model.DesiredReplicas(p) < 0 {
		return invalid("Replicas must be zero or greater")
	}
	// Reject persistent multi-writer layouts without an explicit concurrency adapter.
	if model.DesiredReplicas(p) > 1 && model.HasVolumes(p) {
		return invalid("Persistent volumes support only one replica")
	}

	return nil
}

// invalidEndpointDestination forbids overwriting literal or secret values with a URL binding.
func invalidEndpointDestination(p model.Project, key string) bool {
	_, literal := p.Env[key]
	_, secret := p.Secrets[key]
	return !envRE.MatchString(key) || literal || secret
}

// endpointBinding limits the extended reference grammar to version two projects.
func endpointBinding(p model.Project, ref string) bool { return p.Version == 2 && workloadBinding(ref) }
