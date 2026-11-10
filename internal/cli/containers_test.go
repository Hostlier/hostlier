package cli

import (
	"encoding/json"
	"fmt"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// containerProject writes an explicit project-relative archive fixture for ordinary CLI invocation.
func containerProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	image, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: "amd64", Config: v1.Config{Cmd: []string{"node", "server.mjs"}}})
	// The fixture must be an actual runnable Linux image configuration.
	if err != nil {
		t.Fatal(err)
	}
	tag, _ := name.NewTag("local/minion:test")
	// Serialize a single-image archive just as docker save does.
	if err = tarball.WriteToFile(filepath.Join(dir, "app.tar"), tag, image); err != nil {
		t.Fatal(err)
	}
	raw := "version: 2\nname: minion-chat\nsource:\n  container:\n    archive: app.tar\n    architecture: amd64\nendpoint:\n  exposure: public\n  port: 8080\n  route: chat.example.com\n"
	raw += "sidecars:\n  helper:\n    source:\n      container:\n        archive: app.tar\n        architecture: amd64\n    endpoint:\n      exposure: loopback\n      port: 8081\n    health:\n      exec: [\"true\"]\n"
	// A nested deployment source exercises project-relative path resolution.
	if err = os.WriteFile(filepath.Join(dir, ".hostlier"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// containerPlanFixture uses the real artifact store without granting local socket authority to the CLI.
type containerPlanFixture struct {
	store  artifact.Store
	chunks int
	plans  int
}

// reply accepts ordinary artifact transport followed by an immutable deployment plan.
func (f *containerPlanFixture) reply(c call) (any, error) {
	// Every request must use the existing authenticated API surface.
	switch {
	case c.path == "/v1/artifacts/platform": // Container images target the receiver, never the laptop CPU.
		return map[string]string{"os": "linux", "arch": "amd64"}, nil
	case c.path == "/v1/artifacts/pull": // Registry fixtures default to the explicitly tested local fallback path.
		return nil, model.Err("IMAGE_PULL", "Registry unavailable from fixture controller")
	case c.path == "/v1/artifacts/status": // Status retains independent content verification.
		return f.store.Status(c.body.(artifact.Result))
	case strings.HasSuffix(c.path, "/chunks"): // The real store verifies exact offset/replay rules.
		body := c.body.(map[string]any)
		parts := strings.Split(c.path, "/")
		f.chunks++
		offset, err := f.store.Chunk(parts[3], body["offset"].(int64), body["data"].([]byte))
		return map[string]int64{"offset": offset}, err
	case c.path == "/v1/artifacts/commit": // Publication uses the same verifier as the controller.
		var receipt artifact.Result
		raw, _ := json.Marshal(c.body)
		_ = json.Unmarshal(raw, &receipt)
		return f.store.Commit(receipt)
	case c.path == "/v1/plans": // Local source metadata must already be lowered and verified.
		return f.plan(c)
	default: // Planning must not create runtime workloads.
		return nil, fmt.Errorf("unexpected API %s", c.path)
	}
}

// TestContainerPlanUploadsWithoutSSHAndReusesContent verifies the complete local-archive-to-plan flow.
func TestContainerPlanUploadsWithoutSSHAndReusesContent(t *testing.T) {
	dir := containerProject(t)
	fixture := &containerPlanFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "paired-device"}}
	remote := &fakeClient{reply: fixture.reply}
	options := Options{Client: remote, CWD: t.TempDir()}
	args := []string{"plan", "--file", filepath.Join(dir, ".hostlier")}
	// No socket, SSH helper, registry or native Harnest source participates in preparation.
	if _, err := invoke(args, "", options); err != nil {
		t.Fatal(err)
	}
	chunks := fixture.chunks
	// A fresh CLI invocation should reuse already verified content on the controller.
	if _, err := invoke(args, "", options); err != nil {
		t.Fatal(err)
	}
	// Plans are reviewed separately while identical image bytes are transferred only once.
	if fixture.plans != 2 || chunks == 0 || fixture.chunks != chunks {
		t.Fatal(fixture.plans, fixture.chunks, chunks)
	}
}

