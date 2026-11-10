package model

import "sort"

// Source selects immutable image content or an uploaded compiled artifact.
type Source struct {
	Image     string           `json:"image,omitempty" yaml:"image,omitempty"`
	Container *ContainerSource `json:"container,omitempty" yaml:"container,omitempty"`
}

// ContainerSource selects a local Docker build context or a prebuilt portable archive.
type ContainerSource struct {
	Dockerfile   string `json:"dockerfile,omitempty" yaml:"dockerfile,omitempty"`
	Context      string `json:"context,omitempty" yaml:"context,omitempty"`
	Archive      string `json:"archive,omitempty" yaml:"archive,omitempty"`
	Architecture string `json:"architecture,omitempty" yaml:"architecture,omitempty"`
}

// Endpoint makes publication explicit and records authorized private callers.
type Endpoint struct {
	GRPC               *GRPCPolicy `json:"grpc,omitempty" yaml:"grpc,omitempty"`
	HTTP               *HTTPPolicy `json:"http,omitempty" yaml:"http,omitempty"`
	Protocol           string      `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	HTTPVersions       []string    `json:"httpVersions,omitempty" yaml:"httpVersions,omitempty"`
	Streaming          *bool       `json:"streaming,omitempty" yaml:"streaming,omitempty"`
	WebSocket          *bool       `json:"websocket,omitempty" yaml:"websocket,omitempty"`
	IdleTimeoutSeconds int         `json:"idleTimeoutSeconds,omitempty" yaml:"idleTimeoutSeconds,omitempty"`
	UpstreamServerName string      `json:"upstreamServerName,omitempty" yaml:"upstreamServerName,omitempty"`
	Port               int         `json:"port,omitempty" yaml:"port,omitempty"`
	Exposure           string      `json:"exposure" yaml:"exposure"`
	Route              string      `json:"route,omitempty" yaml:"route,omitempty"`
	Consumers          []string    `json:"consumers,omitempty" yaml:"consumers,omitempty"`
}

// Shutdown bounds cooperative process termination on either backend.
type Shutdown struct {
	GraceSeconds int `json:"graceSeconds" yaml:"graceSeconds"`
}

// Public preserves the v1 public service default without publishing v2 workers.
func Public(p Project) bool {
	// Named listeners may publish alongside a private primary.
	for _, e := range p.Endpoints {
		// A named public listener makes the workload publicly routed.
		if e.Exposure == "public" {
			return true
		}
	}
	return p.Version == 1 || p.Endpoint != nil && p.Endpoint.Exposure == "public"
}

// GraceSeconds gives all lifecycle paths the same termination window.
func GraceSeconds(p Project) int {
	// Preserve the existing timeout when none was supplied.
	if p.Shutdown == nil {
		return 20
	}
	return p.Shutdown.GraceSeconds
}

// MemberNames gives deterministic creation and rollback order.
func MemberNames(p Project) []string {
	names := make([]string, 0, len(p.Sidecars))
	// Sorting prevents map iteration from changing approval or startup behavior.
	for name := range p.Sidecars {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SecretBindings namespaces sidecar secrets without exposing them to the primary.
func SecretBindings(p Project) map[string]string {
	out := map[string]string{}
	// File references use a non-environment namespace and are pinned by ordinary secret revision approval.
	for name, file := range p.Configs {
		// Only explicit references participate in encrypted revision pinning.
		if file.Secret != "" {
			out["files/"+name] = file.Secret
		}
	}
	// Primary names remain compatible with retained v1 secret revisions.
	for key, value := range p.Secrets {
		out[key] = value
	}
	// A slash cannot occur in an environment name, so member scopes cannot collide.
	for name, member := range p.Sidecars {
		// Each member gets only explicitly requested credentials.
		for key, value := range SecretBindings(member) {
			out[name+"/"+key] = value
		}
	}
	return out
}

// EnvironmentBindings retains component scope for resolved dependency values.
func EnvironmentBindings(p Project) map[string]string {
	out := map[string]string{}
	// Primary bindings retain their existing environment destinations.
	for key, value := range p.Bindings {
		out[key] = value
	}
	// Member destinations cannot collide with primary environment identifiers.
	for name, member := range p.Sidecars {
		// Copy only explicitly declared member bindings.
		for key, value := range member.Bindings {
			out[name+"/"+key] = value
		}
	}
	return out
}

// HasVolumes detects retained single-writer storage anywhere in a deployment group.
func HasVolumes(p Project) bool {
	// Primary storage preserves the original concurrency restriction.
	if len(p.Volumes) > 0 {
		return true
	}
	// Each member owns independent storage but scales with its primary.
	for _, m := range p.Sidecars {
		// A sidecar writer is as exclusive as a primary writer.
		if len(m.Volumes) > 0 {
			return true
		}
	}
	return false
}

// ComponentStatus exposes bounded process diagnostics without environment, tokens or probe output.
type ComponentStatus struct {
	Name         string `json:"name"`
	Instance     string `json:"instance,omitempty"`
	Running      bool   `json:"running"`
	Ready        bool   `json:"ready"`
	RestartCount int    `json:"restartCount,omitempty"`
	ExitCode     int    `json:"exitCode,omitempty"`
	OOMKilled    bool   `json:"oomKilled,omitempty"`
}
