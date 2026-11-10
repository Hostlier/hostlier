package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// TestLegacyCandidateUsesImportedArtifact prevents successful CLI uploads from failing at Docker container creation.
func TestLegacyCandidateUsesImportedArtifact(t *testing.T) {
	image := "hostlier.local/artifacts@sha256:" + strings.Repeat("a", 64)
	r := &Runtime{owner: "legacy-test"}
	created := false
	// Exercise the real legacy candidate path, including its initial absence and ownership-checked reinspection.
	r.docker = &http.Client{Transport: sidecarTransport(func(request *http.Request) (*http.Response, error) {
		// A missing candidate permits exactly one container create.
		if request.Method == "GET" && !created {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("missing"))}, nil
		}
		// Verify the runnable local tag at the daemon boundary, while retaining the immutable approved reference.
		if request.Method == "POST" {
			var body struct{ Image string }
			// The content reference is not itself a Docker repository digest.
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Image != "hostlier.local/artifacts:"+strings.Repeat("a", 64) {
				t.Fatalf("legacy Docker create image=%s error=%v", body.Image, err)
			}
			created = true
			return sidecarResponse(map[string]string{"Id": "candidate"})
		}
		return sidecarResponse(map[string]any{"Id": "candidate", "Config": map[string]any{"Labels": r.labels("app")}})
	})}
	d := &dockerDeployment{runtime: r, ctx: context.Background(), spec: model.Project{Version: 1, Name: "app", Image: image, Port: 3011}, name: "candidate", network: "test-network"}
	// The candidate must be created using imported content without changing the approved image identity.
	if err := d.candidateLegacy(); err != nil || !created || d.spec.Image != image || d.candidate.ID != "candidate" {
		t.Fatalf("candidate=%s approved=%s created=%t error=%v", d.candidate.ID, d.spec.Image, created, err)
	}
}
