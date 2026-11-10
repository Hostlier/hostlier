package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
)

// registryPreparationRuntime supplies real archive bytes without starting a container or contacting a daemon.
type registryPreparationRuntime struct {
	recordingRuntime
	archive []byte
	pulls   atomic.Int64
	entered chan struct{}
	gate    chan struct{}
}

// PrepareRegistryImage models network staging while exposing a gate for lock/cancellation verification.
func (r *registryPreparationRuntime) PrepareRegistryImage(ctx context.Context, image, dir string) (artifact.Result, error) {
	r.pulls.Add(1)
	if r.entered != nil { // Gated tests observe the exact point where slow registry work begins.
		close(r.entered)
	}
	if r.gate != nil { // Slow downloads must remain cancellable and cannot block unrelated API reads.
		select {
		case <-r.gate: // Complete only when the test releases the download.
		case <-ctx.Done(): // Abandon partial preparation on caller cancellation or shutdown.
			return artifact.Result{}, ctx.Err()
		}
	}
	path := filepath.Join(dir, "fixture.tar")
	if err := os.WriteFile(path, r.archive, 0600); err != nil { // Produce bytes only in the server-selected private preparation directory.
		return artifact.Result{}, err
	}
	return artifact.PrepareContainer(dir, path, runtime.GOARCH)
}

// registryPreparationArchive creates one Linux image for this controller's actual CPU architecture.
func registryPreparationArchive(t *testing.T) []byte {
	t.Helper()
	image, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: runtime.GOARCH})
	if err != nil { // Platform validation must inspect real runnable config bytes.
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "image.tar")
	tag, _ := name.NewTag("local/fixture:registry")
	if err := tarball.WriteToFile(path, tag, image); err != nil { // Use the same archive format as native registry downloads.
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil { // The fake runtime must stage the exact fixture archive.
		t.Fatal(err)
	}
	return raw
}

// TestControllerRegistryPreparation verifies authenticated inert staging and independent archive publication.
func TestControllerRegistryPreparation(t *testing.T) {
	r := &registryPreparationRuntime{archive: registryPreparationArchive(t)}
	c := newTestController(t, t.TempDir(), r, time.Now)
	defer closeController(t, c)
	request := map[string]string{"image": "ghcr.io/team/private:latest"}
	status, _ := call(t, c, false, "POST", "/v1/artifacts/pull", request, "")
	if status == 200 || r.pulls.Load() != 0 { // Unauthenticated requests never consult server credentials or download images.
		t.Fatal("unauthenticated preparation accepted", status)
	}
	status, platform := call(t, c, true, "GET", "/v1/artifacts/platform", nil, "")
	if status != 200 || platform["arch"] != runtime.GOARCH || platform["os"] != "linux" { // Discovery reports the runtime's Linux guest platform.
		t.Fatal(status, platform)
	}
	status, result := call(t, c, true, "POST", "/v1/artifacts/pull", request, "")
	if status != 200 { // Omitted architecture uses the same platform returned by discovery.
		t.Fatal(status, result)
	}
	raw, _ := json.Marshal(result)
	var receipt artifact.Result
	_ = json.Unmarshal(raw, &receipt)
	store := artifact.Store{Root: filepath.Join(c.opts.StateDir, "artifacts")}
	if _, err := store.Inspect(receipt.Image); err != nil { // Planning can consume the fully staged archive without another registry pull.
		t.Fatal(err)
	}
	if r.recordingRuntime.calls != 0 || strings.Contains(string(raw), c.opts.StateDir) { // Preparation cannot deploy a workload or expose a server path.
		t.Fatal("preparation executed or leaked state paths")
	}
}

// TestControllerRegistryMismatch rejects stale architecture constraints before any credential lookup or download.
func TestControllerRegistryMismatch(t *testing.T) {
	r := &registryPreparationRuntime{archive: registryPreparationArchive(t)}
	c := newTestController(t, t.TempDir(), r, time.Now)
	defer closeController(t, c)
	other := "amd64"
	if runtime.GOARCH == other { // Always request a platform different from the actual receiver.
		other = "arm64"
	}
	request := map[string]string{"image": "ghcr.io/team/private:latest", "architecture": other}
	status, result := call(t, c, true, "POST", "/v1/artifacts/pull", request, "")
	if code(result) != "ARTIFACT_PLATFORM" || r.pulls.Load() != 0 { // An explicit mismatch is rejected before the runtime is called.
		t.Fatal(status, result)
	}
}

