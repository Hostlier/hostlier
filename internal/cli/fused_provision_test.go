package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// pendingRuntimeFixture produces compiler-shaped artifacts without any runtime secret or app identity.
func pendingRuntimeFixture(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	manifest := `{"schema_version":1,"name":"mini","version":"1.0.0","services":{"crm":{"version":"v1","operations":["read"]}}}`
	digest := sha256.Sum256([]byte(manifest))
	hash := "sha256:" + hex.EncodeToString(digest[:])
	pending := map[string]any{"version": 2, "name": "mini", "source": map[string]string{"image": "example/mini:1.0.0"}, "endpoint": map[string]any{"exposure": "public", "port": 8080, "route": "mini.example.com"}, "fused": map[string]string{"grpcUrl": "https://grpc.example.com", "tokenSecret": "mini-token"}, "consent": map[string]string{"engineUrl": "https://engine.example.com", "manifest": "fused-access.json", "digest": hash}}
	raw, err := json.Marshal(pending)
	// Invalid fixture serialization must fail before writing misleading test artifacts.
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "hostlier-pending.json")
	writeRuntimeFixture(t, path, raw)
	writeRuntimeFixture(t, filepath.Join(directory, "fused-access.json"), []byte(manifest))
	return path, hash
}

// writeRuntimeFixture makes fixture setup failures visible at the caller.
func writeRuntimeFixture(t *testing.T, path string, raw []byte) {
	t.Helper()
	// Fixture writes must succeed before the behavior under test begins.
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestFusedBindExportsPublicIdentity proves recovery does not initiate OAuth or expose a runtime secret.
func TestFusedBindExportsPublicIdentity(t *testing.T) {
	path, digest := pendingRuntimeFixture(t)
	runtime := model.FusedConnection{GRPCURL: "https://grpc.example.com", AppID: "11111111-2222-4333-8444-555555555555", TokenSecret: "mini-token"}
	// The controller fixture exposes public identity and a secret reference only.
	client := &fakeClient{reply: func(request call) (any, error) {
		return map[string]any{"connections": map[string]any{"mini": map[string]any{"status": "connected", "engineUrl": "https://engine.example.com", "manifestDigest": digest, "runtime": runtime}}}, nil
	}}
	_, err := invoke([]string{"fused", "bind", "mini", "--file", path, "--socket", "/test/controller.sock"}, "", Options{Client: client, Home: t.TempDir()})
	// Binding must consist of one read-only controller request.
	if err != nil || len(client.calls) != 1 {
		t.Fatal(err, client.calls)
	}
	project, err := config.ReadProject(filepath.Join(filepath.Dir(path), ".hostlier"))
	// The exported project must pass normal deployment validation with the issued identity.
	if err != nil || project.Fused.AppID != runtime.AppID {
		t.Fatal(err, project.Fused)
	}
	// Reloading must derive the stored token reference from Fused, never upload its name as a literal.
	if project.Secrets["FUSED_APP_TOKEN"] != runtime.TokenSecret {
		t.Fatal("runtime token reference was not restored")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), ".hostlier"))
	var exported model.Project
	// Public artifacts retain the declarative tokenSecret without serializing a lowered secret binding.
	if err != nil || json.Unmarshal(raw, &exported) != nil || exported.Secrets["FUSED_APP_TOKEN"] != "" {
		t.Fatal("exported a lowered runtime token binding", err)
	}
	_, err = invoke([]string{"fused", "bind", "mini", "--file", path, "--socket", "/test/controller.sock"}, "", Options{Client: client, Home: t.TempDir()})
	// Repeating bind cannot replace a deployment file the user may have edited.
	if err == nil {
		t.Fatal("existing project overwritten")
	}
}

// TestPendingMinionRejectsChangedManifest ensures local compiler binding is checked before any network mutation.
func TestPendingMinionRejectsChangedManifest(t *testing.T) {
	path, _ := pendingRuntimeFixture(t)
	writeRuntimeFixture(t, filepath.Join(filepath.Dir(path), "fused-access.json"), []byte(`{"services":{}}`))
	_, _, err := readPendingMinion(path)
	// Replacing compiler requirements must force a rebuild rather than request different access.
	if err == nil {
		t.Fatal("modified manifest accepted")
	}
}
