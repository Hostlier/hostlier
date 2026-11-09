package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"hostlier/internal/model"
)

// EnsureCollector reconciles one owned DaemonSet while leaving application mutation locks available.
func (k *K3s) EnsureCollector(ctx context.Context, spec model.CollectorDeployment) error {
	k.base.collectorMu.Lock()
	defer k.base.collectorMu.Unlock()
	p, err := prepareCollector(spec)
	// Invalid desired state cannot modify the currently deployed system collector.
	if err != nil {
		return err
	}
	objects, err := k.collectorObjects(p)
	// Parse and constrain the entire desired bundle before querying or changing the cluster.
	if err != nil {
		return err
	}
	// Preflight every named object so a late foreign collision cannot cause partial credential rotation.
	if err = k.preflightCollector(ctx, objects); err != nil {
		return err
	}
	for _, object := range objects { // Secrets and configuration precede the DaemonSet template update.
		if err = k.applyCollectorObject(ctx, object); err != nil {
			return err
		}
	}
	return k.waitCollectorK3s(ctx)
}

// collectorObjects labels a constrained renderer bundle and injects private runtime credentials.
func (k *K3s) collectorObjects(p preparedCollector) ([]map[string]any, error) {
	objects, err := decodeCollectorObjects(p.spec.Manifest)
	// Every required singleton object must exist exactly once in the authored bundle.
	if err != nil {
		return nil, err
	}
	var daemon map[string]any
	ordered := []map[string]any{}
	for _, object := range objects { // Preserve namespace-first renderer order while deferring workload startup.
		k.labelCollectorObject(object, p.revision)
		if object["kind"] == "ConfigMap" { // The canonical configuration controls both the hash and mounted content.
			data := objectMap(object, "data")
			data["collector.yaml"] = p.config
			if len(p.ca) > 0 { // Explicit public CA content is projected beside the collector configuration.
				data["ca.pem"] = string(p.ca)
			}
		}
		if object["kind"] == "DaemonSet" { // No Pod may start before its token and registry Secret exist.
			daemon = object
		} else {
			ordered = append(ordered, object)
		}
	}
	// Persistent node collectors use exactly the controller-pinned image and pull credential.
	if err = k.configureCollectorDaemon(daemon, p); err != nil {
		return nil, err
	}
	return append(append(ordered, k.collectorSecrets(p)...), daemon), nil
}

// decodeCollectorObjects rejects unknown, duplicate, or cross-namespace Kubernetes targets.
func decodeCollectorObjects(manifest string) ([]map[string]any, error) {
	decoder := yaml.NewDecoder(strings.NewReader(manifest))
	objects, seen := []map[string]any{}, map[string]bool{}
	for { // Decode complete YAML documents before returning any mutation-ready object.
		var object map[string]any
		err := decoder.Decode(&object)
		if err == io.EOF { // A clean stream terminator is valid only with the full required set below.
			break
		}
		if err != nil { // Never expose raw parser details that might contain private configuration.
			return nil, model.Err("COLLECTOR_CONFIG", "Collector manifest is invalid")
		}
		if object == nil { // YAML document separators can produce empty documents.
			continue
		}
		kind, _ := object["kind"].(string)
		if seen[kind] || !validCollectorObject(object) { // Only one copy of each expected fixed singleton is permitted.
			return nil, model.Err("COLLECTOR_CONFIG", "Collector manifest contains an unexpected target")
		}
		seen[kind] = true
		objects = append(objects, object)
	}
	// Required roles, configuration, service account, namespace and DaemonSet cannot be omitted.
	if len(seen) != 6 {
		return nil, model.Err("COLLECTOR_CONFIG", "Collector manifest is incomplete")
	}
	return objects, nil
}

// validCollectorObject fixes the namespace and object identities independently of input YAML.
func validCollectorObject(object map[string]any) bool {
	metadata, _ := object["metadata"].(map[string]any)
	name, namespace := metadata["name"], metadata["namespace"]
	switch object["kind"] { // Cluster-scoped objects must never carry a namespaced target.
	case "Namespace":
		return name == collectorNamespace && namespace == nil
	// Cluster permissions remain at fixed cluster-scoped identities.
	case "ClusterRole", "ClusterRoleBinding":
		return name == collectorNamespace+"-collector" && namespace == nil
	// Configuration and workload objects remain inside the fixed collector namespace.
	case "ServiceAccount", "ConfigMap", "DaemonSet":
		return name == collectorName && namespace == collectorNamespace
	// No other Kubernetes resource kind is part of the managed collector contract.
	default:
		return false
	}
}

