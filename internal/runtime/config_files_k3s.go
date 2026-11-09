package runtime

import (
	"context"
	"encoding/base64"
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// groupConfigFiles attaches immutable, revision-scoped files to their exact container owners.
func (k *K3s) groupConfigFiles(manifests []map[string]any, p model.Project, version string, values map[string]string) []map[string]any {
	// Only Pod-producing manifests carry a file namespace.
	for _, manifest := range manifests {
		// Namespace, policy and storage declarations remain unchanged.
		if manifest["kind"] != "Deployment" {
			continue
		}
		pod := manifest["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		containers := append([]any{}, pod["containers"].([]any)...)
		// Native sidecars share scheduling but retain independent file mounts.
		if init, ok := pod["initContainers"].([]any); ok {
			containers = append(containers, init...)
		}
		// Each component gets its own immutable ConfigMap/Secret objects.
		for _, item := range containers {
			container := item.(map[string]any)
			name := container["name"].(string)
			member := p
			// The primary always uses app, while named members use their authored identities.
			if name != "app" {
				member = p.Sidecars[name]
			}
			objects := k.componentConfigFiles(p.Name, name, member, version, values, container, pod)
			manifests = append(manifests, objects...)
		}
	}
	return manifests
}

// componentConfigFiles keeps secret data out of public ConfigMaps and literal environment variables.
func (k *K3s) componentConfigFiles(app, member string, p model.Project, version string, values map[string]string, container, pod map[string]any) []map[string]any {
	out := []map[string]any{}
	// A mount references one key, avoiding arbitrary file-tree injection.
	for name, file := range p.Configs {
		id := "config-" + shortHash(member+"/"+name)
		objectName := version + "-" + shortHash(id)
		mode := file.Mode
		// Direct runtime calls retain the read-only mode default.
		if mode == 0 {
			mode = 0444
		}
		metadata := k.metadata(app, objectName)
		data := file.Content
		kind := "ConfigMap"
		source := map[string]any{"name": objectName}
		// Encrypted references are materialized only inside namespace-scoped Secrets.
		if file.Secret != "" {
			key := "files/" + name
			// Member file secrets have an independent destination namespace.
			if member != "app" {
				key = member + "/" + key
			}
			data = values[key]
			kind = "Secret"
			source = map[string]any{"secretName": objectName}
		}
		object := map[string]any{"apiVersion": "v1", "kind": kind, "metadata": metadata, "immutable": true}
		// ConfigMaps preserve UTF-8 text; Secret payloads use Kubernetes' standard base64 wire encoding.
		if kind == "Secret" {
			object["data"] = map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(data))}
		} else {
			object["data"] = map[string]string{"content": data}
		}
		source["items"] = []any{map[string]any{"key": "content", "path": "content", "mode": mode}}
		volume := map[string]any{"name": id}
		// Volumes select exactly one native source type.
		if kind == "Secret" {
			volume["secret"] = source
		} else {
			volume["configMap"] = source
		}
		existing, _ := pod["volumes"].([]any)
		pod["volumes"] = append(existing, volume)
		mounts, _ := container["volumeMounts"].([]any)
		// Existing sidecar volume mounts may use a typed map slice from the legacy generator.
		if typed, ok := container["volumeMounts"].([]map[string]string); ok {
			// Preserve every existing typed volume mount before adding files.
			for _, mount := range typed {
				mounts = append(mounts, mount)
			}
		}
		container["volumeMounts"] = append(mounts, map[string]any{"name": id, "mountPath": file.Mount, "subPath": "content", "readOnly": true})
		out = append(out, object)
	}
	return out
}

// servicePorts exposes named primary listeners without publishing loopback-only sidecars.
func servicePorts(p model.Project) []any {
	// Existing singular services keep their wire-compatible unnamed port.
	if len(p.Endpoints) == 0 {
		return []any{map[string]any{"port": p.Port, "targetPort": p.Port, "protocol": "TCP"}}
	}
	out := []any{}
	seen := map[int]bool{}
	// Kubernetes port names remain deterministic and duplicate aliases share one socket.
	for _, name := range model.EndpointNames(p) {
		e := p.Endpoints[name]
		// Loopback and listenerless endpoints stay inside their Pod namespace.
		if e.Exposure == "loopback" || e.Exposure == "none" || seen[e.Port] {
			continue
		}
		seen[e.Port] = true
		out = append(out, map[string]any{"name": "ep-" + shortHash(name)[:12], "port": e.Port, "targetPort": e.Port, "protocol": "TCP"})
	}
	return out
}

// validateGroupConfigValues applies the Docker file boundary to every Kubernetes component.
func validateGroupConfigValues(p model.Project, values map[string]string) error {
	components := map[string]model.Project{"": p}
	// Members retain the same credential namespaces as Docker groups.
	for name, member := range p.Sidecars {
		components[name] = member
	}
	// Reject unresolved paths and missing pinned file secrets before native object generation.
	for member, project := range components {
		// Controller and direct runtime callers share the exact schema validation.
		if _, err := config.ValidateManagedConfigs(project); err != nil {
			return err
		}
		// Empty secret content is valid only when its explicit scoped key actually exists.
		for name, file := range project.Configs {
			// A missing value cannot become an empty configuration file.
			if _, err := configFileContent(file, member, name, values); err != nil {
				return err
			}
		}
	}
	return nil
}

// restoreIsolationPolicy restores existing network grants without reading or rewriting retained file secrets.
func (k *K3s) restoreIsolationPolicy(ctx context.Context, app appRecord) error {
	policy, err := k.isolationPolicy(app.Spec, app.DependencyNetworks)
	// Retained dependency names must still belong to this server.
	if err != nil {
		return err
	}
	// Workload groups recheck their existing private provider relationships.
	if app.Spec.Version == 2 {
		s, err := k.base.read()
		// Missing runtime state cannot authorize permissive fallback networking.
		if err != nil {
			return err
		}
		providers, err := resolveWorkloadBindings(app.Spec, s, map[string]string{}, k.privateAddress)
		// A stopped or unauthorized provider cannot be guessed into an active contract.
		if err != nil {
			return err
		}
		k.extendGroupPolicy(app.Spec, providers, policy)
	}
	return k.apply(ctx, policy)
}
