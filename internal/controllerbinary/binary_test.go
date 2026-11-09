package controllerbinary

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSiblingRequiresExecutable rejects absent, nonexecutable and directory artifacts.
func TestSiblingRequiresExecutable(t *testing.T) {
	directory := t.TempDir()
	cli := filepath.Join(directory, "hostlier")
	// A standalone laptop CLI must report the missing server clearly.
	if _, err := sibling(cli); err == nil {
		t.Fatal("missing controller accepted")
	}
	controller := filepath.Join(directory, "hostlier-controller")
	// A nonexecutable file must not pass release validation.
	if err := os.WriteFile(controller, []byte("server"), 0600); err != nil {
		t.Fatal(err)
	}
	// This failure must occur before init applies host mutations.
	if _, err := sibling(cli); err == nil {
		t.Fatal("nonexecutable controller accepted")
	}
	// The same artifact becomes valid only with execution permission.
	if err := os.Chmod(controller, 0755); err != nil {
		t.Fatal(err)
	}
	actual, err := sibling(cli)
	// Sibling lookup must return the controller, never the client.
	if err != nil || actual != controller {
		t.Fatal(actual, err)
	}
}

// TestTargetSuffixSelectsMatchingController keeps native and Linux builds independent in dist.
func TestTargetSuffixSelectsMatchingController(t *testing.T) {
	directory := t.TempDir()
	controller := filepath.Join(directory, "hostlier-controller-linux-arm64")
	// A cross-compiled CLI must select the matching target artifact.
	if err := os.WriteFile(controller, []byte("server"), 0755); err != nil {
		t.Fatal(err)
	}
	actual, err := sibling(filepath.Join(directory, "hostlier-linux-arm64"))
	// Accidentally choosing a native sibling would fail only after deployment.
	if err != nil || actual != controller {
		t.Fatal(actual, err)
	}
}
