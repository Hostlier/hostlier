package installer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hostlier/internal/collectorimage"
)

// collectorFixture creates a small deterministic release without requiring a production executable.
func collectorFixture(t *testing.T, version string) (string, string) {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "collector")
	ca := filepath.Join(root, "ca.pem")
	// The packaging boundary trusts reviewed release files, independently of their payload size.
	for path, body := range map[string]string{binary: "collector-fixture-" + version, ca: "public-ca-fixture"} {
		// Fixture file failures must not masquerade as installer behavior.
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "collector-image")
	// Generate the same OCI structure used by actual release packaging.
	if err := collectorimage.Build(source, []collectorimage.PlatformInput{{Architecture: "arm64", BinaryPath: binary, CAPath: ca}}); err != nil {
		t.Fatal(err)
	}
	image, err := collectorimage.Open(source)
	// A fixture must be fully validated before deriving its approved digest.
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	return source, image.Digest()
}

// TestCollectorInstallRetryAndUpgrade verifies immutable publication and preservation of old pull data.
func TestCollectorInstallRetryAndUpgrade(t *testing.T) {
	source, digest := collectorFixture(t, "one")
	target := filepath.Join(t.TempDir(), "collector-image")
	// Installing the same reviewed image twice must be a safe retry.
	for attempt := 0; attempt < 2; attempt++ {
		// No retry should require deleting existing runtime assets.
		if err := installCollectorImage(source, target, digest); err != nil {
			t.Fatal(err)
		}
	}
	original, err := collectorimage.Open(target)
	// Capture the old serving image to model an in-progress registry download.
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	source, next := collectorFixture(t, "two")
	// A new reviewed image replaces the index while retaining immutable referenced content.
	if err = installCollectorImage(source, target, next); err != nil {
		t.Fatal(err)
	}
	current, err := collectorimage.Open(target)
	// The new index must point at exactly the reviewed release.
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	// A version change must be observable to the controller reconciler.
	if current.Digest() != next || next == digest {
		t.Fatal("upgrade index not published")
	}
	assertCollectorFiles(t, target, original.Files())
}

// assertCollectorFiles checks preserved pull content and private installed metadata.
func assertCollectorFiles(t *testing.T, target string, files []string) {
	t.Helper()
	// In-flight requests for old content must retain their immutable blobs.
	for _, name := range files {
		// Index metadata changes while blob filenames remain addressable.
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Fatal("old blob lost", err)
		}
	}
	info, err := os.Stat(filepath.Join(target, "index.json"))
	// Installed release metadata remains private to the controller administrator.
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("image index permissions", err)
	}
}

// TestCollectorInstallRejectsChangedApproval prevents activating a different release after consent.
func TestCollectorInstallRejectsChangedApproval(t *testing.T) {
	source, _ := collectorFixture(t, "one")
	target := filepath.Join(t.TempDir(), "collector-image")
	// Invalid digests must fail before any visible destination index is created.
	if err := installCollectorImage(source, target, "sha256:changed"); err == nil {
		t.Fatal("changed image accepted")
	}
	// A rejected install must not leave an apparently usable release.
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("rejected image published", err)
	}
}

// TestCollectorInstallRefusesRedirectedDestination protects unrelated folders from automatic setup.
func TestCollectorInstallRefusesRedirectedDestination(t *testing.T) {
	source, digest := collectorFixture(t, "one")
	foreign := t.TempDir()
	target := filepath.Join(t.TempDir(), "collector-image")
	// Simulate a conflicting administrator-created destination.
	if err := os.Symlink(foreign, target); err != nil {
		t.Fatal(err)
	}
	// The installer must refuse to adopt a redirected registry directory.
	if err := installCollectorImage(source, target, digest); err == nil {
		t.Fatal("symlink target accepted")
	}
	entries, err := os.ReadDir(foreign)
	// A collision cannot write any files into the unrelated destination.
	if err != nil || len(entries) != 0 {
		t.Fatal("foreign destination modified", err)
	}
}

// TestCollectorInspectionBindsImage validates that approval covers executable image content.
func TestCollectorInspectionBindsImage(t *testing.T) {
	source, digest := collectorFixture(t, "review")
	p := Plan{Options: Options{CollectorSource: source}}
	inspectCollector(&p)
	// The plan must carry the immutable digest used by apply.
	if len(p.Errors) != 0 || p.Snapshot.CollectorDigest != digest {
		t.Fatal("image missing from plan", p.Errors)
	}
	p.CollectorSource = filepath.Join(t.TempDir(), "missing")
	inspectCollector(&p)
	// Missing release assets make automatic-install promises fail during inspection.
	if len(p.Errors) == 0 || !strings.Contains(p.Errors[len(p.Errors)-1], filepath.Join(p.CollectorSource, "oci-layout")) {
		t.Fatal("missing release did not identify the failed file", p.Errors)
	}
}

// TestCollectorReadinessRequiresManagedReady distinguishes HTTP availability from provisioning success.
func TestCollectorReadinessRequiresManagedReady(t *testing.T) {
	// Both absent and explicitly degraded collection must prevent a successful installation receipt.
	for _, body := range []string{`{}`, `{"telemetry":{"managed":{"state":"degraded"}}}`, `{"telemetry":{"managed":{"state":"ready"}}}`} {
		// The real HTTP path validates bounded decoding and lifecycle status together.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := awaitCollector(ctx, server.Client(), server.URL, time.Millisecond)
		cancel()
		server.Close()
		wantReady := body == `{"telemetry":{"managed":{"state":"ready"}}}`
		// Only the explicit ready state can complete initialization.
		if (err == nil) != wantReady {
			t.Fatalf("body %s: %v", body, err)
		}
	}
}

type collectorStatusTransport func(*http.Request) (*http.Response, error)

func (f collectorStatusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// TestCollectorReadinessReportsManagedReason preserves the bounded controller health reason on timeout.
func TestCollectorReadinessReportsManagedReason(t *testing.T) {
	client := &http.Client{Transport: collectorStatusTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"telemetry":{"managed":{"state":"degraded","reason":"Collector process is not ready"}}}`))}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := awaitCollector(ctx, client, "http://local/v1/status/snapshot", time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "managed collector degraded: Collector process is not ready") {
		t.Fatal(err)
	}
}
