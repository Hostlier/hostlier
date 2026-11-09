package controller

import (
	"context"
	"hostlier/internal/artifact"
	"hostlier/internal/client"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// scopedArtifactFixture creates a real image archive without a registry or executable payload.
func scopedArtifactFixture(t *testing.T) (artifact.Result, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image.tar")
	tag, err := name.NewTag("hostlier.local/artifacts:test")
	// Require a valid image tag before constructing the transport fixture.
	if err != nil {
		t.Fatal(err)
	}
	// Serialize actual Docker archive bytes for the controller's verifier.
	if err = tarball.WriteToFile(path, tag, empty.Image); err != nil {
		t.Fatal(err)
	}
	digest, err := empty.Image.Digest()
	// The manifest digest is checked independently from the archive checksum.
	if err != nil {
		t.Fatal(err)
	}
	config, err := empty.Image.ConfigName()
	// Execution configuration identity must also match at commit time.
	if err != nil {
		t.Fatal(err)
	}
	checksum, size, err := artifact.FileDigest(path)
	// Bind the receipt to the complete serialized archive.
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	// Upload only the bytes whose checksum was recorded above.
	if err != nil {
		t.Fatal(err)
	}
	return artifact.Result{Image: "hostlier.local/artifacts@" + digest.String(), ConfigDigest: config.String(), ArchiveDigest: checksum, Size: size}, data
}

// TestScopedArtifactUpload exercises signed transport, validated publication and retained scope boundaries.
func TestScopedArtifactUpload(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	api, _, grantID := pairedFixture(t, c)
	receipt, data := scopedArtifactFixture(t)
	ctx := context.Background()
	path := "/v1/artifacts/" + receipt.ArchiveDigest + "/chunks"
	var result any
	other, _, _ := pairedFixture(t, c)
	// A second authenticated client cannot poison this device's claimed archive digest.
	if err := other.Request(ctx, "POST", path, artifactChunkRequest{Data: []byte("invalid")}, &result); err != nil {
		t.Fatal("second client staging", err)
	}
	status, _ := humanHTTP(t, c, false, "POST", path, artifactChunkRequest{Data: data}, "", "")
	// Artifact staging remains unavailable without authentication.
	if status == 200 {
		t.Fatal("unauthenticated upload accepted")
	}
	// A valid signed scoped device may stage content without deployment approval.
	if err := api.Request(ctx, "POST", path, artifactChunkRequest{Data: data}, &result); err != nil {
		t.Fatal("scoped upload", err)
	}
	// Lost chunk acknowledgements are recoverable through exact replay.
	if err := api.Request(ctx, "POST", path, artifactChunkRequest{Data: data}, &result); err != nil {
		t.Fatal("chunk retry", err)
	}
	bad := receipt
	bad.ConfigDigest = "sha256:" + strings.Repeat("a", 64)
	// Permission to upload never replaces independent content verification.
	if err := api.Request(ctx, "POST", "/v1/artifacts/commit", bad, &result); err == nil {
		t.Fatal("unverified image committed")
	}
	// Repeating commit must survive promotion from private staging to shared content.
	for range 2 {
		// Each successful response refers to the same verified immutable image.
		if err := api.Request(ctx, "POST", "/v1/artifacts/commit", receipt, &result); err != nil {
			t.Fatal("scoped commit/retry", err)
		}
	}
	store := artifact.Store{Root: filepath.Join(c.opts.StateDir, "artifacts")}
	// Ordinary runtime import sees the committed archive without client-specific paths.
	if _, err := store.Inspect(receipt.Image); err != nil {
		t.Fatal("runtime cannot inspect uploaded image", err)
	}
	// The second client's invalid staging cannot be committed over the verified archive.
	if err := other.Request(ctx, "POST", "/v1/artifacts/commit", receipt, &result); err == nil {
		t.Fatal("second client committed invalid bytes")
	}
	assertArtifactScopeBounds(t, c, api, grantID, receipt, data)
}

// assertArtifactScopeBounds keeps upload authority separate from owner administration and grant lifetime.
func assertArtifactScopeBounds(t *testing.T, c *Controller, api *client.Client, grantID string, receipt artifact.Result, data []byte) {
	t.Helper()
	ctx := context.Background()
	path := "/v1/artifacts/" + receipt.ArchiveDigest + "/chunks"
	var result any
	// Upload permission grants no archive reads, deletions or unrelated API routes.
	for _, action := range [][2]string{{"GET", path}, {"DELETE", "/v1/artifacts/" + receipt.ArchiveDigest}, {"POST", "/v1/artifacts/commit/extra"}, {"GET", "/v1/secrets"}, {"GET", "/v1/apps/other"}} {
		// These routes must remain inaccessible after successful artifact publication.
		if err := api.Request(ctx, action[0], action[1], nil, &result); err == nil {
			t.Fatal("scope leak", action)
		}
	}
	// Preparation does not reserve an execution approval.
	if c.state.Authorizations[grantID].Uses != 0 {
		t.Fatal("staging consumed deployment allowance")
	}
	c.state.Authorizations[grantID].Revoked = true
	// Revocation prevents further chunk writes even for an already committed digest.
	if err := api.Request(ctx, "POST", path, artifactChunkRequest{Data: data}, &result); err == nil {
		t.Fatal("revoked client uploaded")
	}
	// Commit retries also require a currently live grant.
	if err := api.Request(ctx, "POST", "/v1/artifacts/commit", receipt, &result); err == nil {
		t.Fatal("revoked client committed")
	}
	c.state.Authorizations[grantID].Revoked = false
	c.state.Authorizations[grantID].ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	// Expiry independently prevents writes even if the credential has not been revoked.
	if err := api.Request(ctx, "POST", path, artifactChunkRequest{Data: data}, &result); err == nil {
		t.Fatal("expired client uploaded")
	}
}
