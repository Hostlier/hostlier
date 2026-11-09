package runtime

import (
	"context"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
	"maps"
	"net"
	"strconv"
	"strings"
	"time"
)

// extendGroupManifests applies v2 process, secret and network semantics to generated objects.
func (k *K3s) extendGroupManifests(spec model.Project, dc model.DeployContext, manifests []map[string]any) ([]map[string]any, error) {
	// Legacy manifests retain their existing wire representation.
	if spec.Version != 2 {
		return manifests, nil
	}
	// Missing scoped file credentials cannot produce empty native configuration objects.
	if err := validateGroupConfigValues(spec, dc.Secrets); err != nil {
		return nil, err
	}
	values := map[string]string{}
	// Copy secrets so endpoint injection never mutates a caller's input map.
	maps.Copy(values, dc.Secrets)
	s, err := k.base.read()
	// Runtime state is needed to recheck private consumer grants.
	if err != nil {
		return nil, err
	}
	providers, err := resolveWorkloadBindings(spec, s, values, k.privateAddress)
	// Missing providers cannot produce permissive networking fallbacks.
	if err != nil {
		return nil, err
	}
	version := "app-" + shortHash(dc.OperationID)
	// Rewrite only owned workload objects generated in this call.
	for _, m := range manifests {
		// The single secret uses component-qualified keys and explicit key references.
		if m["kind"] == "Secret" {
			m["stringData"] = groupSecretData(values)
		}
		// All components use one Pod network namespace.
		if m["kind"] == "Deployment" {
			k.extendPod(spec, version, values, m)
		}
		// Provider and consumer access is explicit at the namespace boundary.
		if m["kind"] == "NetworkPolicy" {
			k.extendGroupPolicy(spec, providers, m)
		}
	}
	// Endpoint-only bindings also need a Secret when no credential existed originally.
	if needsEndpointSecret(dc, values) {
		manifests = append(manifests, map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": k.metadata(spec.Name, version), "type": "Opaque", "stringData": groupSecretData(values)})
	}
	return k.groupConfigFiles(k.appendMemberVolumes(spec, version, manifests), spec, version, values), nil
}

// privateAddress uses cluster DNS rather than host-loopback addresses for independent services.
func (k *K3s) privateAddress(name string, p model.Project) string {
	return "http://" + net.JoinHostPort("app."+k.namespace(name)+".svc.cluster.local", strconv.Itoa(p.Port))
}

// groupSecretData uses valid Kubernetes Secret keys while preserving member scope.
func groupSecretData(values map[string]string) map[string]string {
	out := map[string]string{}
	// Slash separates component scope internally and cannot occur in environment identifiers.
	for key, value := range values {
		out[strings.ReplaceAll(key, "/", ".")] = value
	}
	return out
}

// extendPod gives each container scoped configuration and native sidecar lifecycle ordering.
func (k *K3s) extendPod(p model.Project, version string, values map[string]string, m map[string]any) {
	pod := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	containers := pod["containers"].([]any)
	primary := containers[0].(map[string]any)
	configureGroupContainer(primary, p, "", version, values)
	sidecars := []any{}
	grace := model.GraceSeconds(p)
	// Native sidecars start before and terminate after the primary process.
	for _, name := range model.MemberNames(p) {
		member := p.Sidecars[name]
		container := map[string]any{"name": name, "image": member.Image, "restartPolicy": "Always"}
		configureGroupContainer(container, member, name, version, values)
		container["startupProbe"] = groupProbe(member)
		// Optional sidecars never make the Pod unready after startup.
		if member.Required != nil && !*member.Required {
			delete(container, "readinessProbe")
			delete(container, "startupProbe")
		}
		sidecars = append(sidecars, container)
		grace += model.GraceSeconds(member)
	}
	pod["initContainers"] = sidecars
	pod["terminationGracePeriodSeconds"] = grace
}

// configureGroupContainer selects probes and credentials without inheriting sibling values.
func configureGroupContainer(c map[string]any, p model.Project, member, version string, values map[string]string) {
	// Prepared images are present locally and must never fall back to remote pulls.
	if artifact.IsImage(p.Image) {
		c["image"] = artifact.RuntimeImage(p.Image)
		c["imagePullPolicy"] = "Never"
	}
	delete(c, "envFrom")
	c["env"] = groupEnvironment(p, member, version, values)
	c["readinessProbe"] = groupProbe(p)
	// Entry point and arguments have matching Docker/Kubernetes semantics.
	if len(p.Command) > 0 {
		c["command"] = p.Command
	}
	// Omitted arguments preserve the image defaults.
	if len(p.Args) > 0 {
		c["args"] = p.Args
	}
	// Workers declare no listener or service port.
	if p.Port == 0 {
		delete(c, "ports")
	}
	// Each container's resources are independent of the parent allocation.
	if p.Resources != nil {
		c["resources"] = map[string]any{"limits": p.Resources}
	}
}

// groupEnvironment references only explicitly scoped secret keys.
func groupEnvironment(p model.Project, member, version string, values map[string]string) []any {
	out := []any{}
	// Literal values remain part of the reviewed specification.
	for key, value := range p.Env {
		out = append(out, map[string]string{"name": key, "value": value})
	}
	prefix := ""
	// Member key prefixes are not exported as environment names.
	if member != "" {
		prefix = member + "/"
	}
	// A container can read only the keys selected in its own environment.
	for key := range values {
		env, ok := strings.CutPrefix(key, prefix)
		// Exclude sibling and nested scopes.
		if !ok || strings.Contains(env, "/") {
			continue
		}
		out = append(out, map[string]any{"name": env, "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": version, "key": strings.ReplaceAll(key, "/", ".")}}})
	}
	return out
}

