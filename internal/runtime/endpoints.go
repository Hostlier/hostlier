package runtime

import (
	"context"
	"hostlier/internal/model"
	"net"
	"sort"
	"strconv"
)

// endpointPortBindings publishes every public listener on host loopback, never a wildcard address.
func endpointPortBindings(p model.Project) (map[string]any, error) {
	bindings := map[string]any{}
	// Public aliases sharing one container listener reuse its exact private host port.
	for _, project := range model.EndpointProjects(p) {
		key := strconv.Itoa(project.Port) + "/tcp"
		// A port is bound once even when several named routes target it.
		if _, ok := bindings[key]; ok {
			continue
		}
		value, err := privatePortBinding(project.Port)
		// Failure to reserve a private mapping cannot fall back to host networking.
		if err != nil {
			return nil, err
		}
		bindings[key] = value[key]
	}
	return bindings, nil
}

// endpointUpstreams resolves approved named routes against the same namespace as their primary.
func (d *dockerDeployment) endpointUpstreams() (map[string]string, error) {
	out := map[string]string{}
	// Named listeners use independent loopback mappings on the owned namespace container.
	for name, p := range model.EndpointProjects(d.spec) {
		port, err := d.runtime.backendPort(d.ctx, d.anchor, p.Port)
		// Every public listener must have a verified private mapping before edge activation.
		if err != nil {
			return nil, err
		}
		out[name] = net.JoinHostPort(d.runtime.options.UpstreamHost, port)
	}
	return out, nil
}

// endpointURLs are public addresses only; internal ports are not published as host URLs.
func endpointURLs(p model.Project) map[string]string {
	out := map[string]string{}
	// Legacy clients keep using URL while named clients receive every advertised endpoint.
	for name, project := range model.EndpointProjects(p) {
		// The legacy unnamed route already has its existing URL field.
		if name != "" {
			out[name] = project.Route
		}
	}
	return out
}

// expandedApplications renders every named edge route from one retained workload identity.
func expandedApplications(s state) []appRecord {
	out := []appRecord{}
	// Existing filtering preserves degraded and zero-capacity semantics.
	for _, app := range runningApplications(s) {
		// Only opted-in traffic passes through the idle gateway.
		if app.Spec.SleepAfter != "" {
			app.Upstream = idleAddress
			// Named endpoint copies below use their own upstream map.
			app.EndpointUpstreams = map[string]string{model.PrimaryEndpointName(app.Spec): idleAddress}
		}
		// A legacy record has no second mapping table.
		if len(app.Spec.Endpoints) == 0 {
			out = append(out, app)
			continue
		}
		// Every copy retains the same lifecycle, capacity and immutable source identity.
		for _, name := range model.EndpointNames(app.Spec) {
			project, ok := model.EndpointProjects(app.Spec)[name]
			// Private endpoints do not become public simply because another listener is public.
			if !ok {
				continue
			}
			copy := app
			copy.Spec = project
			copy.Upstream = app.EndpointUpstreams[name]
			copy.Spec.Name = app.Name + "." + name
			out = append(out, copy)
		}
	}
	// Specific endpoint paths must precede a root route on the same host.
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Spec.Route) > len(out[j].Spec.Route) })
	return out
}

// clusterEndpointUpstreams maps named listeners onto the one allocated private ClusterIP.
func clusterEndpointUpstreams(p model.Project, upstream string) map[string]string {
	out := map[string]string{}
	host, _, err := net.SplitHostPort(upstream)
	// An invalid primary cannot invent a reachable secondary service.
	if err != nil {
		return out
	}
	// Service port names retain each endpoint's reviewed container port.
	for name, project := range model.EndpointProjects(p) {
		out[name] = net.JoinHostPort(host, strconv.Itoa(project.Port))
	}
	return out
}

// publicEndpointsReady validates TLS reachability without attempting to execute a gRPC method.
func (r *Runtime) publicEndpointsReady(ctx context.Context, p model.Project) error {
	// The primary retains its existing HTTPS readiness gate.
	for _, project := range model.EndpointProjects(p) {
		// gRPC readiness belongs to its explicit process probe and end-to-end protocol check.
		if project.Endpoint != nil && (project.Endpoint.Protocol == "grpc" || project.Endpoint.Protocol == "grpcs") {
			continue
		}
		// Every HTTP route must present a valid certificate before the deployment is committed.
		if err := r.publicReady(ctx, project.Route); err != nil {
			return err
		}
	}
	return nil
}

// validateEndpointRoutes reserves every named public address before touching workload processes.
func (r *Runtime) validateEndpointRoutes(ctx context.Context, s state, p model.Project) error {
	// Every secondary route has the same ownership boundary as the primary.
	for _, endpoint := range model.EndpointProjects(p) {
		route, err := workloadRoute(endpoint, r.options.Hostname)
		// Invalid routes cannot reach the edge ownership check.
		if err != nil {
			return err
		}
		// Existing applications and administrator routes retain their reservations.
		if err := r.validateOwnedRoute(ctx, s, p.Name, route); err != nil {
			return err
		}
	}
	return nil
}
