package collector

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// deployment renders the platform-specific supervision without secret material.
func deployment(o Options, config string) (string, error) {
	// Docker uses one Compose service instead of Kubernetes objects.
	if o.Runtime == "docker" {
		raw, err := yaml.Marshal(compose(o))
		return string(raw), err
	}
	var result strings.Builder
	// Keep each Kubernetes object as its own YAML document.
	for _, document := range kubernetes(o, config) {
		raw, err := yaml.Marshal(document)
		// Never publish an incomplete deployment document.
		if err != nil {
			return "", err
		}
		result.WriteString("---\n")
		result.Write(raw)
	}
	return result.String(), nil
}

// compose requires an operator-provided token environment file and read-only log mount.
func compose(o Options) object {
	return object{"name": "hostlier-observability", "services": object{"collector": object{"image": o.Image, "container_name": "hostlier-collector", "restart": "unless-stopped", "user": "0:0", "command": []string{"--config=/etc/otelcol/collector.yaml"}, "env_file": []string{"./collector.env"}, "environment": object{"GOMEMLIMIT": "96MiB"}, "mem_limit": "256m", "cpus": 0.5, "read_only": true, "security_opt": []string{"no-new-privileges:true"}, "cap_drop": []string{"ALL"}, "volumes": []string{"./collector.yaml:/etc/otelcol/collector.yaml:ro", "/var/run/docker.sock:/var/run/docker.sock:ro", "/var/lib/docker/containers:/var/lib/docker/containers:ro"}, "logging": object{"driver": "json-file", "options": object{"max-size": "1m", "max-file": "2"}}}}}
}

// metadata gives every collector object an explicit system namespace and identity.
func metadata(o Options, name string) object { return object{"name": name, "namespace": o.Namespace} }

// kubernetes builds the shared DaemonSet and its scoped Kubernetes permissions.
func kubernetes(o Options, config string) []object {
	name := "hostlier-collector"
	return []object{
		{"apiVersion": "v1", "kind": "Namespace", "metadata": object{"name": o.Namespace}},
		{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": metadata(o, name)},
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata(o, name), "data": object{"collector.yaml": config, "kubeconfig": kubeconfig()}},
		{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": object{"name": o.Namespace + "-collector"}, "rules": []any{object{"apiGroups": []string{""}, "resources": []string{"pods", "namespaces"}, "verbs": []string{"get", "list", "watch"}}, object{"apiGroups": []string{""}, "resources": []string{"nodes/proxy", "nodes/stats"}, "verbs": []string{"get"}}}},
		{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": object{"name": o.Namespace + "-collector"}, "roleRef": object{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": o.Namespace + "-collector"}, "subjects": []any{object{"kind": "ServiceAccount", "name": name, "namespace": o.Namespace}}},
		daemonset(o, name),
	}
}

// kubeconfig verifies the API server certificate and uses a rotating projected token.
func kubeconfig() string {
	return "apiVersion: v1\nkind: Config\nclusters:\n  - name: cluster\n    cluster:\n      server: https://kubernetes.default.svc\n      certificate-authority: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt\nusers:\n  - name: collector\n    user:\n      tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token\ncontexts:\n  - name: hostlier\n    context:\n      cluster: cluster\n      user: collector\ncurrent-context: hostlier\n"
}

// daemonset schedules exactly one log reader per node without publishing ingestion ports.
func daemonset(o Options, name string) object {
	labels := object{"app.kubernetes.io/name": name}
	container := object{"name": "collector", "image": o.Image, "args": []string{"--config=/etc/otelcol/collector.yaml"}, "env": []any{object{"name": "K8S_NODE_NAME", "valueFrom": object{"fieldRef": object{"fieldPath": "spec.nodeName"}}}, object{"name": "KUBECONFIG", "value": "/etc/otelcol/kubeconfig"}, object{"name": "GOMEMLIMIT", "value": "96MiB"}, object{"name": "HOSTLIER_TELEMETRY_TOKEN", "valueFrom": object{"secretKeyRef": object{"name": "hostlier-telemetry", "key": "token"}}}}, "resources": object{"requests": object{"cpu": "20m", "memory": "128Mi"}, "limits": object{"cpu": "500m", "memory": "256Mi"}}, "securityContext": object{"runAsUser": 0, "readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false, "capabilities": object{"drop": []string{"ALL"}}}, "volumeMounts": []any{object{"name": "config", "mountPath": "/etc/otelcol", "readOnly": true}, object{"name": "podlogs", "mountPath": "/var/log/pods", "readOnly": true}}}
	return object{"apiVersion": "apps/v1", "kind": "DaemonSet", "metadata": metadata(o, name), "spec": object{"selector": object{"matchLabels": labels}, "template": object{"metadata": object{"labels": labels}, "spec": object{"serviceAccountName": name, "nodeSelector": object{"kubernetes.io/os": "linux"}, "tolerations": []any{object{"operator": "Exists", "effect": "NoSchedule"}}, "containers": []any{container}, "volumes": []any{object{"name": "config", "configMap": object{"name": name}}, object{"name": "podlogs", "hostPath": object{"path": "/var/log/pods", "type": "Directory"}}}}}}}
}
