package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hostlier/internal/artifact"
	"hostlier/internal/model"
)

// legacyRegistryProject keeps the version-one document shape while selecting an ordinary registry image.
func legacyRegistryProject(t *testing.T, reference string) string {
	t.Helper()
	dir := containerProject(t)
	p := model.Project{Version: 1, Name: "legacy", Image: reference, Port: 8080, Route: "legacy.example.com", Env: map[string]string{"NODE_ENV": "production"}}
	raw, err := json.Marshal(p)
	if err != nil { // The fixture uses actual strict legacy fields rather than a version-two source.
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hostlier"), raw, 0600); err != nil { // Keep the portable archive available for the fake controller's successful download.
		t.Fatal(err)
	}
	return dir
}

// assertLegacyRegistryPlan verifies preparation does not introduce version-two fields or change app settings.
func assertLegacyRegistryPlan(t *testing.T, c call) {
	t.Helper()
	p := c.body.(map[string]any)["spec"].(model.Project)
	if p.Version != 1 || p.Source != nil || !artifact.IsImage(p.Image) { // The controller must receive a valid version-one project naming verified content.
		t.Fatal("registry preparation changed the legacy schema", p)
	}
	if p.Port != 8080 || p.Route != "legacy.example.com" || p.Env["NODE_ENV"] != "production" { // Existing routing and runtime settings remain part of the same deployment intent.
		t.Fatal("registry preparation changed legacy app settings", p)
	}
}

// TestTopLevelImageControllerFirst detects the receiver and stages a legacy image before consulting local login.
func TestTopLevelImageControllerFirst(t *testing.T) {
	dir := legacyRegistryProject(t, "ghcr.io/team/private:latest")
	receipt, err := artifact.PrepareContainer(t.TempDir(), filepath.Join(dir, "app.tar"), "amd64")
	if err != nil { // The fake controller must report actual verifiable image bytes.
		t.Fatal(err)
	}
	fixture := &containerPlanFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "controller-download"}}
	if _, err := fixture.store.Import(context.Background(), receipt); err != nil { // Populate the same shared artifact store inspected during planning.
		t.Fatal(err)
	}
	login := t.TempDir()
	t.Setenv("DOCKER_CONFIG", login)
	if err := os.WriteFile(filepath.Join(login, "config.json"), []byte("unusable local login"), 0600); err != nil { // A successful server-first attempt must never need local registry credentials.
		t.Fatal(err)
	}
	remote := &fakeClient{reply: func(c call) (any, error) { // Allow only ordinary receiver discovery, server staging and final planning.
		if c.path == "/v1/artifacts/pull" {
			return receipt, nil
		}
		if c.path == "/v1/plans" { // Verify the legacy schema before independent artifact inspection.
			assertLegacyRegistryPlan(t, c)
		}
		return fixture.reply(c)
	}}
	assertRegistryValidationOffline(t, dir, remote)
	if _, err := invoke([]string{"plan"}, "", Options{CWD: dir, Client: remote}); err != nil { // Top-level image requires no architecture or source declaration.
		t.Fatal(err)
	}
	if len(remote.calls) != 3 || fixture.chunks != 0 { // Discovery and a successful server pull precede the plan without uploading image bytes.
		t.Fatal(remote.calls, fixture.chunks)
	}
}

// TestTopLevelImageLocalFallback imports locally authenticated content when the controller cannot access the registry.
func TestTopLevelImageLocalFallback(t *testing.T) {
	registry := newRegistryPullFixture(t, false)
	localRegistryLogin(t, registry.ref.RegistryStr(), false)
	dir := legacyRegistryProject(t, registry.ref.Name())
	fixture := &containerPlanFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "legacy-fallback"}}
	remote := &fakeClient{reply: func(c call) (any, error) { // The existing registry fixture models a missing controller connection or login.
		if c.path == "/v1/plans" {
			assertLegacyRegistryPlan(t, c)
		}
		return fixture.reply(c)
	}}
	if _, err := invoke([]string{"plan"}, "", Options{CWD: dir, Client: remote}); err != nil { // The CLI must pull, verify and upload before submitting the legacy deployment.
		t.Fatal(err)
	}
	if fixture.chunks == 0 || registry.tokens.Load() == 0 || fixture.plans != 1 { // Both authenticated download and independent server-side artifact publication are required.
		t.Fatal("legacy fallback did not transfer verified content", fixture.chunks, registry.tokens.Load(), fixture.plans)
	}
	if remote.calls[0].path != "/v1/artifacts/platform" || remote.calls[1].path != "/v1/artifacts/pull" { // The controller gets the first attempt even when local credentials are available.
		t.Fatal(remote.calls)
	}
}
