package runtime

import (
	"context"
	"regexp"

	"hostlier/internal/model"
)

var telemetryDockerID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var telemetryKubeName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// telemetryDenied gives no caller-controlled identity authority over a workload's attribution.
func telemetryDenied() error {
	return model.Err("TELEMETRY_IDENTITY", "Telemetry does not identify a current owned workload")
}

// ResolveTelemetry verifies a full daemon identity against active retained application metadata.
func (r *Runtime) ResolveTelemetry(ctx context.Context, attributes map[string]string) (model.TelemetryIdentity, error) {
	id := attributes["container.id"]
	// Names, prefixes, arbitrary hostlier labels, and host-only records are not container identities.
	if !telemetryDockerID.MatchString(id) {
		return model.TelemetryIdentity{}, telemetryDenied()
	}
	s, err := r.telemetryState()
	// Runtime state must remain readable throughout the ownership decision.
	if err != nil {
		return model.TelemetryIdentity{}, err
	}
	identity, ok := dockerTelemetryIdentity(s, id)
	// Namespace holders, previous revisions, and deleted containers are not application members.
	if !ok {
		return model.TelemetryIdentity{}, telemetryDenied()
	}
	container, err := r.container(ctx, id, identity.Application)
	// Inspect must independently confirm both the full identity and the application owner.
	if err != nil || container.ID != id {
		return model.TelemetryIdentity{}, telemetryDenied()
	}
	current, err := r.telemetryState()
	checked, ok := dockerTelemetryIdentity(current, id)
	// A deployment completed during daemon inspection cannot attribute a retired instance.
	if err != nil || !ok || checked != identity {
		return model.TelemetryIdentity{}, telemetryDenied()
	}
	return identity, nil
}

// telemetryState snapshots durable ownership without holding the mutation lock during network I/O.
func (r *Runtime) telemetryState() (state, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read()
}

// dockerTelemetryIdentity selects only a primary or known sidecar recorded in the active group.
func dockerTelemetryIdentity(s state, id string) (model.TelemetryIdentity, bool) {
	// Durable state defines which runtime identities currently belong to a deployment.
	for name, app := range s.Apps {
		identity := model.TelemetryIdentity{Application: name, Revision: app.RuntimeID, Component: "app", Instance: id}
		// The primary has a fixed component name across both runtime adapters.
		if app.RuntimeID == id {
			return identity, true
		}
		// Arbitrary sidecars require both a runtime record and an approved component declaration.
		for member, container := range app.Members {
			_, declared := app.Spec.Sidecars[member]
			if declared && container == id { // An orphaned runtime map entry cannot grant ownership.
				identity.Component = member
				return identity, true
			}
		}
	}
	return model.TelemetryIdentity{}, false
}

// ResolveTelemetry verifies actual Pod identity and namespace ownership before attributing a record.
func (k *K3s) ResolveTelemetry(ctx context.Context, attributes map[string]string) (model.TelemetryIdentity, error) {
	// Pod coordinates are required, and names must be safe literal kubectl object arguments.
	if !validKubeTelemetryCoordinates(attributes) {
		return model.TelemetryIdentity{}, telemetryDenied()
	}
	app, identity, err := k.kubeTelemetryIdentity(attributes)
	// Namespace or component names alone cannot invent application membership.
	if err != nil {
		return model.TelemetryIdentity{}, err
	}
	namespace := attributes["k8s.namespace.name"]
	ns, err := k.get(ctx, "namespace", namespace, "")
	// The namespace must belong to this installation and the resolved application.
	if err != nil || !k.telemetryNamespaceMatches(ns, namespace, identity.Application) {
		return model.TelemetryIdentity{}, telemetryDenied()
	}
	pod, err := k.get(ctx, "pod", attributes["k8s.pod.name"], namespace)
	// Live Pod metadata and approved containers independently verify collector coordinates.
	if err != nil || !k.telemetryPodMatches(pod, app, identity, attributes) {
		return model.TelemetryIdentity{}, telemetryDenied()
	}
	_, checked, err := k.kubeTelemetryIdentity(attributes)
	// Recheck durable state after cluster I/O without blocking concurrent deployment operations.
	if err != nil || checked != identity {
		return model.TelemetryIdentity{}, telemetryDenied()
	}
	return identity, nil
}

