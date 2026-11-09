package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/dockerlogin"
	"hostlier/internal/model"
)

// k3sLoginFixture builds a k3s adapter whose server Docker login contains the supplied config.
func k3sLoginFixture(t *testing.T, config string) *K3s {
	t.Helper()
	directory := t.TempDir()
	// An empty config models a server account that never ran docker login.
	if config != "" {
		// The fixture must exist before the adapter reads it.
		if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir(), DockerLogin: &dockerlogin.Source{Directory: directory}})
	// The adapter must construct without a running cluster.
	if err != nil {
		t.Fatal(err)
	}
	return adapter.(*K3s)
}

// decodePullConfig returns the kubelet auth entries from a generated dockerconfigjson.
func decodePullConfig(t *testing.T, raw []byte) map[string]map[string]string {
	t.Helper()
	var config struct {
		Auths map[string]map[string]string `json:"auths"`
	}
	// Generated configs must be valid kubelet JSON.
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	return config.Auths
}

// TestK3sPullSecretUsesServerLogin verifies only the image registry's login reaches the namespace secret.
func TestK3sPullSecretUsesServerLogin(t *testing.T) {
	login := base64.StdEncoding.EncodeToString([]byte("alice:ghp-token"))
	other := base64.StdEncoding.EncodeToString([]byte("bob:unrelated"))
	k := k3sLoginFixture(t, `{"auths":{"ghcr.io":{"auth":"`+login+`"},"registry.example.com":{"auth":"`+other+`"}}}`)
	spec := model.Project{Name: "app", Image: "ghcr.io/acme/app@sha256:" + strings.Repeat("a", 64), Port: 3000, Route: "app.example.com"}
	raw, err := k.k3sPullConfig(context.Background(), spec)
	// A valid inline login must produce a pull config.
	if err != nil || raw == nil {
		t.Fatal("pull config missing", err)
	}
	auths := decodePullConfig(t, raw)
	entry := auths["ghcr.io"]
	// Only the deployed registry is included, in kubelet's username/password form.
	if len(auths) != 1 || entry["username"] != "alice" || entry["password"] != "ghp-token" || entry["auth"] != login {
		t.Fatal("unexpected pull config entries", len(auths))
	}
	manifests, err := k.K3sManifests(spec, model.DeployContext{OperationID: "approved-op"})
	// Manifest generation must succeed for an immutable image.
	if err != nil {
		t.Fatal(err)
	}
	assertPullSecretManifests(t, k.withPullSecret(spec, manifests, raw), raw)
}

// assertPullSecretManifests checks isolation order, secret type and every Pod template reference.
func assertPullSecretManifests(t *testing.T, manifests []map[string]any, raw []byte) {
	t.Helper()
	// Isolation must still be applied before any credential or workload object.
	if manifests[0]["kind"] != "NetworkPolicy" {
		t.Fatal("isolation is no longer first")
	}
	secretAt, deploymentAt := -1, -1
	// Locate the pull secret and the first Deployment.
	for i, m := range manifests {
		// The pull secret is identified by its fixed name.
		if m["kind"] == "Secret" && m["metadata"].(map[string]any)["name"] == k3sPullSecret {
			secretAt = i
			assertPullSecretObject(t, m, raw)
		}
		// Every Deployment must reference the namespace pull secret.
		if m["kind"] == "Deployment" {
			deploymentAt = firstIndex(deploymentAt, i)
			assertPullSecretReference(t, m)
		}
	}
	// The secret must exist before kubelet schedules the candidate Pods.
	if secretAt < 0 || deploymentAt < 0 || secretAt > deploymentAt {
		t.Fatal("pull secret not applied before the deployment", secretAt, deploymentAt)
	}
}

// firstIndex keeps an already recorded position.
func firstIndex(current, candidate int) int {
	// Only an unset position is replaced.
	if current < 0 {
		return candidate
	}
	return current
}

// assertPullSecretObject checks kubelet's secret type and the exact generated config.
func assertPullSecretObject(t *testing.T, m map[string]any, raw []byte) {
	t.Helper()
	data := m["data"].(map[string]any)[".dockerconfigjson"]
	// Any other type or payload would be ignored or misread by kubelet.
	if m["type"] != "kubernetes.io/dockerconfigjson" || data != base64.StdEncoding.EncodeToString(raw) {
		t.Fatal("pull secret has wrong type or content")
	}
}

// assertPullSecretReference checks a Deployment's Pod template references the pull secret.
func assertPullSecretReference(t *testing.T, m map[string]any) {
	t.Helper()
	pod := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	encoded, _ := json.Marshal(pod["imagePullSecrets"])
	// Pods without the reference would pull anonymously.
	if string(encoded) != `[{"name":"hostlier-registry"}]` {
		t.Fatal("deployment missing imagePullSecrets", string(encoded))
	}
}

// TestK3sPullSecretAnonymousAndHub keeps public images unchanged and uses Hub's legacy key.
func TestK3sPullSecretAnonymousAndHub(t *testing.T) {
	k := k3sLoginFixture(t, "")
	spec := model.Project{Name: "app", Image: "docker.io/library/nginx@sha256:" + strings.Repeat("b", 64), Port: 80, Route: "app.example.com"}
	raw, err := k.k3sPullConfig(context.Background(), spec)
	// No login means an anonymous pull with no Kubernetes secret.
	if err != nil || raw != nil {
		t.Fatal("anonymous image produced credentials", err)
	}
	manifests, _ := k.K3sManifests(spec, model.DeployContext{OperationID: "approved-op"})
	encoded, _ := json.Marshal(k.withPullSecret(spec, manifests, raw))
	// Public deployments keep their previous manifest shape.
	if strings.Contains(string(encoded), "imagePullSecrets") || strings.Contains(string(encoded), k3sPullSecret) {
		t.Fatal("anonymous deployment references a pull secret")
	}
	hub := base64.StdEncoding.EncodeToString([]byte("carol:hub-token"))
	k = k3sLoginFixture(t, `{"auths":{"https://index.docker.io/v1/":{"auth":"`+hub+`"}}}`)
	raw, err = k.k3sPullConfig(context.Background(), spec)
	// Docker Hub logins are stored under the legacy index address kubelet matches.
	if err != nil || decodePullConfig(t, raw)["https://index.docker.io/v1/"]["username"] != "carol" {
		t.Fatal("Docker Hub login not mapped to kubelet key", err)
	}
}

// TestK3sPullSecretRejectsUnusableLogins never downgrades broken or token-only logins to anonymous pulls.
func TestK3sPullSecretRejectsUnusableLogins(t *testing.T) {
	spec := model.Project{Name: "app", Image: "ghcr.io/acme/app@sha256:" + strings.Repeat("c", 64), Port: 3000, Route: "app.example.com"}
	// Each case must fail before any Kubernetes mutation.
	for name, config := range map[string]string{"token-only": `{"auths":{"ghcr.io":{"identitytoken":"refresh"}}}`, "malformed": `{"auths":`} {
		_, err := k3sLoginFixture(t, config).k3sPullConfig(context.Background(), spec)
		problem, ok := err.(*model.Error)
		// Credential errors identify the image without leaking login contents.
		if !ok || problem.Code != "REGISTRY_CREDENTIALS" || strings.Contains(problem.Message, "refresh") {
			t.Fatal(name, "login was not rejected safely", err)
		}
	}
}
