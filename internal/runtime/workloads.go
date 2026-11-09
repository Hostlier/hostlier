package runtime

import (
	"context"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"net"
	"sort"
	"strconv"
	"strings"
)

// workloadRoute reserves a public address only for explicitly public workloads.
func workloadRoute(p model.Project, hostname string) (model.Route, error) {
	// Private workloads have no certificate or domain dependency.
	if !model.Public(p) {
		return model.Route{}, nil
	}
	return config.ParseRoute(p.Route, nil, hostname)
}

// groupHasVolumes detects exclusive writers anywhere in an application group.
func groupHasVolumes(p model.Project) bool {
	// The primary retains the original single-writer behavior.
	if len(p.Volumes) > 0 {
		return true
	}
	// Sidecar volumes participate in the same update boundary.
	for _, m := range p.Sidecars {
		// Any member's retained storage requires a group maintenance window.
		if len(m.Volumes) > 0 {
			return true
		}
	}
	return false
}

// memberEnvironment separates resolved credentials by member without inheriting siblings.
func memberEnvironment(p model.Project, member string, values map[string]string) []string {
	merged := map[string]string{}
	// Literal environment is scoped to this component.
	for key, value := range p.Env {
		merged[key] = value
	}
	prefix := member + "/"
	// The primary has unprefixed destinations for compatibility with v1.
	if member == "" {
		prefix = ""
	}
	// Only explicitly scoped values reach a member's container.
	for key, value := range values {
		rest, ok := strings.CutPrefix(key, prefix)
		// Nested or sibling destinations never become environment variables.
		if ok && !strings.Contains(rest, "/") {
			merged[rest] = value
		}
	}
	out := []string{}
	// Encode an exec environment without shell interpolation.
	for key, value := range merged {
		out = append(out, key+"="+value)
	}
	sort.Strings(out)
	return out
}

// resolveWorkloadBindings checks provider grants again against runtime state.
func resolveWorkloadBindings(p model.Project, s state, values map[string]string, address func(string, model.Project) string) ([]string, error) {
	providers := []string{}
	// Runtime resolution applies equally to primary and member destinations.
	for dest, ref := range model.EnvironmentBindings(p) {
		parts := strings.Split(ref, ".")
		// Database references were already resolved by the controller.
		if len(parts) != 3 {
			continue
		}
		// Group-local addresses never leave the shared namespace.
		if parts[0] == "sidecars" {
			values[dest] = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Sidecars[parts[1]].Port))
		}
		// Independent services require provider-owned authorization.
		if parts[0] == "applications" {
			provider, ok := s.Apps[parts[1]]
			// Do not attach to missing, stopped or unauthorized services.
			if !ok || provider.Status != "running" || !runtimeConsumer(provider.Spec, p.Name) {
				return nil, model.Err("PRIVATE_ACCESS", "Private service unavailable or unauthorized")
			}
			providers = append(providers, provider.Name)
			values[dest] = address(provider.Name, provider.Spec)
		}
	}
	return providers, nil
}

// runtimeConsumer verifies exact consumer identity at the runtime boundary.
func runtimeConsumer(p model.Project, name string) bool {
	// Only private services issue access grants.
	if p.Endpoint == nil || p.Endpoint.Exposure != "private" {
		return false
	}
	// Names are validated before reaching runtime planning.
	for _, n := range p.Endpoint.Consumers {
		// Exact equality avoids prefix-based access expansion.
		if n == name {
			return true
		}
	}
	return false
}

// pullMember uses the existing authenticated immutable image pull contract.
func (r *Runtime) pullMember(ctx context.Context, image string) error {
	// Direct runtime callers must not bypass immutable input validation.
	if !digestPattern.MatchString(image) {
		return model.Err("UNPINNED_IMAGE", "Group images must be immutable")
	}
	d := dockerDeployment{runtime: r, ctx: ctx, spec: model.Project{Image: image}}
	return d.pull()
}
