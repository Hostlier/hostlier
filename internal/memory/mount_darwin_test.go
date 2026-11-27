//go:build darwin

package memory

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Exercise Apple's actual filesystem client against the Darwin server backend.
// The anonymous HTTP fixture is loopback-only and contains only temporary data.
func TestMemoryMacNativeMount(t *testing.T) {
	if os.Getenv("HOSTLIER_TEST_NATIVE_MOUNT") != "1" {
		t.Skip("set HOSTLIER_TEST_NATIVE_MOUNT=1 to mount a temporary WebDAV volume")
	}
	root, source, base := memoryFixture(t)
	locks := newNativeLocks()
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		serveNativeDAV(w, req, root, locks, "/files")
	}))
	defer server.Close()
	point := filepath.Join(base, "mounted")
	if err := os.Mkdir(point, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(program string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(ctx, program, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", program, err, output)
		}
	}
	mounted := false
	defer func() {
		if mounted {
			run("/sbin/umount", "-f", point)
		}
	}()
	mount := func() {
		run("/sbin/mount_webdav", "-v", "Hostlier Memory Test", server.URL+"/files/", point)
		mounted = true
	}
	mount()
	dir := filepath.Join(point, "Documents")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	name := "résumé + image.bin"
	data := bytes.Repeat([]byte{0, 255, 17, 42}, 1<<20)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "save.tmp"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "save.tmp"), filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, filepath.Join(point, "Renamed")); err != nil {
		t.Fatal(err)
	}
	run("/sbin/umount", point)
	mounted = false
	onDisk, err := os.ReadFile(filepath.Join(source, "Renamed", name))
	if err != nil || !bytes.Equal(onDisk, data) {
		t.Fatalf("server bytes differ: %v", err)
	}
	mount()
	readBack, err := os.ReadFile(filepath.Join(point, "Renamed", name))
	if err != nil || !bytes.Equal(readBack, data) {
		t.Fatalf("remounted bytes differ: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(point, "Renamed")); err != nil {
		t.Fatal(err)
	}
	run("/sbin/umount", point)
	mounted = false
	if _, err := os.Stat(filepath.Join(source, "Renamed")); !os.IsNotExist(err) {
		t.Fatalf("native deletion did not reach server: %v", err)
	}
}
