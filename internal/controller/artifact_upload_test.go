package controller

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"hostlier/internal/client"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hostlier/internal/artifact"
)

// TestArtifactLargeChunkHTTP exercises real JSON/base64 decoding under the ordinary API body limit.
func TestArtifactLargeChunkHTTP(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	id := strings.Repeat("a", 64)
	path := "/v1/artifacts/" + id + "/chunks"
	chunk := artifactChunkRequest{Data: bytes.Repeat([]byte("x"), artifact.ChunkBytes)}
	status, result := humanHTTP(t, c, true, "POST", path, chunk, "", "")
	// Base64 overhead must still leave one MiB of raw bytes within the server's request limit.
	if status != 200 || result["offset"] != float64(artifact.ChunkBytes) {
		t.Fatal(status, result)
	}
	api, _, _ := pairedFixture(t, c)
	var signedResult any
	// Signed-device requests must also fit the negotiated transport limit.
	if err := api.Request(context.Background(), "POST", path, chunk, &signedResult); err != nil {
		t.Fatal(err)
	}
	chunk.Offset, chunk.Data = artifact.ChunkBytes, bytes.Repeat([]byte("x"), artifact.ChunkBytes+1)
	status, result = humanHTTP(t, c, true, "POST", path, chunk, "", "")
	// Negotiated limits remain hard allocation and disk boundaries.
	if status == 200 {
		t.Fatal("oversized chunk accepted", result)
	}
}

// TestDeviceBodyLimit reserves the larger signed-body allowance for exact payload routes.
func TestDeviceBodyLimit(t *testing.T) {
	chunkPath := "/v1/artifacts/" + strings.Repeat("a", 64) + "/chunks"
	cases := []struct {
		method, path string
		size         int
		accepted     bool
	}{
		{"POST", chunkPath, maxRequestBytes, true},
		{"POST", "/v1/hosts/worker/proxy" + chunkPath[3:], maxRequestBytes, true},
		{"POST", "/v1/hosts/worker/proxy" + chunkPath[3:], maxRequestBytes + 1, false},
		{"POST", "/v1/hosts/worker/proxy" + chunkPath[3:] + "/extra", 131073, false},
		{"POST", chunkPath, maxRequestBytes + 1, false},
		{"POST", "/v1/plans", maxRequestBytes, true},
		{"POST", "/v1/plans", maxRequestBytes + 1, false},
		{"POST", "/v1/planning", maxRequestBytes, true},
		{"POST", "/v1/hosts/worker/proxy/plans", maxRequestBytes, true},
		{"GET", "/v1/plans", 131073, false},
		{"POST", "/v1/plans/id/apply", 131073, false},
		{"GET", chunkPath, 131073, false},
		{"POST", "/v1/artifacts/status", 131073, false},
		{"POST", "/v1/artifacts/commit", 131073, false},
		{"POST", "/v1/approvals", 131073, false},
		{"POST", chunkPath + "/extra", 131073, false},
	}
	// Each case checks the reader used before signed-request verification.
	for _, test := range cases {
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(strings.Repeat("x", test.size)))
		_, err := deviceRequestBody(r)
		// Unrelated routes and oversized payloads retain their hard bounds.
		if (err == nil) != test.accepted {
			t.Fatalf("%s %s size=%d: %v", test.method, test.path, test.size, err)
		}
	}
}

// TestAPIBodyLimit applies the remote payload allowance to the corresponding HTTP and RPC routes.
func TestAPIBodyLimit(t *testing.T) {
	chunkPath := "/v1/artifacts/" + strings.Repeat("a", 64) + "/chunks"
	for _, test := range []struct {
		method, path string
		local        bool
		want         int64
	}{
		{"POST", chunkPath, false, maxRequestBytes},
		{"POST", "/v1/hosts/worker/proxy" + chunkPath[3:], false, maxRequestBytes},
		{"POST", "/v1/hosts/worker/proxy" + chunkPath[3:] + "/extra", false, 1 << 20},
		{"POST", "/v1/plans", false, maxRequestBytes},
		{"POST", "/v1/planning", false, maxRequestBytes},
		{"POST", "/v1/hosts/worker/proxy/plans", false, maxRequestBytes},
		{"POST", "/v1/hosts/worker/proxy/planning", false, maxRequestBytes},
		{"POST", "/v1/hosts/worker/proxy/plans/id/apply", false, 1 << 20},
		{"GET", "/v1/plans", false, 1 << 20},
		{"POST", "/v1/artifacts/status", false, 1 << 20},
		{"POST", "/v1/artifacts/commit", false, 1 << 20},
		{"POST", "/v1/apps", false, 1 << 20},
		{"GET", chunkPath, false, 1 << 20},
		{"POST", chunkPath + "/extra", false, 1 << 20},
		{"POST", "/v1/artifacts/" + strings.Repeat("z", 64) + "/chunks", false, 1 << 20},
		{"POST", "/v1/apps", true, maxRequestBytes},
	} {
		r := httptest.NewRequest(test.method, test.path, nil)
		if got := apiBodyLimit(r, test.local); got != test.want {
			t.Fatalf("%s %s local=%t: got %d, want %d", test.method, test.path, test.local, got, test.want)
		}
	}
}

