package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/artifact"
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// registryTargetProject makes source.image the default primary and sidecar registry fixture.
func registryTargetProject(t *testing.T, reference string) string {
	t.Helper()
	dir := containerProject(t)
	path := filepath.Join(dir, ".hostlier")
	p, err := config.ReadProjectInput(path)
	if err != nil { // Missing project bytes cannot silently select another deployment input.
		t.Fatal(err)
	}
	p.Image, p.Source = "", &model.Source{Image: reference}
	for name, member := range p.Sidecars { // Ordinary sidecar registry sources use the exact same default flow.
		member.Image, member.Source = "", &model.Source{Image: reference}
		p.Sidecars[name] = member
	}
	raw, err := json.Marshal(p)
	if err != nil { // Keep the full fixture topology rather than constructing a reduced deployment example.
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil { // Preserve the project-relative file and existing sidecar topology.
		t.Fatal(err)
	}
	return dir
}

// TestPreparedImageSourcesSkipRegistry protects package outputs and retained artifacts from repeated preparation.
func TestPreparedImageSourcesSkipRegistry(t *testing.T) {
	image := "hostlier.local/artifacts@sha256:" + strings.Repeat("a", 64)
	remote := &fakeClient{}
	s := &session{ctx: context.Background(), remote: remote, flags: map[string]string{}, errOut: io.Discard}
	p := model.Project{Version: 2, Image: image, Source: &model.Source{Image: image}, Sidecars: map[string]model.Project{"helper": {Version: 2, Source: &model.Source{Image: image}}}}
	result, err := s.prepareArtifacts(p)
	if err != nil || result.Image != image || len(remote.calls) != 0 { // Prepared content must remain usable without a registry or controller connection.
		t.Fatal(result, err, remote.calls)
	}
	legacy := model.Project{Version: 1, Image: image}
	result, err = s.prepareArtifacts(legacy)
	if err != nil || result.Source != nil || result.Image != image || len(remote.calls) != 0 { // Legacy prepared revisions also remain registry-independent and retain their schema.
		t.Fatal(result, err, remote.calls)
	}
}

// TestRegistryControllerFirst avoids laptop credentials and uploads when the controller stages the image.
func TestRegistryControllerFirst(t *testing.T) {
	dir := registryTargetProject(t, "ghcr.io/team/private:latest")
	receipt, err := artifact.PrepareContainer(t.TempDir(), filepath.Join(dir, "app.tar"), "amd64")
	if err != nil { // The fake controller reports only real independently verified image content.
		t.Fatal(err)
	}
	fixture := &containerPlanFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "controller-download"}}
	if _, err := fixture.store.Import(context.Background(), receipt); err != nil { // Stage exactly the archive the server claims it downloaded.
		t.Fatal(err)
	}
	configDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", configDir)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("invalid laptop login"), 0600); err != nil { // Any local login lookup would fail this test.
		t.Fatal(err)
	}
	remote := &fakeClient{reply: func(c call) (any, error) { // Simulate successful server staging after receiver discovery.
		if c.path == "/v1/artifacts/pull" {
			return receipt, nil
		}
		return fixture.reply(c)
	}}
	assertRegistryValidationOffline(t, dir, remote)
	if _, err := invoke([]string{"plan"}, "", Options{CWD: dir, Client: remote}); err != nil { // No architecture, Docker daemon or usable local registry config is present.
		t.Fatal(err)
	}
	paths := []string{}
	for _, c := range remote.calls { // Capture the entire controller interaction, not only the final plan.
		paths = append(paths, c.path)
	}
	want := "/v1/artifacts/platform,/v1/artifacts/pull,/v1/artifacts/pull,/v1/plans"
	if strings.Join(paths, ",") != want || fixture.chunks != 0 { // Discovery is shared by the primary and sidecar; neither uploads.
		t.Fatal(paths, fixture.chunks)
	}
}

// assertRegistryValidationOffline checks source.image syntax without consulting controller or registry credentials.
func assertRegistryValidationOffline(t *testing.T, dir string, remote *fakeClient) {
	t.Helper()
	if _, err := invoke([]string{"validate"}, "", Options{CWD: dir, Client: remote}); err != nil { // An unusable laptop login must not affect offline project validation.
		t.Fatal(err)
	}
	if len(remote.calls) != 0 { // Validation never probes receiver architecture, registry access or deployment authority.
		t.Fatal(remote.calls)
	}
}

// TestRegistryFallbackUsesControllerArchitecture downloads the receiver platform after a server registry failure.
func TestRegistryFallbackUsesControllerArchitecture(t *testing.T) {
	f := newRegistryPullFixture(t, false)
	localRegistryLogin(t, f.ref.RegistryStr(), false)
	dir := registryTargetProject(t, f.ref.Name())
	fixture := &containerPlanFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "fallback"}}
	remote := &fakeClient{reply: func(c call) (any, error) { // The fixture receiver deliberately differs from the earlier amd64 archive.
		if c.path == "/v1/artifacts/platform" {
			return map[string]string{"os": "linux", "arch": "arm64"}, nil
		}
		return fixture.reply(c)
	}}
	if _, err := invoke([]string{"plan"}, "", Options{CWD: dir, Client: remote}); err != nil { // Both images must fall back to native local registry downloads.
		t.Fatal(err)
	}
	for _, c := range remote.calls { // Inspect uploaded content as well as the receiver constraint sent to the controller.
		if c.path == "/v1/artifacts/pull" {
			if c.body.(map[string]any)["architecture"] != "arm64" { // Never derive the requested platform from the laptop or old archive.
				t.Fatal(c.body)
			}
		}
		if c.path == "/v1/plans" { // The final source must be an imported arm64 artifact.
			p := c.body.(map[string]any)["spec"].(model.Project)
			receipt, err := fixture.store.Inspect(p.Image)
			if err != nil { // The controller must actually retain the committed downloaded image.
				t.Fatal(err)
			}
			assertRegistryArchive(t, receipt.Path, "arm64")
		}
	}
}

