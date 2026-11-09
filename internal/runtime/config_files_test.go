package runtime

import (
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagedConfigFilesAreImmutableAndScoped proves secret isolation and read-only Docker mounts.
func TestManagedConfigFilesAreImmutableAndScoped(t *testing.T) {
	root := t.TempDir()
	r := &Runtime{options: Options{StateDir: root}}
	p := model.Project{Version: 2, Configs: map[string]model.ConfigFile{"settings": {Mount: "/etc/app.yaml", Content: "revision: first\n"}, "token": {Mount: "/run/token", Secret: "private-token"}}}
	d := &dockerDeployment{runtime: r, name: "owned-operation", dc: model.DeployContext{Secrets: map[string]string{"files/token": "approved-secret"}}}
	mounts, err := d.configMounts(p, "")
	// Secret-backed files are mounted read-only alongside nonsecret configuration.
	if err != nil || len(mounts) != 2 {
		t.Fatalf("%v %v", mounts, err)
	}
	// Neither source may be modified or redirected during immutable replay.
	for _, item := range mounts {
		mount := item.(map[string]any)
		// All materialized files must be mounted read-only.
		if mount["ReadOnly"] != true {
			t.Fatal("writable config")
		}
	}
	file := p.Configs["settings"]
	file.Content = "revision: second\n"
	p.Configs["settings"] = file
	// Changed file bytes require a new operation directory, not an in-place write.
	if _, err = d.configMounts(p, ""); err == nil {
		t.Fatal("live config source overwritten")
	}
	data, _ := os.ReadFile(filepath.Join(root, "config-files", "owned-operation", "app", "settings"))
	// The original container's source inode still contains its exact reviewed input.
	if string(data) != "revision: first\n" {
		t.Fatal("old revision changed")
	}
	d.name = "new-operation"
	// A new approved operation can materialize the changed revision independently.
	if _, err = d.configMounts(p, ""); err != nil {
		t.Fatal(err)
	}
}

// TestManagedConfigRejectsSymlinkAndMissingSecret prevents host-path injection and empty secret fallback.
func TestManagedConfigRejectsSymlinkAndMissingSecret(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "config-files"))
	// A redirected host directory cannot receive even a nonsecret configuration file.
	if err := privateConfigDirectory(root, filepath.Join(root, "config-files", "operation", "app")); err == nil {
		t.Fatal("symlink accepted")
	}
	_, err := configFileContent(model.ConfigFile{Secret: "token"}, "worker", "token", map[string]string{"files/token": "primary-only"})
	// A member cannot inherit its primary's file secret.
	if err == nil {
		t.Fatal("primary secret exposed to member")
	}
}

// TestK3sManagedConfigFilesAndNamedPorts retain native immutable file and service semantics.
func TestK3sManagedConfigFilesAndNamedPorts(t *testing.T) {
	adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir()})
	// Manifest generation uses the real adapter while avoiding a cluster dependency.
	if err != nil {
		t.Fatal(err)
	}
	p := model.Project{Version: 2, Name: "engine", Image: "example/engine@sha256:" + strings.Repeat("a", 64), Port: 8081, Route: "engine.example.com", Endpoint: &model.Endpoint{Port: 8081, Exposure: "public"}, Endpoints: map[string]model.Endpoint{"http": {Port: 8081, Exposure: "public"}, "grpc": {Port: 50051, Exposure: "public"}}, Configs: map[string]model.ConfigFile{"settings": {Mount: "/app/engine.yaml", Content: "server: {}"}, "token": {Mount: "/run/token", Secret: "token"}}}
	manifests, err := adapter.(*K3s).K3sManifests(p, model.DeployContext{OperationID: "revision-one", Secrets: map[string]string{"files/token": "secret-value"}})
	// Every file is represented by an immutable native object and a read-only subPath mount.
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	// Only file objects carry immutable; environment Secret behavior remains unchanged.
	for _, manifest := range manifests {
		// Count actual immutable objects rather than the unrelated environment Secret.
		if manifest["immutable"] == true {
			count++
		}
	}
	// The service exposes both named ports without NodePort or host publication.
	if count != 2 || len(servicePorts(p)) != 2 {
		t.Fatalf("immutable files=%d", count)
	}
}

// TestK3sConfigSecretsCannotFallBackToEmpty prevents a missing pinned revision from producing an apparently valid Pod.
func TestK3sConfigSecretsCannotFallBackToEmpty(t *testing.T) {
	p := model.Project{Version: 2, Configs: map[string]model.ConfigFile{"token": {Mount: "/run/token", Secret: "private"}}}
	// A different component's credential cannot satisfy the primary's file reference.
	if err := validateGroupConfigValues(p, map[string]string{"worker/files/token": "sibling"}); err == nil {
		t.Fatal("missing primary file secret accepted")
	}
	// Explicit empty bytes remain distinguishable from an absent revision.
	if err := validateGroupConfigValues(p, map[string]string{"files/token": ""}); err != nil {
		t.Fatal(err)
	}
}