// TestScopedControllerRegistryPreparation preserves signed device scope and grant lifetime on the new routes.
func TestScopedControllerRegistryPreparation(t *testing.T) {
	r := &registryPreparationRuntime{archive: registryPreparationArchive(t)}
	c := newTestController(t, t.TempDir(), r, time.Now)
	defer closeController(t, c)
	api, _, grantID := pairedFixture(t, c)
	var result any
	ctx := context.Background()
	if err := api.Request(ctx, "GET", "/v1/artifacts/platform", nil, &result); err != nil { // Deployment-scoped clients need receiver discovery without owner administration.
		t.Fatal(err)
	}
	request := map[string]string{"image": "ghcr.io/team/private:latest"}
	if err := api.Request(ctx, "POST", "/v1/artifacts/pull", request, &result); err != nil { // Inert staging uses the existing authenticated artifact boundary.
		t.Fatal(err)
	}
	if c.state.Authorizations[grantID].Uses != 0 { // Image preparation never consumes deployment approval or activates code.
		t.Fatal("preparation consumed a deployment allowance")
	}
	c.state.Authorizations[grantID].Revoked = true
	if err := api.Request(ctx, "POST", "/v1/artifacts/pull", request, &result); err == nil { // A revoked device cannot use the controller's registry connection.
		t.Fatal("revoked grant could prepare an image")
	}
}

// TestControllerRegistryPreparationReleasesLock verifies slow downloads do not monopolize controller state.
func TestControllerRegistryPreparationReleasesLock(t *testing.T) {
	r := &registryPreparationRuntime{archive: registryPreparationArchive(t), entered: make(chan struct{}), gate: make(chan struct{})}
	c := newTestController(t, t.TempDir(), r, time.Now)
	defer closeController(t, c)
	done := make(chan int, 1)
	go func() { // Hold the preparer in simulated network I/O while another request reads controller state.
		status, _ := call(t, c, true, "POST", "/v1/artifacts/pull", map[string]string{"image": "ghcr.io/team/private:latest"}, "")
		done <- status
	}()
	select { // Wait for actual slow work rather than assuming goroutine scheduling order.
	case <-r.entered: // Observe the start of slow registry I/O.
	case <-time.After(3 * time.Second): // A blocked preparation cannot silently hang the test.
		t.Fatal("registry preparation did not start")
	}
	read := make(chan int, 1)
	go func() { // A healthy controller must continue serving discovery during a large image download.
		status, _ := call(t, c, true, "GET", "/v1/artifacts/platform", nil, "")
		read <- status
	}()
	select { // The read must complete before the download gate is released.
	case status := <-read: // Status remains available while the image download is blocked.
		if status != 200 { // The concurrent request retains normal authenticated routing.
			t.Fatal(status)
		}
	case <-time.After(time.Second): // Release the fixture before reporting a lock-order regression.
		close(r.gate)
		t.Fatal("registry download held controller state lock")
	}
	close(r.gate)
	if status := <-done; status != 200 { // Releasing the download publishes verified content through the ordinary store.
		t.Fatal(status)
	}
}

// TestControllerRegistryTimeoutReturnsFallback leaves local fallback available after a server download stalls.
func TestControllerRegistryTimeoutReturnsFallback(t *testing.T) {
	r := &registryPreparationRuntime{entered: make(chan struct{}), gate: make(chan struct{})}
	c := newTestController(t, t.TempDir(), r, time.Now)
	defer closeController(t, c)
	c.opts.LongOperationTimeout = 20 * time.Millisecond
	status, result := call(t, c, true, "POST", "/v1/artifacts/pull", map[string]string{"image": "ghcr.io/team/private:latest"}, "")
	if code(result) != "IMAGE_PULL" { // A still-connected CLI receives a registry failure rather than an ambiguous controller timeout.
		t.Fatal(status, result)
	}
}

// TestControllerRegistryRevocationDuringDownload rechecks authority before publishing shared image content.
func TestControllerRegistryRevocationDuringDownload(t *testing.T) {
	r := &registryPreparationRuntime{archive: registryPreparationArchive(t), entered: make(chan struct{}), gate: make(chan struct{})}
	c := newTestController(t, t.TempDir(), r, time.Now)
	defer closeController(t, c)
	api, _, grantID := pairedFixture(t, c)
	done := make(chan error, 1)
	go func() { // Pause an authenticated scoped request after its initial authorization succeeds.
		var receipt artifact.Result
		done <- api.Request(context.Background(), "POST", "/v1/artifacts/pull", map[string]string{"image": "ghcr.io/team/private:latest"}, &receipt)
	}()
	select { // Revoke only after the download has actually started.
	case <-r.entered: // The controller has released its state lock for slow work.
	case <-time.After(3 * time.Second): // A broken request cannot silently hang the test.
		t.Fatal("preparation did not start")
	}
	c.mu.Lock()
	c.state.Authorizations[grantID].Revoked = true
	c.mu.Unlock()
	close(r.gate)
	err := <-done
	var failure *model.Error
	if !errors.As(err, &failure) || failure.Code != "UNAUTHORIZED" { // Completed bytes cannot override a revoked credential.
		t.Fatal("revoked request received a prepared image", err)
	}
	entries, err := os.ReadDir(filepath.Join(c.opts.StateDir, "artifacts"))
	if err != nil || len(entries) != 0 { // Neither a committed receipt nor private temporary download files may remain.
		t.Fatal("revoked preparation published content", entries, err)
	}
}
