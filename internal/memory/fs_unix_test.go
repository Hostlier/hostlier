//go:build linux || darwin

package memory

import (
	"context"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func memoryFixture(t *testing.T) (*Root, string, string) {
	t.Helper()
	base := memoryTempDir(t)
	source := filepath.Join(base, "export")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, source, base
}
func TestMemoryConfinement(t *testing.T) {
	r, source, base := memoryFixture(t)
	ctx := context.Background()
	if err := os.Symlink(base, filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "secret"), filepath.Join(source, "symlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(base, "secret"), filepath.Join(source, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(source, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../secret", "a/../../secret", "link/secret", "symlink", "hardlink", "fifo", "/../secret", "..\\secret"} {
		for _, flags := range []int{os.O_RDONLY, os.O_WRONLY | os.O_TRUNC, os.O_CREATE | os.O_RDWR | os.O_TRUNC} {
			f, err := r.OpenFile(ctx, name, flags, 0600)
			if err == nil {
				f.Close()
				t.Fatalf("opened forbidden %q flags %d", name, flags)
			}
		}
	}
	listing, err := r.OpenFile(ctx, "/", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := listing.Readdir(-1)
	listing.Close()
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsafe listing %v %v", entries, err)
	}
	data, _ := os.ReadFile(filepath.Join(base, "secret"))
	if string(data) != "outside" {
		t.Fatal("outside data changed")
	}
	if err := r.Mkdir(ctx, "link/new", 0700); err == nil {
		t.Fatal("mkdir escaped")
	}
	if err := r.RemoveAll(ctx, "symlink"); err == nil {
		t.Fatal("symlink deletion accepted")
	}
	if err := r.Rename(ctx, "hardlink", "safe"); err == nil {
		t.Fatal("hardlink rename accepted")
	}
}
func TestMemoryExistingFilesAndIdentity(t *testing.T) {
	r, source, _ := memoryFixture(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(source, "existing.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := r.OpenFile(ctx, "existing.txt", os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("updated")); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = r.Mkdir(ctx, "folder", 0700); err != nil {
		t.Fatal(err)
	}
	if err = r.Rename(ctx, "existing.txt", "folder/moved.txt"); err != nil {
		t.Fatal(err)
	}
	f, err = r.OpenFile(ctx, "folder/moved.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "updated" || info.Name() != "moved.txt" {
		t.Fatalf("read %s %s", data, info.Name())
	}
	if err = r.RemoveAll(ctx, "folder"); err != nil {
		t.Fatal(err)
	}
	if err = r.RemoveAll(ctx, "/"); err == nil {
		t.Fatal("removed root")
	}
	if err = os.Rename(source, source+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err = r.Check(); err == nil {
		t.Fatal("replacement accepted")
	}
	if _, err = r.OpenFile(ctx, "new", os.O_CREATE|os.O_WRONLY, 0600); err == nil {
		t.Fatal("write after replacement")
	}
}
func TestMemorySingleFile(t *testing.T) {
	base := memoryTempDir(t)
	source := filepath.Join(base, "document.txt")
	if err := os.WriteFile(source, []byte("document"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "sibling"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	dir, err := r.OpenFile(ctx, "/", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := dir.Readdir(-1)
	dir.Close()
	if err != nil || len(entries) != 1 || entries[0].Name() != "document.txt" {
		t.Fatalf("%v %v", entries, err)
	}
	for _, name := range []string{"sibling", "../sibling", "new.txt"} {
		if f, e := r.OpenFile(ctx, name, os.O_CREATE|os.O_WRONLY, 0600); e == nil {
			f.Close()
			t.Fatalf("exposed %s", name)
		}
	}
	f, err := r.OpenFile(ctx, "document.txt", os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(f, strings.NewReader("changed"))
	f.Close()
	data, _ := os.ReadFile(source)
	if string(data) != "changed" {
		t.Fatal("selected file not updated")
	}
	if r.RemoveAll(ctx, "document.txt") == nil || r.Rename(ctx, "document.txt", "new") == nil {
		t.Fatal("single source identity changed")
	}
}
func TestMemorySymlinkSwap(t *testing.T) {
	r, source, base := memoryFixture(t)
	target := filepath.Join(source, "race")
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		for {
			select {
			case <-done:
				return
			default:
			}
			os.Remove(target)
			os.Symlink(filepath.Join(base, "secret"), target)
			os.Remove(target)
			f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e == nil {
				f.Write([]byte("inside"))
				f.Close()
			}
		}
	}()
	for i := 0; i < 300; i++ {
		f, e := r.OpenFile(context.Background(), "race", os.O_RDONLY, 0)
		if e != nil {
			continue
		}
		data, _ := io.ReadAll(f)
		f.Close()
		if strings.Contains(string(data), "outside") {
			close(done)
			<-exited
			t.Fatal("read outside through race")
		}
	}
	close(done)
	<-exited
}

func memoryTempDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
