//go:build !linux && !darwin

package memory

import "testing"

func TestMemoryUnsupportedServerFailsClosed(t *testing.T) {
	if root, err := OpenRoot(t.TempDir()); err == nil || root != nil {
		t.Fatal("unsupported server opened an export")
	}
}
