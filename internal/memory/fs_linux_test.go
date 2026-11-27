//go:build linux

package memory

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryNestedMount(t *testing.T) {
	r, source, _ := memoryFixture(t)
	nested := filepath.Join(source, "mount")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", nested, "tmpfs", 0, "size=1m"); err != nil {
		t.Skipf("requires isolated CAP_SYS_ADMIN test container: %v", err)
	}
	defer unix.Unmount(nested, 0)
	if err := os.WriteFile(filepath.Join(nested, "secret"), []byte("nested"), 0600); err != nil {
		t.Fatal(err)
	}
	if f, e := r.OpenFile(context.Background(), "mount/secret", os.O_RDONLY, 0); e == nil {
		f.Close()
		t.Fatal("nested mount escaped")
	}
	// A selected mounted drive itself is valid; only its nested mounts are excluded.
	drive, err := OpenRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	defer drive.Close()
	if err = unix.Unmount(nested, unix.MNT_DETACH); err != nil {
		t.Fatal(err)
	}
	if drive.Check() == nil {
		t.Fatal("unmounted drive accepted")
	}
}