// groupProbe uses exec for private loopback and keeps HTTP support for public apps.
func groupProbe(p model.Project) map[string]any {
	probe := map[string]any{"periodSeconds": 1, "timeoutSeconds": 2, "failureThreshold": 60}
	// Exec probes originate inside the container and can reach Pod loopback.
	if p.Health != nil && len(p.Health.Exec) > 0 {
		probe["exec"] = map[string]any{"command": p.Health.Exec}
	} else { // Public services retain the existing HTTP readiness behavior.
		probe["httpGet"] = map[string]any{"path": healthPath(p), "port": p.Port}
	}
	// Bound startup tolerance using the same project readiness window.
	if p.Health != nil && p.Health.TimeoutSeconds > 0 {
		probe["failureThreshold"] = p.Health.TimeoutSeconds
	}
	return probe
}

// extendGroupPolicy grants independent private callers and selected provider egress.
func (k *K3s) extendGroupPolicy(p model.Project, providers []string, m map[string]any) {
	policy := m["spec"].(map[string]any)
	egress := policy["egress"].([]any)
	// Egress authorizes only the providers resolved from explicit bindings.
	for _, name := range providers {
		egress = append(egress, map[string]any{"to": []any{k.namespacePeer(name)}})
	}
	policy["egress"] = egress
	ingress := []any{}
	// Private services expose only their declared listener to named consumer namespaces.
	if p.Endpoint != nil && p.Endpoint.Exposure == "private" {
		// Namespace selectors are qualified by the server ownership prefix.
		for _, name := range p.Endpoint.Consumers {
			ingress = append(ingress, map[string]any{"from": []any{k.namespacePeer(name)}, "ports": []any{map[string]any{"protocol": "TCP", "port": p.Port}}})
		}
	}
	policy["ingress"] = ingress
}

// namespacePeer selects only one server-owned application's namespace.
func (k *K3s) namespacePeer(name string) map[string]any {
	return map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": k.namespace(name)}}}
}

// appendMemberVolumes creates separately owned claims and explicit member mounts.
func (k *K3s) appendMemberVolumes(p model.Project, version string, manifests []map[string]any) []map[string]any {
	// Locate the generated Pod before augmenting its volume inventory.
	for _, m := range manifests {
		// Other objects cannot carry container mounts.
		if m["kind"] != "Deployment" {
			continue
		}
		pod := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		// Native sidecars retain a separate filesystem unless mounts are explicit.
		for _, item := range pod["initContainers"].([]any) {
			c := item.(map[string]any)
			name := c["name"].(string)
			mounts := []any{}
			// Claims have stable member-qualified names across revisions.
			for key, v := range p.Sidecars[name].Volumes {
				claim := "sidecar-" + name + "-" + key
				manifests = append(manifests, map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": k.metadata(p.Name, claim), "spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "storageClassName": "local-path", "resources": map[string]any{"requests": map[string]string{"storage": v.Size}}}})
				pod["volumes"] = append(pod["volumes"].([]any), map[string]any{"name": claim, "persistentVolumeClaim": map[string]string{"claimName": claim}})
				mounts = append(mounts, map[string]string{"name": claim, "mountPath": v.Mount})
			}
			c["volumeMounts"] = mounts
		}
	}
	return manifests
}

// groupService avoids creating an inbound service for a listenerless worker.
func (k *K3s) groupService(ctx context.Context, p model.Project, version string) (string, error) {
	// Remove a prior service when an approved update changes to a worker.
	if p.Version == 2 && p.Port == 0 {
		return "", k.remove(ctx, "service", "app", k.namespace(p.Name))
	}
	return k.service(ctx, p, version)
}

// needsEndpointSecret adds a binding secret only when the legacy generator omitted it.
func needsEndpointSecret(dc model.DeployContext, values map[string]string) bool {
	return len(dc.Secrets) == 0 && len(values) > 0
}

// readyGroup includes the approved shutdown budget when a restart replaces old Pods.
func (k *K3s) readyGroup(ctx context.Context, app appRecord) error {
	copy := *k
	grace := model.GraceSeconds(app.Spec)
	// Native sidecars stop after the primary within the shared Pod grace period.
	for _, m := range app.Spec.Sidecars {
		grace += model.GraceSeconds(m)
	}
	copy.options.HealthTimeout += time.Duration(grace) * time.Second
	return copy.ready(ctx, k.namespace(app.Name), app.RuntimeID)
}
