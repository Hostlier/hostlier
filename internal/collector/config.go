// Package collector renders shared, independently supervised OpenTelemetry collectors.
package collector

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Version is the upstream distribution used to validate generated configurations.
const Version = "0.164.0"

// DefaultImage selects the minimal distribution for standalone rendered bundles.
// Managed installations replace this local tag with the controller's immutable registry reference.
const DefaultImage = "hostlier-collector:0.164.0-minimal"

// Options describes non-secret collector deployment inputs.
type Options struct {
	Runtime   string
	Endpoint  string
	Namespace string
	Image     string
	CAFile    string
}

// Bundle contains a Collector config and its Compose or Kubernetes deployment.
type Bundle struct {
	Config     string
	Deployment string
}

// object keeps generated YAML trees explicit without importing platform API libraries.
type object = map[string]any

// Render builds a shared collector without embedding credentials or modifying a server.
func Render(o Options) (Bundle, error) {
	o, err := normalize(o)
	// Fail before returning a partially rendered bundle.
	if err != nil {
		return Bundle{}, err
	}
	config := configuration(o)
	raw, err := yaml.Marshal(config)
	// Fail before returning a partially rendered bundle.
	if err != nil {
		return Bundle{}, err
	}
	deployment, err := deployment(o, string(raw))
	return Bundle{Config: string(raw), Deployment: deployment}, err
}

// normalize rejects unsafe addresses before they become exporter or manifest values.
func normalize(o Options) (Options, error) {
	// Runtime selection determines the native telemetry inputs.
	if o.Runtime != "docker" && o.Runtime != "k3s" {
		return o, fmt.Errorf("collector runtime must be docker or k3s")
	}
	// Reject unsafe endpoint values before building YAML.
	if err := validateEndpoint(o.Endpoint); err != nil {
		return o, err
	}
	o.Endpoint = strings.TrimRight(o.Endpoint, "/")
	// Use an isolated system namespace unless explicitly supplied.
	if o.Namespace == "" {
		o.Namespace = "hostlier-observability"
	}
	// Namespace values become object names and file-glob segments.
	if !dnsLabel(o.Namespace) {
		return o, fmt.Errorf("collector namespace must be a DNS label")
	}
	// Default to the exact tested multi-platform distribution.
	if o.Image == "" {
		o.Image = DefaultImage
	}
	// Reject ambiguous image selection and interpolation.
	if strings.ContainsAny(o.Image, "\n\r$ \t") || !strings.Contains(o.Image, ":") {
		return o, fmt.Errorf("collector image must have an explicit tag or digest")
	}
	// Private CA mounts require a reviewed deployment-specific volume.
	if o.CAFile != "" {
		return o, fmt.Errorf("custom CA files must be mounted and configured explicitly in the reviewed collector manifest")
	}
	return o, nil
}

// validateEndpoint requires a plain controller origin, with no secret-bearing URL fields.
func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	// Only a parseable HTTPS origin can receive exported telemetry.
	if err != nil || !httpsOrigin(u) {
		return fmt.Errorf("collector endpoint must be an HTTPS controller origin without credentials, query or path")
	}
	// Collector interpolation must not be accepted from a command-line URL.
	if strings.ContainsAny(endpoint, "\n\r$") {
		return fmt.Errorf("collector endpoint cannot contain interpolation")
	}
	return nil
}

