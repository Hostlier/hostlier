package packageexec

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestPackageUploadsBindActorDigestAndReplay tests the immutable prefix and authenticated ownership boundaries.
func TestPackageUploadsBindActorDigestAndReplay(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	// Protected staging rejects alias ancestors, so canonicalize the test directory.
	if err != nil {
		t.Fatal(err)
	}
	store := Uploads{Root: filepath.Join(root, "uploads")}
	data := []byte("an executable fixture split across requests")
	digest := Digest(data)
	// A valid first chunk is durable and replayable without appending twice.
	for i := 0; i < 2; i++ {
		// Replaying a retained prefix must not advance its cursor.
		if offset, err := store.Chunk("alice", digest, 0, data[:10]); err != nil || offset != 10 {
			t.Fatal(offset, err)
		}
	}
	// Changed replays cannot replace the retained prefix.
	if _, err = store.Chunk("alice", digest, 0, bytes.Repeat([]byte("x"), 10)); err == nil {
		t.Fatal("changed replay accepted")
	}
	// Gaps cannot grow sparse files outside the bounded upload.
	if _, err = store.Chunk("alice", digest, 11, data[10:]); err == nil {
		t.Fatal("offset gap accepted")
	}
	// The exact remaining suffix completes the upload.
	if _, err = store.Chunk("alice", digest, 10, data[10:]); err != nil {
		t.Fatal(err)
	}
	checkUploadOwnership(t, store, data)
}

// checkUploadOwnership verifies actor separation, digest revalidation and nonexecutable staging permissions.
func checkUploadOwnership(t *testing.T, store Uploads, data []byte) {
	t.Helper()
	request := InstallRequest{Name: "postgres", Digest: Digest(data), Size: int64(len(data))}
	// A second credential cannot adopt an uploaded binary merely by knowing its digest.
	if _, err := store.Read("bob", request); err == nil {
		t.Fatal("cross-actor upload accepted")
	}
	received, err := store.Read("alice", request)
	// The original uploader must receive exactly the retained bytes.
	if err != nil || !bytes.Equal(received, data) {
		t.Fatal(err)
	}
	path, _ := store.path("alice", request.Digest)
	info, err := os.Stat(path)
	// Uploaded bytes must have no executable or group/world permission bits.
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	// Disk corruption after preview must invalidate activation even when size is unchanged.
	if err = os.WriteFile(path, bytes.Repeat([]byte("x"), len(data)), 0600); err != nil {
		t.Fatal(err)
	}
	// Rehashing is mandatory when reusing an earlier approved identity.
	if _, err = store.Read("alice", request); err == nil {
		t.Fatal("corrupt upload accepted")
	}
}

// TestVerifiedInstallationNeverReplacesOnDigestMismatch protects an already installed package from altered transfers.
func TestVerifiedInstallationNeverReplacesOnDigestMismatch(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	// Canonical roots preserve production ancestry checks.
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Directory: filepath.Join(root, "packages")}
	data := []byte("original package")
	request := InstallRequest{Name: "postgres", Digest: Digest(data), Size: int64(len(data))}
	// Initial activation establishes the previously working executable.
	if _, err = runner.InstallVerified(request, data); err != nil {
		t.Fatal(err)
	}
	// A different payload cannot use the original approval fingerprint.
	if _, err = runner.InstallVerified(request, []byte("changed package!")); err == nil {
		t.Fatal("changed bytes installed")
	}
	actual, err := os.ReadFile(filepath.Join(runner.Directory, "postgres"))
	// Failed verification must preserve the old executable atomically.
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal(string(actual), err)
	}
}

// TestPackageUploadQuotaPermitsContinuationButRejectsNewFiles checks quota boundaries without large allocations.
func TestPackageUploadQuotaPermitsContinuationButRejectsNewFiles(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	// Canonical private staging retains the same ancestry rules as production.
	if err != nil {
		t.Fatal(err)
	}
	store := Uploads{Root: root}
	current := ""
	// Fill the file-count quota using tiny inert uploads.
	for i := 0; i < uploadCount; i++ {
		digest := Digest([]byte(fmt.Sprint(i)))
		// Every slot up to the configured bound should be usable.
		if _, err = store.Chunk("actor", digest, 0, []byte("a")); err != nil {
			t.Fatal(i, err)
		}
		current = digest
	}
	// Existing files may continue at the quota without consuming another slot.
	if _, err = store.Chunk("actor", current, 1, []byte("b")); err != nil {
		t.Fatal(err)
	}
	// Exact retries must not reserve another file or count retained bytes twice.
	if _, err = store.Chunk("actor", current, 0, []byte("a")); err != nil {
		t.Fatal(err)
	}
	// A different content identity may not bypass the total file count.
	if _, err = store.Chunk("actor", Digest([]byte("extra")), 0, []byte("a")); err == nil {
		t.Fatal("file quota bypass")
	}
}
