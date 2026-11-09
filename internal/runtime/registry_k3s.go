package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"hostlier/internal/artifact"
	"hostlier/internal/dockerlogin"
	"hostlier/internal/model"
)

// k3sPullSecret names the namespace-scoped image pull secret refreshed by each approved deployment.
const k3sPullSecret = "hostlier-registry"

// k3sPullConfig builds a kubelet dockerconfigjson holding only the registries this workload group pulls from.
func (k *K3s) k3sPullConfig(ctx context.Context, spec model.Project) ([]byte, error) {
	auths := map[string]any{}
	checked := map[string]bool{}
	// Look up each distinct registry once; a missing login keeps that registry anonymous.
	for _, image := range groupPullImages(spec) {
		registry := dockerlogin.Registry(image)
		// Repeated images from one registry share a single credential entry.
		if checked[registry] {
			continue
		}
		checked[registry] = true
		entry, ok, err := k.kubeletAuth(ctx, image)
		// A broken or locked login must never silently become an anonymous pull.
		if err != nil {
			return nil, registryImageContext(image, err)
		}
		// Registries without a login entry are pulled anonymously, as on Docker.
		if ok {
			auths[pullConfigKey(registry)] = entry
		}
	}
	// Public-only groups need no Kubernetes credential object at all.
	if len(auths) == 0 {
		return nil, nil
	}
	return json.Marshal(map[string]any{"auths": auths})
}

// kubeletAuth converts one registry login into the username/password form kubelet understands.
func (k *K3s) kubeletAuth(ctx context.Context, image string) (map[string]string, bool, error) {
	credential, ok, err := k.base.auth(ctx, image)
	// Lookup failures and absent logins are reported unchanged to the caller.
	if err != nil || !ok {
		return nil, false, err
	}
	// Kubelet pull secrets cannot carry Docker identity or registry tokens.
	if credential.Username == "" || credential.Password == "" {
		return nil, false, model.Err("REGISTRY_CREDENTIALS", "k3s pulls need a username and password or access-token Docker login for this registry; token-only logins are not supported by Kubernetes")
	}
	auth := base64.StdEncoding.EncodeToString([]byte(credential.Username + ":" + credential.Password))
	return map[string]string{"username": credential.Username, "password": credential.Password, "auth": auth}, true, nil
}

// groupPullImages lists remote images the cluster pulls, excluding locally imported artifacts.
func groupPullImages(spec model.Project) []string {
	images := []string{spec.Image}
	// Version-two groups render their sidecars into the same Pod.
	if spec.Version == 2 {
		// Sorted member order keeps the generated secret deterministic.
		for _, name := range model.MemberNames(spec) {
			images = append(images, spec.Sidecars[name].Image)
		}
	}
	remote := []string{}
	// Prepared artifacts use imagePullPolicy Never and must not request credentials.
	for _, image := range images {
		// Only registry references can be pulled by kubelet.
		if image != "" && !artifact.IsImage(image) {
			remote = append(remote, image)
		}
	}
	return remote
}

// pullConfigKey uses Docker Hub's historical index key, which kubelet maps to docker.io images.
func pullConfigKey(registry string) string {
	// Kubelet's default-registry matching expects the legacy Hub index address.
	if registry == "docker.io" {
		return "https://index.docker.io/v1/"
	}
	return registry
}

// withPullSecret adds the pull secret before the first workload and references it from every Pod template.
func (k *K3s) withPullSecret(spec model.Project, manifests []map[string]any, config []byte) []map[string]any {
	// Anonymous deployments keep their original manifest set unchanged.
	if config == nil {
		return manifests
	}
	secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": k.metadata(spec.Name, k3sPullSecret), "type": "kubernetes.io/dockerconfigjson", "data": map[string]any{".dockerconfigjson": base64.StdEncoding.EncodeToString(config)}}
	out := make([]map[string]any, 0, len(manifests)+1)
	inserted := false
	// Isolation stays first; the secret must exist before any Deployment schedules Pods.
	for _, m := range manifests {
		// Attach the reference to each Deployment and insert the secret ahead of the first.
		if m["kind"] == "Deployment" {
			// Exactly one secret object is applied per deployment.
			if !inserted {
				out, inserted = append(out, secret), true
			}
			pod := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			pod["imagePullSecrets"] = []any{map[string]any{"name": k3sPullSecret}}
		}
		out = append(out, m)
	}
	// A manifest set without workloads still records the refreshed credential.
	if !inserted {
		out = append(out, secret)
	}
	return out
}
