//go:build darwin

package memory

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"hostlier/internal/model"
)

func TestMemoryMacStateAliases(t *testing.T) {
	base := memoryTempDir(t)
	state := filepath.Join(base, "State-é")
	if err := os.MkdirAll(filepath.Join(state, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := New([]string{base}, state)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, alias := range []string{"state-é", "State-e\u0301"} {
		p := filepath.Join(base, alias)
		if _, err := os.Stat(p); err != nil {
			continue // A case-sensitive filesystem need not provide both aliases.
		}
		for _, source := range []string{p, filepath.Join(p, "private")} {
			project := model.Project{Version: 3, Kind: "memory", Name: "library", Exports: map[string]model.MemoryExport{"files": {Path: source, Protocols: []string{"webdav"}}}}
			if _, err := r.Prepare(project); err == nil {
				t.Fatalf("exported controller state via %s", source)
			}
		}
	}
	// APFS's Data-volume path aliases are not symlinks and must also be checked
	// by filesystem identity, even when an administrator explicitly allows one.
	alias := "/System/Volumes/Data" + state
	if info, err := os.Stat(alias); err == nil {
		original, _ := os.Stat(state)
		if os.SameFile(info, original) && !filesystemOverlap(alias, state) {
			t.Fatal("firmlink alias bypassed state exclusion")
		}
	}
}

func TestMemoryMacAliasedTransfers(t *testing.T) {
	root, source, _ := memoryFixture(t)
	if err := os.Mkdir(filepath.Join(source, "Folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Folder", "document"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "folder")); err != nil {
		t.Skip("requires a case-insensitive volume")
	}
	locks := newNativeLocks()
	for _, transfer := range []struct {
		method, src, dst string
		status           int
	}{
		{"COPY", "Folder", "folder", 403},
		{"COPY", "Folder", "folder/child", 403},
		{"MOVE", "Folder/document", "folder", 403},
		{"MOVE", "Folder", "folder/child", 403},
		{"MOVE", "Folder", "folder", 204},
	} {
		req := httptest.NewRequest(transfer.method, "http://storage/files/"+transfer.src, nil)
		req.Header.Set("Destination", "http://storage/files/"+transfer.dst)
		w := httptest.NewRecorder()
		serveNativeDAV(w, req, root, locks, "/files")
		if w.Code != transfer.status {
			t.Fatalf("%s %s -> %s: %d %s", transfer.method, transfer.src, transfer.dst, w.Code, w.Body.String())
		}
		data, err := os.ReadFile(filepath.Join(source, "folder", "document"))
		if err != nil || string(data) != "keep" {
			t.Fatalf("transfer destroyed source: %v", err)
		}
	}
}

// Explicit opt-in: this test creates, mounts, ejects and removes a private disk
// image, without requiring or touching any physical drive.
func TestMemoryMacMountedDrive(t *testing.T) {
	if os.Getenv("HOSTLIER_TEST_DISK_IMAGE") != "1" {
		t.Skip("set HOSTLIER_TEST_DISK_IMAGE=1 to test a temporary macOS disk image")
	}
	r, source, base := memoryFixture(t)
	nested := filepath.Join(source, "drive")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(base, "drive.dmg")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("/usr/bin/hdiutil", args...).CombinedOutput(); err != nil {
			t.Fatalf("hdiutil %v: %v: %s", args, err, out)
		}
	}
	run("create", "-size", "32m", "-fs", "HFS+", "-volname", "Hostlier Memory Test", image)
	run("attach", "-nobrowse", "-mountpoint", nested, image)
	mounted := true
	defer func() {
		if mounted {
			run("detach", "-force", nested)
		}
	}()
	if err := os.WriteFile(filepath.Join(nested, "document"), []byte("on drive"), 0600); err != nil {
		t.Fatal(err)
	}
	if f, err := r.OpenFile(context.Background(), "drive/document", os.O_RDONLY, 0); err == nil {
		f.Close()
		t.Fatal("nested filesystem exposed")
	}
	drive, err := OpenRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	defer drive.Close()
	f, err := drive.OpenFile(context.Background(), "document", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	run("detach", nested)
	mounted = false
	if drive.Check() == nil {
		t.Fatal("ejected drive still accepted")
	}
	if f, err := drive.OpenFile(context.Background(), "new", os.O_CREATE|os.O_WRONLY, 0600); err == nil {
		f.Close()
		t.Fatal("wrote to directory underneath ejected drive")
	}
}
