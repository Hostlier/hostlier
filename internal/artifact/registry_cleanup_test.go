package artifact

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRegistryPreparationCleanup reclaims stale server-generated trees while preserving recent and unrelated data.
func TestRegistryPreparationCleanup(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-2 * CleanupGrace)
	seedRegistryPreparationCleanup(t, root, old)
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "registry-pull-789")); err != nil { // A recognized-looking symlink must never broaden deletion authority.
		t.Fatal(err)
	}
	result, err := (Store{Root: root}).SweepPreparations(time.Now())
	if err != nil || result.Files != 1 { // Exactly one abandoned directory was eligible.
		t.Fatal(result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "registry-pull-123")); !os.IsNotExist(err) { // Confirm stale content was actually removed.
		t.Fatal("stale preparation remained", err)
	}
	for _, path := range []string{filepath.Join(root, "registry-pull-456"), filepath.Join(root, "unrelated-directory"), target} { // Recent, unknown and symlink-target data must all survive.
		if _, err := os.Stat(path); err != nil {
			t.Fatal("unrelated preparation data removed", err)
		}
	}
}

// seedRegistryPreparationCleanup creates stale, recent and unrelated directory leases.
func seedRegistryPreparationCleanup(t *testing.T, root string, old time.Time) {
	t.Helper()
	// Only an expired recognized directory may be deleted by a later sweep.
	for _, fixture := range []struct {
		name  string
		stale bool
	}{{"registry-pull-123", true}, {"registry-pull-456", false}, {"unrelated-directory", true}} { // Only an expired recognized preparation may be deleted.
		path := filepath.Join(root, fixture.name)
		if err := os.Mkdir(path, 0700); err != nil { // Every fixture is isolated inside a private temporary artifact store.
			t.Fatal(err)
		}
		if fixture.stale { // Age only the cases intended to exercise expiry.
			if err := os.Chtimes(path, old, old); err != nil { // Filesystem timestamps must support the real cleanup lease.
				t.Fatal(err)
			}
		}
	}
}