// objectMap obtains mutable YAML object metadata without retaining a nil map.
func objectMap(parent map[string]any, name string) map[string]any {
	object, _ := parent[name].(map[string]any)
	// Renderer-provided maps may omit optional annotations or labels.
	if object == nil {
		object = map[string]any{}
		parent[name] = object
	}
	return object
}

// labelCollectorObject distinguishes system collectors from application-owned objects at fixed names.
func (k *K3s) labelCollectorObject(object map[string]any, revision string) {
	metadata := objectMap(object, "metadata")
	labels := objectMap(metadata, "labels")
	labels["io.hostlier.managed"], labels["io.hostlier.owner"], labels["io.hostlier.collector"] = "true", k.base.owner, "true"
	objectMap(metadata, "annotations")[collectorHashLabel] = revision
}

// configureCollectorDaemon makes image, credentials and rollout identity explicit on every node.
func (k *K3s) configureCollectorDaemon(daemon map[string]any, p preparedCollector) error {
	template := objectMap(objectMap(daemon, "spec"), "template")
	pod := objectMap(template, "spec")
	containers, _ := pod["containers"].([]any)
	// The renderer contract permits one collector container, never injected application sidecars.
	if len(containers) != 1 {
		return model.Err("COLLECTOR_CONFIG", "Collector DaemonSet must contain exactly one process")
	}
	container, ok := containers[0].(map[string]any)
	// The expected workload identity must survive decode before any fields are updated.
	if !ok || container["name"] != "collector" {
		return model.Err("COLLECTOR_CONFIG", "Collector DaemonSet process is invalid")
	}
	container["image"], container["imagePullPolicy"] = p.spec.Image, "IfNotPresent"
	pod["imagePullSecrets"] = []any{map[string]any{"name": "hostlier-collector-registry"}}
	k.labelCollectorObject(template, p.revision)
	k.configureCollectorTLS(pod, container, p)
	return nil
}

// collectorSecrets supplies ingestion and registry credentials only in protected Kubernetes Secrets.
func (k *K3s) collectorSecrets(p preparedCollector) []map[string]any {
	auth := base64.StdEncoding.EncodeToString([]byte("collector:" + p.spec.Token))
	registry, _ := json.Marshal(map[string]any{"auths": map[string]any{p.registry: map[string]string{"username": "collector", "password": p.spec.Token, "auth": auth}}})
	secrets := []map[string]any{
		{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "hostlier-telemetry", "namespace": collectorNamespace}, "type": "Opaque", "data": map[string]any{"token": base64.StdEncoding.EncodeToString([]byte(p.spec.Token))}},
		{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "hostlier-collector-registry", "namespace": collectorNamespace}, "type": "kubernetes.io/dockerconfigjson", "data": map[string]any{".dockerconfigjson": base64.StdEncoding.EncodeToString(registry)}},
	}
	if p.spec.TLS != nil { // Client keys stay in a Secret rather than public configuration objects.
		data := objectMap(secrets[0], "data")
		for name, content := range collectorTLSFiles(p.spec.TLS) { // Store exactly the dedicated transport identity.
			data[name] = base64.StdEncoding.EncodeToString(content)
		}
	}
	for _, secret := range secrets { // Secret ownership is checked with the same contract as the public manifest objects.
		k.labelCollectorObject(secret, p.revision)
	}
	return secrets
}

// collectorObjectIdentity extracts only constrained renderer or runtime-owned resource coordinates.
func collectorObjectIdentity(object map[string]any) (string, string, string) {
	metadata := objectMap(object, "metadata")
	kind, _ := object["kind"].(string)
	name, _ := metadata["name"].(string)
	namespace, _ := metadata["namespace"].(string)
	return kind, name, namespace
}

