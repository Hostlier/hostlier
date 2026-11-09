package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/dockerlogin"
)

// TestDockerLoginOwnerSurvivesRetry ensures a later root invocation cannot change the credential owner.
func TestDockerLoginOwnerSurvivesRetry(t *testing.T) {
	owner := &dockerlogin.Source{Username: "alice", UID: "1000", GID: "1000", Home: "/home/alice", Directory: "/home/alice/.docker"}
	other := &dockerlogin.Source{Username: "root", UID: "0", GID: "0", Home: "/root", Directory: "/root/.docker"}
	options, err := resolveDockerLogin(Options{Runtime: "docker", DockerLogin: other}, &Installation{DockerLogin: owner})
	// Credential source changes require explicit metadata changes, never ambient sudo state.
	if err != nil || options.DockerLogin != owner {
		t.Fatal("retry replaced login owner")
	}
	directory := t.TempDir()
	raw, _ := json.Marshal(Installation{DockerLogin: owner})
	// Exercise the same persisted record loaded by the installed controller.
	if err = os.WriteFile(filepath.Join(directory, "installation.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := DockerLoginSource(directory)
	// The system service must reload the exact reviewed account and config location.
	if err != nil || *source != *owner {
		t.Fatal("controller lost login source")
	}
}

// TestDockerLoginServiceIsolation checks read-only access without exposing unrelated homes or modifying Docker units.
func TestDockerLoginServiceIsolation(t *testing.T) {
	source := &dockerlogin.Source{Username: "alice", UID: "1000", GID: "1000", Home: "/home/alice", Directory: "/opt/alice-docker"}
	assets, err := InstallationAssets(Options{Domain: "apps.example.com", Runtime: "docker", DockerMode: "existing", DockerLogin: source})
	// Only Hostlier's unit should gain narrowly scoped read-only login mounts.
	if err != nil || !strings.Contains(assets.Service, "ProtectHome=tmpfs\nBindReadOnlyPaths=-/home/alice -/opt/alice-docker -/run/user/1000") || strings.Contains(assets.Service, "Requires=docker.service") {
		t.Fatal("login service isolation missing", err)
	}
	source.Directory = "/tmp/config\nUser=alice"
	_, err = InstallationAssets(Options{Domain: "apps.example.com", DockerLogin: source})
	// Config paths may not inject additional systemd directives.
	if err == nil {
		t.Fatal("unit injection accepted")
	}
}

// TestDockerLoginK3sRuntime keeps the selected login and read-only mounts for k3s, never for unknown runtimes.
func TestDockerLoginK3sRuntime(t *testing.T) {
	source := &dockerlogin.Source{Username: "alice", UID: "1000", GID: "1000", Home: "/home/alice", Directory: "/opt/alice-docker"}
	options, err := resolveDockerLogin(Options{Runtime: "k3s", DockerLogin: source}, nil)
	// k3s keeps the reviewed login owner for namespace-scoped pull secrets.
	if err != nil || options.DockerLogin != source {
		t.Fatal("k3s lost the selected Docker login", err)
	}
	assets, err := InstallationAssets(Options{Domain: "apps.example.com", Runtime: "k3s", DockerLogin: source})
	// The k3s controller gains the same narrow read-only mounts as Docker.
	if err != nil || !strings.Contains(assets.Service, "BindReadOnlyPaths=-/home/alice -/opt/alice-docker -/run/user/1000") {
		t.Fatal("k3s login mounts missing", err)
	}
	options, err = resolveDockerLogin(Options{Runtime: "podman", DockerLogin: source}, nil)
	// Unknown runtimes never receive credential authority.
	if err != nil || options.DockerLogin != nil {
		t.Fatal("unsupported runtime adopted Docker credentials")
	}
}
