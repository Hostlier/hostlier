package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"hostlier/internal/model"
	"hostlier/pkg/packagekit"
)

// TestSandboxPackagesK3s checks actual package binaries against the Kubernetes renderer.
// HOSTLIER_SANDBOX_PACKAGE_DIR opts into the sibling repository integration test.
// HOSTLIER_SANDBOX_K3S_EXPORT optionally saves manifests for an isolated live smoke run.
func TestSandboxPackagesK3s(t *testing.T) {
	dir := os.Getenv("HOSTLIER_SANDBOX_PACKAGE_DIR")
	// Ordinary unit tests do not require a checkout of the package repository.
	if dir == "" {
		t.Skip("set HOSTLIER_SANDBOX_PACKAGE_DIR to the built sandbox package directory")
	}
	// Both target architectures must pass independently of the CLI machine's CPU.
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch,
			// Keep each server-architecture scenario independent.
			func(t *testing.T) {
				p, values := sandboxPackageProject(t, dir, arch)
				k := k3sSecurityFixture(t)
				manifests, err := k.K3sManifests(p, model.DeployContext{OperationID: "sandbox-test", Secrets: values})
				// The real profiles must pass the backend's Docker-extension rejection gate.
				if err != nil {
					t.Fatal(err)
				}
				assertSandboxPod(t, manifests, p)
				// Export only the native-architecture fixture for optional local cluster validation.
				if output := os.Getenv("HOSTLIER_SANDBOX_K3S_EXPORT"); output != "" && arch == goruntime.GOARCH {
					exportSandboxManifests(t, output, manifests, p)
				}
			})
	}
}

// sandboxPackageProject uses real hooks and distinct test-only secrets for both sidecars.
func sandboxPackageProject(t *testing.T, dir, arch string) (model.Project, map[string]string) {
	t.Helper()
	p := model.Project{Version: 2, Name: "sandbox-test", Image: "hostlier-sandbox-browser:local", Port: 8080,
		Command:  []string{"node", "-e", "setInterval(() => {}, 1000)"},
		Health:   &model.Health{Exec: []string{"node", "-e", "process.exit(0)"}},
		Sidecars: map[string]model.Project{}, Bindings: map[string]string{}}
	values := map[string]string{}
	// Contribution decoding uses the same typed wire contract as CLI composition.
	for index, kind := range []string{"browser", "computer"} {
		request := `{"version":1,"architecture":"` + arch + `","config":{"image":"hostlier-sandbox-` + kind + `:local","tokenSecret":"` + kind + `-token","cpu":"1"}}`
		cmd := exec.Command(filepath.Join(dir, "hostlier-sandbox-"+kind), "project-contribute")
		cmd.Stdin = strings.NewReader(request)
		raw, err := cmd.Output()
		// A failing hook must not leave a partial scenario that appears to pass.
		if err != nil {
			t.Fatal(err)
		}
		var c packagekit.ProjectContribution
		// Missing or incompatible contribution fields are a protocol regression.
		if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&c); err != nil || len(c.Sidecars) != 1 {
			t.Fatalf("invalid %s contribution: %s (%v)", kind, raw, err)
		}
		member := c.Sidecars[kind]
		member.Version = 2
		member.Image, member.Port = member.Source.Image, member.Endpoint.Port
		p.Sidecars[kind] = member
		// Runtime preparation normally resolves these stored-token references.
		for env := range c.Secrets {
			values[env] = strings.Repeat(string(rune('a'+index)), 64)
		}
		values[kind+"/SANDBOX_TOKEN"] = strings.Repeat(string(rune('a'+index)), 64)
		// Endpoint values are resolved by the renderer from each declared member.
		for key, value := range c.Bindings {
			p.Bindings[key] = value
		}
	}
	return p, values
}

// assertSandboxPod checks startup ordering, scoped credentials and distinct scratch mounts.
func assertSandboxPod(t *testing.T, manifests []map[string]any, p model.Project) {
	t.Helper()
	// Locate the single candidate rather than depending on ancillary object ordering.
	for _, m := range manifests {
		// Only the Deployment owns application containers.
		if m["kind"] != "Deployment" {
			continue
		}
		pod := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		init := pod["initContainers"].([]any)
		// Both scratch initializers must finish before either native sidecar starts.
		if len(init) != 4 || init[0].(map[string]any)["image"] != collectorTrustHelper || init[1].(map[string]any)["image"] != collectorTrustHelper {
			t.Fatal("missing scratch setup before sidecars")
		}
		scratch := sandboxScratchMounts(t, init[2:], p)
		// Shared loopback does not imply shared desktop or browser files.
		if len(scratch) != 2 || pod["automountServiceAccountToken"] != false {
			t.Fatal("scratch or Kubernetes credential isolation lost")
		}
		assertSandboxScratchVolumes(t, pod, scratch)
		return
	}
	t.Fatal("missing Deployment")
}