// collectorOwned requires explicit collector identity in addition to existing server ownership.
func (k *K3s) collectorOwned(object map[string]any) bool {
	metadata, _ := object["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	return k.owned(object) && labels["io.hostlier.collector"] == "true"
}

// preflightCollector fails on every foreign collision before making any bundle mutation.
func (k *K3s) preflightCollector(ctx context.Context, objects []map[string]any) error {
	for _, object := range objects { // The token Secret must not rotate before a later workload collision is discovered.
		kind, name, namespace := collectorObjectIdentity(object)
		current, err := k.get(ctx, kind, name, namespace)
		if err != nil { // Missing resources are acceptable, failed lookups are not.
			return err
		}
		if current != nil && !k.collectorOwned(current) { // Never adopt manual or foreign-controller resources.
			return model.Err("OWNERSHIP", "Refusing to adopt an unowned collector object")
		}
	}
	return nil
}

// applyCollectorObject repairs drift but skips already matching objects and their rollout templates.
func (k *K3s) applyCollectorObject(ctx context.Context, object map[string]any) error {
	kind, name, namespace := collectorObjectIdentity(object)
	current, err := k.get(ctx, kind, name, namespace)
	// Recheck immediately before apply rather than relying only on the bundle preflight.
	if err != nil {
		return err
	}
	// Another actor cannot take ownership between the initial preflight and this write.
	if current != nil && !k.collectorOwned(current) {
		return model.Err("OWNERSHIP", "Collector ownership changed before reconciliation")
	}
	raw, _ := json.Marshal(object)
	var normalized map[string]any
	_ = json.Unmarshal(raw, &normalized)
	// JSON normalization aligns YAML integers with actual Kubernetes JSON number representations.
	if collectorObjectMatches(current, normalized) {
		return nil
	}
	_, err = k.run(ctx, []string{"apply", "--server-side", "--field-manager=hostlier", "-f", "-"}, raw)
	return err
}

// collectorObjectMatches compares desired fields while permitting server defaults and status metadata.
func collectorObjectMatches(current, desired any) bool {
	switch value := desired.(type) { // Maps admit server defaults; ordered lists must match their declared members.
	case map[string]any:
		actual, ok := current.(map[string]any)
		if !ok { // Different object shapes always require reconciliation.
			return false
		}
		for key, item := range value { // Every explicitly authored value remains authoritative.
			if !collectorObjectMatches(actual[key], item) {
				return false
			}
		}
		return true
	// Container and rule lists must preserve their declared members.
	case []any:
		return collectorListMatches(current, value)
	// Scalar desired values must match exactly.
	default:
		return reflect.DeepEqual(current, desired)
	}
}

// collectorListMatches keeps container and credential selections exact without rejecting object defaults.
func collectorListMatches(current any, desired []any) bool {
	actual, ok := current.([]any)
	// Additional containers or altered rule entries are configuration drift, not server defaults.
	if !ok || len(actual) != len(desired) {
		return false
	}
	for i, item := range desired { // Lists are small, fixed renderer output rather than unbounded inventories.
		if !collectorObjectMatches(actual[i], item) {
			return false
		}
	}
	return true
}

// waitCollectorK3s waits for every desired node to run the current template before retiring old tokens.
func (k *K3s) waitCollectorK3s(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for { // DaemonSet convergence is bounded independently from application rollouts.
		daemon, err := k.get(bounded, "DaemonSet", collectorName, collectorNamespace)
		if err != nil { // Failed cluster reads cannot report a healthy collector.
			return err
		}
		if !k.collectorOwned(daemon) { // Disappearing or replaced resources invalidate readiness.
			return model.Err("OWNERSHIP", "Collector DaemonSet ownership changed")
		}
		if collectorDaemonReady(daemon) { // Every desired node must observe the current rollout generation.
			return nil
		}
		// Poll only until current-generation readiness or cancellation.
		select {
		// A bounded context prevents provisioning from blocking indefinitely.
		case <-bounded.Done():
			return model.Err("COLLECTOR_NOT_READY", "Collector DaemonSet is not ready on every node")
		// Recheck status after the bounded polling interval.
		case <-ticker.C:
		}
	}
}

// collectorDaemonReady requires actual current-generation scheduling and healthy updated replicas.
func collectorDaemonReady(daemon map[string]any) bool {
	metadata, _ := daemon["metadata"].(map[string]any)
	status, _ := daemon["status"].(map[string]any)
	generation, _ := metadata["generation"].(float64)
	observed, _ := status["observedGeneration"].(float64)
	desired, _ := status["desiredNumberScheduled"].(float64)
	return generation > 0 && observed >= generation && desired > 0 && status["updatedNumberScheduled"] == desired && status["numberReady"] == desired && status["numberAvailable"] == desired
}