// TestRegistryPreparationErrorsDoNotFallback keeps trust, platform, storage and transport errors authoritative.
func TestRegistryPreparationErrorsDoNotFallback(t *testing.T) {
	for _, code := range []string{"UNAUTHORIZED", "SCOPE_DENIED", "TLS", "CONNECTION", "ARTIFACT_SPACE", "ARTIFACT_PLATFORM", "IMAGE_PLATFORM", "UNAVAILABLE"} { // These failures cannot be repaired with different registry credentials.
		t.Run(code, func(t *testing.T) { // A malformed local login makes accidental fallback immediately observable.
			dir := registryTargetProject(t, "ghcr.io/team/private:latest")
			remote := &fakeClient{reply: func(c call) (any, error) { // Receiver discovery succeeds before the exact authoritative failure.
				if c.path == "/v1/artifacts/platform" {
					return map[string]string{"os": "linux", "arch": "amd64"}, nil
				}
				return nil, model.Err(code, "fixture failure")
			}}
			_, err := invoke([]string{"plan"}, "", Options{CWD: dir, Client: remote})
			if planningErrorCode(err) != code || len(remote.calls) != 2 { // No artifact negotiation, upload or deployment plan is permitted.
				t.Fatal(err, remote.calls)
			}
		})
	}
}

// TestContainerArchitectureTargetsSelectedHost checks receiver discovery and explicit constraints through fleet routing.
func TestContainerArchitectureTargetsSelectedHost(t *testing.T) {
	remote := &fakeClient{reply: func(c call) (any, error) { // The node is the image receiver; the master must not select its own CPU.
		if c.path != "/v1/hosts/worker/proxy/artifacts/platform" {
			t.Fatal(c.path)
		}
		return map[string]string{"os": "linux", "arch": "arm64"}, nil
	}}
	s := &session{ctx: context.Background(), remote: remote, flags: map[string]string{"on": "worker"}, errOut: io.Discard}
	arch, err := s.resolveContainerArchitecture("")
	if err != nil || arch != "arm64" { // Omitted platform is discovered from the selected fleet receiver.
		t.Fatal(arch, err)
	}
	if _, err := s.resolveContainerArchitecture("amd64"); planningErrorCode(err) != "ARTIFACT_PLATFORM" { // Explicit constraints cannot override the actual receiver.
		t.Fatal(err)
	}
	if len(remote.calls) != 1 { // Primary and sidecar sources share one receiver snapshot per invocation.
		t.Fatal(remote.calls)
	}
}

// TestArchitectureFreeArchiveValidate checks offline preflight without requiring a receiver connection.
func TestArchitectureFreeArchiveValidate(t *testing.T) {
	dir := containerProject(t)
	path := filepath.Join(dir, ".hostlier")
	raw, _ := os.ReadFile(path)
	raw = []byte(strings.ReplaceAll(string(raw), "        architecture: amd64\n", ""))
	raw = []byte(strings.ReplaceAll(string(raw), "    architecture: amd64\n", ""))
	if err := os.WriteFile(path, raw, 0600); err != nil { // Remove only architecture while retaining valid local archives.
		t.Fatal(err)
	}
	remote := &fakeClient{}
	if _, err := invoke([]string{"validate"}, "", Options{CWD: dir, Client: remote}); err != nil { // Offline archive checks verify an allowed Linux platform before receiver discovery.
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(remote.calls)
	if len(remote.calls) != 0 { // No platform lookup or registry request may escape offline validation.
		t.Fatal(string(encoded))
	}
}

// TestRemovedContainerImageFailsOffline keeps obsolete declarations from triggering controller requests or local builds.
func TestRemovedContainerImageFailsOffline(t *testing.T) {
	dir := t.TempDir()
	document := "version: 2\nname: obsolete\nsource:\n  container:\n    context: .\n    image: ghcr.io/team/private:latest\nendpoint:\n  exposure: public\n  port: 8080\n"
	if err := os.WriteFile(filepath.Join(dir, ".hostlier"), []byte(document), 0600); err != nil { // Use the real project reader rather than constructing a removed Go field.
		t.Fatal(err)
	}
	remote := &fakeClient{}
	for _, command := range []string{"validate", "plan"} { // Both offline preflight and preparation must reject the old field before side effects.
		_, err := invoke([]string{command}, "", Options{CWD: dir, Client: remote})
		if planningErrorCode(err) != "INVALID_CONFIG" || len(remote.calls) != 0 { // Unknown nested fields cannot be ignored or treated as a registry download alias.
			t.Fatal(command, err, remote.calls)
		}
	}
}