// httpsOrigin excludes credentials and extra request-routing information.
func httpsOrigin(u *url.URL) bool {
	return u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

var namespacePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// dnsLabel limits generated namespace and object names to a safe Kubernetes label.
func dnsLabel(s string) bool { return namespacePattern.MatchString(s) }

// configuration uses standard OTLP JSON with bounded queues and no application ingress.
func configuration(o Options) object {
	exporter := object{"metrics_endpoint": o.Endpoint + "/v1/telemetry/otlp/metrics", "logs_endpoint": o.Endpoint + "/v1/telemetry/otlp/logs", "encoding": "json", "compression": "none", "headers": object{"Authorization": "Bearer ${env:HOSTLIER_TELEMETRY_TOKEN}"}, "timeout": "10s", "retry_on_failure": object{"enabled": true, "initial_interval": "1s", "max_interval": "10s", "max_elapsed_time": "60s"}, "sending_queue": object{"enabled": true, "num_consumers": 1, "queue_size": 16}}
	processors := object{"memory_limiter": object{"check_interval": "1s", "limit_mib": 96, "spike_limit_mib": 24}, "batch": object{"timeout": "5s", "send_batch_size": 64, "send_batch_max_size": 128}, "batch/logs": object{"timeout": "5s", "send_batch_size": 4, "send_batch_max_size": 8}}
	config := object{"exporters": object{"otlp_http/hostlier": exporter}, "processors": processors, "service": object{"telemetry": object{"logs": object{"level": "warn"}}, "pipelines": object{}}}
	// Install only the receivers appropriate to this runtime.
	if o.Runtime == "docker" {
		dockerConfig(config, processors)
	} else {
		kubernetesConfig(config, processors, o.Namespace)
	}
	return config
}

// pipeline keeps buffering bounded and uses only the authenticated Hostlier exporter.
func pipeline(receivers []string, processors []string) object {
	return object{"receivers": receivers, "processors": processors, "exporters": []string{"otlp_http/hostlier"}}
}

// dockerConfig discovers only managed log files and attaches authoritative container IDs.
func dockerConfig(config, processors object) {
	config["extensions"] = object{"docker_observer": object{"endpoint": "unix:///var/run/docker.sock", "api_version": "1.44", "include_all_containers": true, "timeout": "5s"}}
	config["receivers"] = object{
		"docker_stats":     object{"endpoint": "unix:///var/run/docker.sock", "collection_interval": "10s", "timeout": "5s", "container_labels_to_metric_labels": object{"io.hostlier.managed": "hostlier.managed"}},
		"receiver_creator": object{"watch_observers": []string{"docker_observer"}, "receivers": object{"file_log/containers": object{"rule": "type == \"container\" && port == 0 && labels[\"io.hostlier.managed\"] == \"true\"", "config": object{"include": []string{"/var/lib/docker/containers/`container_id`/`container_id`-json.log"}, "start_at": "end", "include_file_path": true, "max_log_size": "16KiB", "max_concurrent_files": 4, "operators": []any{object{"type": "container", "format": "docker", "add_metadata_from_filepath": false, "max_log_size": "16KiB", "on_error": "drop"}}}}}, "resource_attributes": object{"container": object{"container.id": "`container_id`", "container.name": "`name`", "container.image.name": ""}}},
	}
	processors["filter/managed"] = object{"error_mode": "ignore", "metrics": object{"metric": []string{`resource.attributes["hostlier.managed"] != "true"`}}}
	service := config["service"].(object)
	service["extensions"] = []string{"docker_observer"}
	service["pipelines"] = object{"metrics": pipeline([]string{"docker_stats"}, []string{"memory_limiter", "filter/managed", "batch"}), "logs": pipeline([]string{"receiver_creator"}, []string{"memory_limiter", "batch/logs"})}
}

// kubernetesConfig reads kubelet metrics and local pod logs without a per-pod sidecar.
func kubernetesConfig(config, processors object, namespace string) {
	config["receivers"] = object{
		"kubelet_stats": object{"collection_interval": "10s", "auth_type": "kubeConfig", "context": "hostlier", "endpoint": "${env:K8S_NODE_NAME}", "metric_groups": []string{"node", "pod", "container", "volume"}},
		"file_log/pods": object{"include": []string{"/var/log/pods/hostlier-*/*/*.log"}, "exclude": []string{"/var/log/pods/" + namespace + "_*/*/*.log"}, "start_at": "end", "include_file_path": true, "max_log_size": "16KiB", "max_concurrent_files": 128, "operators": []any{object{"type": "container", "max_log_size": "16KiB", "on_error": "drop"}}},
	}
	processors["k8s_attributes"] = object{"auth_type": "serviceAccount", "filter": object{"node_from_env_var": "K8S_NODE_NAME"}, "extract": object{"metadata": []string{"k8s.namespace.name", "k8s.pod.name", "k8s.pod.uid", "k8s.node.name"}, "labels": []any{object{"tag_name": "hostlier.application", "key": "io.hostlier.app", "from": "pod"}, object{"tag_name": "hostlier.runtime_revision", "key": "io.hostlier.version", "from": "pod"}}}, "pod_association": []any{object{"sources": []any{object{"from": "resource_attribute", "name": "k8s.pod.uid"}}}}}
	processors["filter/managed"] = object{"error_mode": "ignore", "metrics": object{"metric": []string{`resource.attributes["k8s.namespace.name"] != nil and not IsMatch(resource.attributes["k8s.namespace.name"], "^hostlier-")`}}}
	config["service"].(object)["pipelines"] = object{"metrics": pipeline([]string{"kubelet_stats"}, []string{"memory_limiter", "filter/managed", "k8s_attributes", "batch"}), "logs": pipeline([]string{"file_log/pods"}, []string{"memory_limiter", "k8s_attributes", "batch/logs"})}
}
