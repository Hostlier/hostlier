package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// TestMinionBindPreservesLocalPackagingAndSecretReferences verifies the guided build-to-provision output is safe to deploy.
func TestMinionBindPreservesLocalPackagingAndSecretReferences(t *testing.T) {
	path, digest := pendingRuntimeFixture(t)
	pending, _, err := readPendingMinion(path)
	// Start from a compiler-shaped artifact with a verified manifest.
	if err != nil {
		t.Fatal(err)
	}
	pending.Source = &model.Source{Container: &model.ContainerSource{Context: ".", Architecture: "arm64"}}
	pending.Secrets = map[string]string{"APP_TOKEN": "app-access-token"}
	raw, err := json.Marshal(pending)
	// Serialize only public references into the pending compiler fixture.
	if err != nil {
		t.Fatal(err)
	}
	writeRuntimeFixture(t, path, raw)
	runtime := model.FusedConnection{GRPCURL: "https://grpc.example.com", AppID: "11111111-2222-4333-8444-555555555555", TokenSecret: "mini-token"}
	// The controller fixture returns public connection metadata only.
	client := &fakeClient{reply: func(request call) (any, error) {
		return map[string]any{"connections": map[string]any{"mini": map[string]any{"status": "connected", "engineUrl": "https://engine.example.com", "manifestDigest": digest, "runtime": runtime}}}, nil
	}}
	_, err = invoke([]string{"fused", "bind", "mini", "--file", path, "--socket", "/test/controller.sock"}, "", Options{Client: client, Home: t.TempDir()})
	// Binding needs only authenticated metadata, with no browser ceremony in this unit fixture.
	if err != nil {
		t.Fatal(err)
	}
	input, err := config.ReadProjectSources(filepath.Join(filepath.Dir(path), ".hostlier"), "")
	// Reloading references must never enqueue their names as literal secret uploads.
	if err != nil || len(input.Secrets) != 0 {
		t.Fatal(err, input.Secrets)
	}
	source := input.Project.Source.Container
	// Deployment packages the selected local architecture instead of pulling an unpublished image.
	if source == nil || source.Context != "." || source.Architecture != "arm64" || input.Project.Secrets["APP_TOKEN"] != "app-access-token" {
		t.Fatal(input.Project)
	}
}
