package collectorimage

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fixtureLayout creates a two-platform immutable layout from harmless synthetic contents.
func fixtureLayout(t *testing.T) (string, *Image) {
	t.Helper()
	base := t.TempDir()
	binary := filepath.Join(base, "binary")
	ca := filepath.Join(base, "ca")
	// Files contain no credentials or executable test processes.
	for path, contents := range map[string]string{binary: strings.Repeat("fixture binary", 100), ca: "fixture public CA"} {
		// Setup failures invalidate the test rather than producing incomplete images.
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(base, "image")
	inputs := []PlatformInput{{Architecture: "arm64", BinaryPath: binary, CAPath: ca}, {Architecture: "amd64", BinaryPath: binary, CAPath: ca}}
	// Both architectures must package successfully before checking registry behavior.
	if err := Build(output, inputs); err != nil {
		t.Fatal(err)
	}
	image, err := Open(output)
	// The public opener validates the exact packaged output.
	if err != nil {
		t.Fatal(err)
	}
	// Release all retained blob descriptors after this test.
	t.Cleanup(func() { image.Close() })
	return output, image
}

// TestDeterministicPlatforms verifies stable content identity independent of output directory.
func TestDeterministicPlatforms(t *testing.T) {
	_, one := fixtureLayout(t)
	_, two := fixtureLayout(t)
	// Metadata and gzip/tar content must not contain host paths or timestamps.
	if one.Digest() != two.Digest() {
		t.Fatal("non-deterministic OCI index")
	}
	// A nonstandard HTTPS port is preserved in Kubernetes image references.
	if one.Reference("controller.example:7443") != "controller.example:7443/hostlier/collector@"+one.Digest() {
		t.Fatal("invalid registry reference")
	}
	// The installer publishes index metadata after all referenced immutable objects.
	if files := one.Files(); files[len(files)-1] != "index.json" {
		t.Fatal("index is not last")
	}
	// Return copies so callers cannot mutate the registry platform inventory.
	platforms := one.Platforms()
	platforms[0].Architecture = "broken"
	// The original inventory must remain unchanged after caller mutation.
	if one.Platforms()[0].Architecture == "broken" {
		t.Fatal("platform inventory exposed")
	}
}

// TestRegistryReads verifies immutable manifests, HEAD, byte ranges and concurrent readers.
func TestRegistryReads(t *testing.T) {
	_, image := fixtureLayout(t)
	server := httptest.NewServer(image)
	defer server.Close()
	response, err := http.Get(server.URL + "/v2/hostlier/collector/manifests/" + image.Digest())
	// Standard clients must be able to begin at the index digest.
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	// The delivered bytes and advertised digest must exactly match the release index.
	if !bytes.Equal(body, image.index) || response.Header.Get("Docker-Content-Digest") != image.Digest() {
		t.Fatal("manifest response mismatch")
	}
	checkConcurrentRanges(t, image, server.URL)
}

// checkConcurrentRanges exercises independent seek positions on a shared retained descriptor.
func checkConcurrentRanges(t *testing.T, image *Image, origin string) {
	t.Helper()
	var digest string
	// Pick a non-manifest object suitable for registry blob reads.
	for key := range image.blobs {
		// Platform manifests belong to the other registry endpoint.
		if !image.manifests[key] {
			digest = key
			break
		}
	}
	var wg sync.WaitGroup
	// Concurrent range reads must not interfere through the underlying file offset.
	for n := 0; n < 8; n++ {
		wg.Add(1)
		// Every goroutine reads the same first four bytes independently.
		go func() { defer wg.Done(); checkRange(t, image, origin, digest) }()
	}
	wg.Wait()
}

// checkRange checks one bounded GET and its equivalent HEAD response.
func checkRange(t *testing.T, image *Image, origin, digest string) {
	t.Helper()
	request, _ := http.NewRequest("GET", origin+"/v2/hostlier/collector/blobs/"+digest, nil)
	request.Header.Set("Range", "bytes=0-3")
	response, err := http.DefaultClient.Do(request)
	// Network failures cannot be mistaken for successful empty body comparisons.
	if err != nil {
		t.Error(err)
		return
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	expected := make([]byte, 4)
	_, _ = image.blobs[digest].file.ReadAt(expected, 0)
	// ServeContent must retain range semantics with the advertised digest.
	if response.StatusCode != 206 || !bytes.Equal(body, expected) {
		t.Error("range mismatch")
	}
	request.Method = "HEAD"
	response, err = http.DefaultClient.Do(request)
	// HEAD must retain response metadata without downloading bytes.
	if err != nil {
		t.Error(err)
		return
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	// A HEAD range retains its status but transfers no content.
	if len(body) != 0 || response.StatusCode != 206 {
		t.Error("HEAD range mismatch")
	}
}

// TestRegistryRejectsMutationsAndTraversal rejects all writable and arbitrary-file surfaces.
func TestRegistryRejectsMutationsAndTraversal(t *testing.T) {
	_, image := fixtureLayout(t)
	// Routes outside the fixed digest-based pull protocol are unsupported.
	for _, test := range []struct {
		method, path string
		status       int
	}{{"PUT", "/v2/hostlier/collector/manifests/latest", 405}, {"GET", "/v2/_catalog", 404}, {"GET", "/v2/hostlier/collector/tags/list", 404}, {"GET", "/v2/hostlier/collector/blobs/../../index.json", 404}, {"GET", "/v2/", 200}} {
		recorder := httptest.NewRecorder()
		image.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		// Verify the explicit protocol error instead of relying on a proxy's path normalization.
		if recorder.Code != test.status {
			t.Fatalf("%s %s: %d", test.method, test.path, recorder.Code)
		}
	}
}

// TestCorruptAndSymlinkedBlobRejected ensures OCI metadata cannot authorize redirected bytes.
func TestCorruptAndSymlinkedBlobRejected(t *testing.T) {
	directory, image := fixtureLayout(t)
	file := filepath.Join(directory, image.Files()[0])
	// Altering even one referenced byte invalidates the complete layout.
	if err := os.WriteFile(file, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	// Reopening must reject the modified layout before exposing a handler.
	if opened, err := Open(directory); err == nil {
		opened.Close()
		t.Fatal("accepted corrupt blob")
	}
	// A matching path must still be rejected when it redirects outside the image directory.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	// Replace the object with a redirect to otherwise valid metadata.
	if err := os.Symlink(filepath.Join(directory, "index.json"), file); err != nil {
		t.Fatal(err)
	}
	// Reopening must reject the modified layout before exposing a handler.
	if opened, err := Open(directory); err == nil {
		opened.Close()
		t.Fatal("accepted symlinked blob")
	}
}
