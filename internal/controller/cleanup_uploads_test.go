package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStartupCleanupWithoutImageEvictionSupport reclaims staging even with an external runtime.
func TestStartupCleanupWithoutImageEvictionSupport(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	// Package upload cleanup requires the same canonical ancestry as installation.
	if err != nil {
		t.Fatal(err)
	}
	files := []string{
		filepath.Join(root, "artifacts", strings.Repeat("a", 64)+".tar"),
		filepath.Join(root, "package-uploads", strings.Repeat("b", 64)+"-"+strings.Repeat("c", 64)),
	}
	// Both initial startup and a later restart must reclaim abandoned uploads.
	for attempt := 0; attempt < 2; attempt++ {
		seedExpiredControllerUploads(t, files)
		runUploadCleanupController(t, root, files)
	}
}

// seedExpiredControllerUploads simulates interruption before either kind of upload completes.
func seedExpiredControllerUploads(t *testing.T, files []string) {
	t.Helper()
	old := time.Now().Add(-48 * time.Hour)
	// Only fixtures under a fresh private state directory are modified.
	for _, name := range files {
		// Create only private fixture storage.
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		// Simulate persisted bytes from the interrupted transfer.
		if err := os.WriteFile(name, []byte("abandoned"), 0600); err != nil {
			t.Fatal(err)
		}
		// Make the interruption older than both upload grace periods.
		if err := os.Chtimes(name, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// waitForUploadCleanup observes startup reclamation with a bounded deadline.
func waitForUploadCleanup(t *testing.T, files []string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	// Confirm every expired upload is gone before restarting the controller.
	for time.Now().Before(deadline) {
		// Return only after both stores have reclaimed their stale transfers.
		if controllerUploadsAbsent(files) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("startup cleanup did not reclaim abandoned uploads")
}

// controllerUploadsAbsent distinguishes successful reclamation from unrelated filesystem failures.
func controllerUploadsAbsent(files []string) bool {
	// Every expired fixture must be confirmed absent.
	for _, name := range files {
		// Require confirmed absence rather than treating read failures as deletion.
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// runUploadCleanupController releases all workers and state before the next restart.
func runUploadCleanupController(t *testing.T, root string, files []string) {
	t.Helper()
	c := newTestController(t, root, &recordingRuntime{}, time.Now)
	defer c.Close(context.Background())
	waitForUploadCleanup(t, files)
}