// TestContainerValidateNeverBuildsOrConnects preserves offline validation semantics.
func TestContainerValidateNeverBuildsOrConnects(t *testing.T) {
	dir := containerProject(t)
	remote := &fakeClient{}
	// Archive validation needs neither Docker nor a saved controller connection.
	if _, err := invoke([]string{"validate"}, "", Options{CWD: dir, Client: remote}); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(dir, ".hostlier")
	raw, _ := os.ReadFile(project)
	raw = []byte(strings.ReplaceAll(string(raw), "archive: app.tar", "context: ."))
	// A Dockerfile that would fail is only inspected, never executed by validate.
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nRUN exit 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Select the explicit local build mode for a second offline validation.
	if err := os.WriteFile(project, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// The invalid build step must not matter until a plan/deploy explicitly builds it.
	if _, err := invoke([]string{"validate"}, "", Options{CWD: dir, Client: remote}); err != nil {
		t.Fatal(err)
	}
	// Validation cannot send an artifact, secret or approval request.
	if len(remote.calls) != 0 {
		t.Fatal(remote.calls)
	}
}

// TestContainerDockerBuildPlan optionally verifies real local Docker export through the CLI upload/plan flow.
func TestContainerDockerBuildPlan(t *testing.T) {
	context := os.Getenv("HOSTLIER_TEST_CONTAINER_CONTEXT")
	// Ordinary unit runs remain offline and do not require a Docker installation.
	if context == "" {
		t.Skip("set HOSTLIER_TEST_CONTAINER_CONTEXT for a real Docker build")
	}
	dir := t.TempDir()
	p := model.Project{Version: 2, Name: "minion-chat", Source: &model.Source{Container: &model.ContainerSource{Context: context, Architecture: "amd64"}}, Endpoint: &model.Endpoint{Exposure: "public", Port: 8080, Route: "chat.example.com"}}
	raw, err := json.Marshal(p)
	// JSON is a valid project input and preserves arbitrary local path characters.
	if err != nil {
		t.Fatal(err)
	}
	// The build uses a temporary project, never the user's saved profile or production controller.
	if err = os.WriteFile(filepath.Join(dir, ".hostlier"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	fixture := &containerPlanFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "local-test"}}
	// Real Docker bytes pass through the same scoped upload store and planning assertions.
	if _, err = invoke([]string{"plan"}, "", Options{CWD: dir, Client: &fakeClient{reply: fixture.reply}}); err != nil {
		t.Fatal(err)
	}
	// A successful plan must actually transfer a verified image first.
	if fixture.chunks == 0 || fixture.plans != 1 {
		t.Fatal(fixture.chunks, fixture.plans)
	}
}

// plan verifies every prepared workload before returning a reviewable fixture plan.
func (f *containerPlanFixture) plan(c call) (any, error) {
	p := c.body.(map[string]any)["spec"].(model.Project)
	// Generic containers cannot inherit an agent launcher or expose laptop paths to the controller.
	if p.Source != nil && p.Source.Container != nil {
		return nil, fmt.Errorf("unprepared container")
	}
	// A plan is admitted only when the uploaded content can be independently read back.
	if _, err := f.store.Inspect(p.Image); err != nil {
		return nil, err
	}
	// Sidecars omit a top-level identity but must still be prepared and independently retained.
	for _, member := range p.Sidecars {
		// No local sidecar source may leak into the immutable controller plan.
		if member.Source != nil && member.Source.Container != nil || member.Image == "" {
			return nil, fmt.Errorf("unprepared sidecar")
		}
		// Identical primary and sidecar images may share one verified transport.
		if _, err := f.store.Inspect(member.Image); err != nil {
			return nil, err
		}
	}
	f.plans++
	return map[string]any{"id": "container-plan", "fingerprint": "approved-image", "spec": p}, nil
}