// TestScopedArtifactResumeStatus checks signed status discovery, private prefixes and grant lifetime.
func TestScopedArtifactResumeStatus(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, _, grantID := pairedFixture(t, c)
	other, _, _ := pairedFixture(t, c)
	receipt, data := scopedArtifactFixture(t)
	prefix := data[:len(data)/2]
	var result any
	ctx := context.Background()
	path := "/v1/artifacts/" + receipt.ArchiveDigest + "/chunks"
	// A scoped device may stage a prefix without activating the workload.
	if err := api.Request(ctx, "POST", path, artifactChunkRequest{Data: prefix}, &result); err != nil {
		t.Fatal(err)
	}
	var status artifact.UploadStatus
	// The signed status response must describe only this device's retained prefix.
	if err := api.Request(ctx, "POST", "/v1/artifacts/status", receipt, &status); err != nil {
		t.Fatal(err)
	}
	digest, _ := artifact.PrefixDigest(bytes.NewReader(prefix), int64(len(prefix)))
	// The prefix is independently checkable by the uploading CLI.
	if status.Offset != int64(len(prefix)) || status.PrefixDigest != digest || status.Committed {
		t.Fatal(status)
	}
	// A different live device cannot inspect the first device's staging progress.
	if err := other.Request(ctx, "POST", "/v1/artifacts/status", receipt, &status); err != nil {
		t.Fatal(err)
	}
	assertEmptyUploadStatus(t, status)
	// Revocation must serialize with the controller's background state snapshots.
	c.mu.Lock()
	c.state.Authorizations[grantID].Revoked = true
	c.mu.Unlock()
	// Reusing content and resuming uploads both require current authorization.
	if err := api.Request(ctx, "POST", "/v1/artifacts/status", receipt, &status); err == nil {
		t.Fatal("revoked device obtained status")
	}
}

// assertEmptyUploadStatus ensures another device learns no private staging progress.
func assertEmptyUploadStatus(t *testing.T, status artifact.UploadStatus) {
	t.Helper()
	// Reused response values must clear every private progress field.
	if status.Offset != 0 || status.Committed || status.PrefixDigest != "" {
		t.Fatal("private staging leaked", status)
	}
}

// TestGRPCFleetLargeArtifactChunk verifies the negotiated chunk through signed native gRPC and the outbound member session.
func TestGRPCFleetLargeArtifactChunk(t *testing.T) {
	f := newHumanFixture(t)
	enrolledHostFixture(t, f, "worker")
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	login, _, _ := pairWithKey(t, f.c, deploymentScope{}, public, private)
	api, _, _ := grpcFixture(t, f.c, client.EncodeDeviceCredential(login.Token, private))
	path := "/v1/hosts/worker/proxy/artifacts/" + strings.Repeat("a", 64) + "/chunks"
	var result struct {
		Offset int64 `json:"offset"`
	}
	chunk := artifactChunkRequest{Data: bytes.Repeat([]byte("x"), artifact.ChunkBytes)}
	if err := api.Request(context.Background(), "POST", path, chunk, &result); err != nil {
		t.Fatal(err)
	}
	if result.Offset != artifact.ChunkBytes {
		t.Fatal("incomplete forwarded chunk", result)
	}
	chunk.Offset = result.Offset
	chunk.Data = append(chunk.Data, 'x')
	if err := api.Request(context.Background(), "POST", path, chunk, &result); err == nil {
		t.Fatal("oversized forwarded chunk accepted")
	}
}
