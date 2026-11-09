package cli

import (
	"fmt"
	"path/filepath"
	"testing"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// TestFusedProvisionReusesAppGrant ensures updates use the specialized controller route without a new OAuth ceremony.
func TestFusedProvisionReusesAppGrant(t *testing.T) {
	for _, unchanged := range []bool{true, false} {
		// Each case owns an independent compiler artifact and output file.
		t.Run(fmt.Sprint("unchanged=", unchanged), func(t *testing.T) {
			path, digest := pendingRuntimeFixture(t)
			runtime := model.FusedConnection{GRPCURL: "https://grpc.example.com", AppID: "11111111-2222-4333-8444-555555555555", TokenSecret: "mini-token"}
			currentDigest := "sha256:previous"
			// Matching builds bind without rotating credentials or publishing another version.
			if unchanged {
				currentDigest = digest
			}
			mutations := 0
			client := &fakeClient{reply: func(request call) (any, error) {
				// Existing authenticated connection metadata is sufficient to choose the deployment path.
				if request.method == "GET" {
					return map[string]any{"connections": map[string]any{"mini": map[string]any{"status": "connected", "engineUrl": "https://engine.example.com", "manifestDigest": currentDigest, "runtime": runtime}}}, nil
				}
				// A version update must never request a broad management token or register another client.
				if request.method != "POST" || request.path != "/v1/fused/mini/deploy" {
					t.Fatalf("unexpected mutation: %#v", request)
				}
				mutations++
				currentDigest = digest
				return map[string]any{"status": "connected"}, nil
			}}
			_, err := invoke([]string{"fused", "provision", "mini", "--file", path, "--socket", "/test/controller.sock"}, "", Options{Client: client, Home: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if unchanged {
				expected = 0
			}
			if mutations != expected {
				t.Fatalf("mutations=%d want %d", mutations, expected)
			}
			project, err := config.ReadProject(filepath.Join(filepath.Dir(path), ".hostlier"))
			if err != nil || project.Fused.AppID != runtime.AppID {
				t.Fatal(err, project.Fused)
			}
		})
	}
}
