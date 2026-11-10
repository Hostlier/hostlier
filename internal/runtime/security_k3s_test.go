package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// k3sSecurityFixture keeps this migration step within its validated recovery boundary.
func k3sSecurityFixture(t *testing.T) *K3s {
	t.Helper()
	adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir(), K3sSecurity: &K3sSecurityPolicy{Version: 1, PodPidsLimit: 256}})
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		t.Fatal(err)
	}
	return adapter.(*K3s)
}

// marginSecurityProject keeps this migration step within its validated recovery boundary.
func marginSecurityProject() model.Project {
	return model.Project{Version: 2, Name: "margin", Image: "example@sha256:" + strings.Repeat("a", 64), Port: 3011, Resources: &model.Limits{Memory: "1Gi"}, Security: &model.ContainerSecurity{NoSwap: true, RunAsUser: 1000, RunAsGroup: 1000, ReadOnlyRootFilesystem: true, NoNewPrivileges: true, DropCapabilities: []string{"ALL"}, PidsLimit: 256, Tmpfs: map[string]model.TmpfsMount{"/tmp": {Size: "16Mi", Mode: 01777}}}}
}

// TestK3sMarginSecurityRetainsConfinementAndScratch keeps this migration step within its validated recovery boundary.
func TestK3sMarginSecurityRetainsConfinementAndScratch(t *testing.T) {
	k := k3sSecurityFixture(t)
	p := marginSecurityProject()
	manifests, err := k.K3sManifests(p, model.DeployContext{OperationID: "migrate"})
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		t.Fatal(err)
	}
	// Exercise each fixture case against the same recovery invariant.
	for _, m := range manifests {
		// Check the observed result against the fixture's migration invariant.
		if m["kind"] != "Deployment" {
			continue
		}
		pod := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		app := pod["containers"].([]any)[0].(map[string]any)
		sec := app["securityContext"].(map[string]any)
		assertK3sSecurityIdentity(t, sec)
		raw, _ := json.Marshal(sec)
		// Check the observed result against the fixture's migration invariant.
		if !strings.Contains(string(raw), `"drop":["ALL"]`) || !strings.Contains(string(raw), "RuntimeDefault") {
			t.Fatal(string(raw))
		}
		volume := pod["volumes"].([]any)[0].(map[string]any)["emptyDir"].(map[string]any)
		// Check the observed result against the fixture's migration invariant.
		if volume["medium"] != "Memory" || volume["sizeLimit"] != "16Mi" {
			t.Fatal(volume)
		}
		assertK3sScratchSetup(t, pod)
	}
}

// TestK3sSecurityNeverWeakensProcessLimit keeps this migration step within its validated recovery boundary.
func TestK3sSecurityNeverWeakensProcessLimit(t *testing.T) {
	k := k3sSecurityFixture(t)
	p := marginSecurityProject()
	p.Security.PidsLimit = 128
	// Check the observed result against the fixture's migration invariant.
	if _, err := k.K3sManifests(p, model.DeployContext{OperationID: "migrate"}); err == nil {
		t.Fatal("larger shared PID limit accepted")
	}
	k.options.K3sSecurity = nil
	// Check the observed result against the fixture's migration invariant.
	if _, err := k.K3sManifests(p, model.DeployContext{OperationID: "migrate"}); err == nil {
		t.Fatal("missing node policy accepted")
	}
}

// TestK3sSecurityKeepsSidecarScopesAndRejectsDockerSeccompExtensions keeps this migration step within its validated recovery boundary.
func TestK3sSecurityKeepsSidecarScopesAndRejectsDockerSeccompExtensions(t *testing.T) {
	k := k3sSecurityFixture(t)
	p := marginSecurityProject()
	member := marginSecurityProject()
	member.Name = "storage"
	member.Security.RunAsUser = 999
	member.Security.RunAsGroup = 999
	p.Sidecars = map[string]model.Project{"storage": member}
	manifests, err := k.K3sManifests(p, model.DeployContext{OperationID: "migrate"})
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		t.Fatal(err)
	}
	// Exercise each fixture case against the same recovery invariant.
	for _, m := range manifests {
		// Check the observed result against the fixture's migration invariant.
		if m["kind"] != "Deployment" {
			continue
		}
		pod := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		init := pod["initContainers"].([]any)
		// Check the observed result against the fixture's migration invariant.
		if len(init) != 3 {
			t.Fatal(init)
		}
		sidecar := init[2].(map[string]any)
		// Check the observed result against the fixture's migration invariant.
		if sidecar["securityContext"].(map[string]any)["runAsUser"] != uint32(999) {
			t.Fatal(sidecar)
		}
	}
	p.Security.SeccompConfig = "policy"
	p.Configs = map[string]model.ConfigFile{"policy": {Content: `{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[{"names":["read"],"action":"SCMP_ACT_ALLOW","includes":{"caps":["CAP_SYS_ADMIN"]}}]}`}}
	// Check the observed result against the fixture's migration invariant.
	if _, err := k.K3sManifests(p, model.DeployContext{OperationID: "migrate"}); err == nil {
		t.Fatal("Docker conditional seccomp rules silently accepted")
	}
}

// assertK3sSecurityIdentity compares every required process boundary independently.
func assertK3sSecurityIdentity(t *testing.T, actual map[string]any) {
	t.Helper()
	expected := map[string]any{"runAsUser": uint32(1000), "runAsGroup": uint32(1000), "runAsNonRoot": true, "readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false}
	// Exercise each fixture case against the same recovery invariant.
	for key, value := range expected {
		// Check the observed result against the fixture's migration invariant.
		if actual[key] != value {
			t.Fatal(key, actual[key], value)
		}
	}
}

// assertK3sScratchSetup checks the pinned helper and exact numeric ownership/mode.
func assertK3sScratchSetup(t *testing.T, pod map[string]any) {
	t.Helper()
	setup := pod["initContainers"].([]any)[0].(map[string]any)
	// Check the observed result against the fixture's migration invariant.
	if setup["image"] != collectorTrustHelper {
		t.Fatal("scratch helper not immutable")
	}
	command := strings.Join(setup["command"].([]string), " ")
	// Check the observed result against the fixture's migration invariant.
	if !strings.Contains(command, "chown 1000:1000 /scratch") || !strings.Contains(command, "chmod 1777 /scratch") {
		t.Fatal(command)
	}
}