// validKubeTelemetryCoordinates requires Pod identity and accepts an optional container dimension.
func validKubeTelemetryCoordinates(attributes map[string]string) bool {
	// Kubernetes names and UIDs contain no whitespace, path traversal, or option prefix.
	for _, key := range []string{"k8s.namespace.name", "k8s.pod.name", "k8s.pod.uid"} {
		if !telemetryKubeName.MatchString(attributes[key]) { // Require every supplied identity dimension.
			return false
		}
	}
	component := attributes["k8s.container.name"]
	return component == "" || telemetryKubeName.MatchString(component)
}

// kubeTelemetryIdentity maps a namespace and component to the controller-owned active record.
func (k *K3s) kubeTelemetryIdentity(attributes map[string]string) (appRecord, model.TelemetryIdentity, error) {
	s, err := k.base.telemetryState()
	// Missing durable state cannot be replaced with labels supplied by the collector.
	if err != nil {
		return appRecord{}, model.TelemetryIdentity{}, err
	}
	component := attributes["k8s.container.name"]
	// Namespace mapping is derived from retained application keys and this server owner.
	for name, app := range s.Apps {
		if k.namespace(name) != attributes["k8s.namespace.name"] || app.RuntimeID == "" { // Deleted groups have no runtime identity.
			continue
		}
		_, member := app.Spec.Sidecars[component]
		// An ephemeral or injected container is not an approved application component.
		if component != "" && component != "app" && !member {
			return appRecord{}, model.TelemetryIdentity{}, telemetryDenied()
		}
		return app, model.TelemetryIdentity{Application: name, Revision: app.RuntimeID, Component: component, Instance: attributes["k8s.pod.uid"]}, nil
	}
	return appRecord{}, model.TelemetryIdentity{}, telemetryDenied()
}

// telemetryObjectOwned verifies server and application labels read from the cluster API.
func (k *K3s) telemetryObjectOwned(object map[string]any, app string) bool {
	metadata, _ := object["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	return k.owned(object) && labels["io.hostlier.app"] == app
}

// telemetryNamespaceMatches checks both actual object identity and ownership from the cluster API.
func (k *K3s) telemetryNamespaceMatches(object map[string]any, namespace, app string) bool {
	metadata, _ := object["metadata"].(map[string]any)
	return metadata["name"] == namespace && k.telemetryObjectOwned(object, app)
}

// telemetryPodMatches rejects deleted, replaced, foreign, or undeclared container identities.
func (k *K3s) telemetryPodMatches(pod map[string]any, app appRecord, identity model.TelemetryIdentity, attributes map[string]string) bool {
	metadata, _ := pod["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	// Cluster-returned metadata must exactly match the caller's immutable Pod coordinates.
	if metadata["uid"] != identity.Instance || metadata["name"] != attributes["k8s.pod.name"] || metadata["namespace"] != attributes["k8s.namespace.name"] {
		return false
	}
	return k.telemetryObjectOwned(pod, identity.Application) && labels["io.hostlier.version"] == app.RuntimeID && (identity.Component == "" || telemetryPodHasComponent(pod, identity.Component))
}

// telemetryPodHasComponent admits only regular containers and persistent native sidecars.
func telemetryPodHasComponent(pod map[string]any, component string) bool {
	spec, _ := pod["spec"].(map[string]any)
	field := "initContainers"
	// The primary is the regular container; native sidecars live in initContainers.
	if component == "app" {
		field = "containers"
	}
	containers, _ := spec[field].([]any)
	// A name claimed by the collector must exist in the actual Pod specification.
	for _, item := range containers {
		container, _ := item.(map[string]any)
		if container["name"] == component { // One-shot init tasks are not persistent workload members.
			return component == "app" || container["restartPolicy"] == "Always"
		}
	}
	return false
}