// assertSandboxContainer verifies the real package policy reaches the Kubernetes container.
func assertSandboxContainer(t *testing.T, c map[string]any, p model.Project) {
	t.Helper()
	name := c["name"].(string)
	sec := c["securityContext"].(map[string]any)
	assertK3sSecurityIdentity(t, sec)
	profile := sec["seccompProfile"].(map[string]any)
	// Localhost refers to the exact reviewed profile, with no unconfined fallback.
	if profile["type"] != "Localhost" || profile["localhostProfile"] != seccompFilename(p.Sidecars[name]) {
		t.Fatal("wrong seccomp profile", profile)
	}
	// A required sidecar must gate both startup and ongoing Pod readiness.
	if c["restartPolicy"] != "Always" || c["readinessProbe"] == nil || c["startupProbe"] == nil {
		t.Fatal("missing native sidecar readiness", name)
	}
	assertSandboxTokenScope(t, c, name)
	probe := c["startupProbe"].(map[string]any)
	// Health runs inside the Pod network namespace and authenticates using the scoped token.
	if probe["failureThreshold"] != 120 || strings.Join(probe["exec"].(map[string]any)["command"].([]string), " ") != "node /opt/hostlier-sandbox/health.mjs" {
		t.Fatal("wrong sandbox probe", probe)
	}
}

// assertSandboxTokenScope prevents accidental sibling or parent token injection.
func assertSandboxTokenScope(t *testing.T, c map[string]any, name string) {
	t.Helper()
	raw, _ := json.Marshal(c["env"])
	// Never expose either parent's or sibling's token to a sandbox.
	if !strings.Contains(string(raw), `"key":"`+name+`.SANDBOX_TOKEN"`) || strings.Contains(string(raw), "BROWSER_TOKEN") || strings.Contains(string(raw), "COMPUTER_TOKEN") {
		t.Fatal("wrong token scope", string(raw))
	}
}

// exportSandboxManifests writes only synthetic test data for the opt-in local cluster harness.
func exportSandboxManifests(t *testing.T, output string, manifests []map[string]any, p model.Project) {
	t.Helper()
	// The destination is explicit; normal test runs only use temporary directories.
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(map[string]any{"apiVersion": "v1", "kind": "List", "items": manifests}, "", "  ")
	// Export includes test-only credentials, so keep fixture files private.
	if err := os.WriteFile(filepath.Join(output, "manifests.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Both profiles share a content-derived filename when targeting the same CPU.
	for _, member := range p.Sidecars {
		// Preserve byte identity between the exported file and Localhost reference.
		if err := os.WriteFile(filepath.Join(output, seccompFilename(member)), []byte(member.Configs[member.Security.SeccompConfig].Content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// sandboxScratchMounts checks native sidecars and collects their private scratch volumes.
func sandboxScratchMounts(t *testing.T, containers []any, p model.Project) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	// Both package types retain identical confinement and readiness guarantees.
	for _, item := range containers {
		c := item.(map[string]any)
		assertSandboxContainer(t, c, p)
		// Each member owns an independent memory-backed scratch directory.
		for _, mount := range c["volumeMounts"].([]any) {
			v := mount.(map[string]any)
			// ConfigMap mounts are independent from writable scratch.
			if v["mountPath"] == "/tmp" {
				out[v["name"].(string)] = true
			}
		}
	}
	return out
}

// assertSandboxScratchVolumes ensures each mount has a bounded memory backing.
func assertSandboxScratchVolumes(t *testing.T, pod map[string]any, scratch map[string]bool) {
	t.Helper()
	// Locate volumes by the exact names referenced by each sandbox.
	for _, item := range pod["volumes"].([]any) {
		v := item.(map[string]any)
		// Other volumes carry read-only configuration.
		if !scratch[v["name"].(string)] {
			continue
		}
		empty := v["emptyDir"].(map[string]any)
		// Kubernetes must cap both scratch volumes independently.
		if empty["medium"] != "Memory" || empty["sizeLimit"] != "1Gi" {
			t.Fatal("unbounded or disk-backed scratch", empty)
		}
		delete(scratch, v["name"].(string))
	}
	// Every referenced scratch volume must be declared.
	if len(scratch) != 0 {
		t.Fatal("missing scratch volumes", scratch)
	}
}
