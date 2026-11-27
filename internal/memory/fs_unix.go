//go:build linux || darwin

package memory

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"hostlier/internal/model"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
)

// Root pins the approved source. Each OS backend resolves names without links
// and verifies filesystem identity before permitting I/O.
type Root struct {
	fd       int
	source   string
	identity model.MemoryIdentity
}

func (r *Root) Identity() model.MemoryIdentity { return r.identity }
func (r *Root) Close() error                   { return unix.Close(r.fd) }

// Check refuses replacement/unmount of the reviewed source. A mount identifier
// is retained as well as device/inode so an empty underlying mountpoint cannot
// take over an export when a removable drive disappears.
func (r *Root) Check() error {
	current, err := OpenRoot(r.source)
	if err != nil {
		return fs.ErrPermission
	}
	defer current.Close()
	if current.identity != r.identity {
		return fs.ErrPermission
	}
	return nil
}

func cleanName(name string) (string, error) {
	if strings.ContainsAny(name, "\\\x00") {
		return "", fs.ErrPermission
	}
	name = strings.TrimPrefix(name, "/")
	name = strings.TrimSuffix(name, "/")
	if name == "" || name == "." {
		return ".", nil
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fs.ErrPermission
		}
	}
	return name, nil
}

type confinedFile struct {
	*os.File
	root     *Root
	name     string
	writable bool
}

func (f *confinedFile) Stat() (os.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return namedInfo{FileInfo: info, name: path.Base(f.name)}, nil
}

func (f *confinedFile) Readdir(n int) ([]os.FileInfo, error) {
	// Never return metadata of an entry that cannot itself be safely opened.
	var out []os.FileInfo
	for n <= 0 || len(out) < n {
		items, err := f.File.Readdir(1)
		for _, item := range items {
			child := path.Join(f.name, item.Name())
			fd, e := f.root.resolve(child)
			if e != nil {
				continue
			}
			_, e = identity(fd)
			unix.Close(fd)
			if e == nil {
				out = append(out, item)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && (n <= 0 || len(out) > 0) {
				err = nil
			}
			return out, err
		}
	}
	return out, nil
}
func (f *confinedFile) Close() error {
	// Remote clients are told completion only after data reaches the filesystem.
	var err error
	if f.writable {
		err = f.File.Sync()
	}
	closeErr := f.File.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (r *Root) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	f, err := r.OpenFile(ctx, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}
func (r *Root) parent(name string) (int, string, error) {
	name, err := cleanName(name)
	if err != nil || name == "." || r.identity.File {
		return -1, "", fs.ErrPermission
	}
	fd, err := r.resolve(path.Dir(name))
	if err != nil {
		return -1, "", err
	}
	id, err := identity(fd)
	if err != nil || id.File {
		unix.Close(fd)
		return -1, "", fs.ErrPermission
	}
	return fd, path.Base(name), nil
}
func (r *Root) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, base, err := r.parent(name)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Mkdirat(fd, base, uint32(perm.Perm()))
}
func (r *Root) Rename(ctx context.Context, oldName, newName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Both existing endpoints must be admissible; links cannot be renamed into
	// otherwise valid paths, and single-file exports cannot expose their parent.
	if _, err := r.Stat(ctx, oldName); err != nil {
		return err
	}
	if _, err := r.Stat(ctx, newName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	oldFD, oldBase, err := r.parent(oldName)
	if err != nil {
		return err
	}
	defer unix.Close(oldFD)
	newFD, newBase, err := r.parent(newName)
	if err != nil {
		return err
	}
	defer unix.Close(newFD)
	return unix.Renameat(oldFD, oldBase, newFD, newBase)
}
func (r *Root) RemoveAll(ctx context.Context, name string) error {
	name, err := cleanName(name)
	if err != nil || name == "." || r.identity.File {
		return fs.ErrPermission
	}
	f, err := r.OpenFile(ctx, name, os.O_RDONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if info.IsDir() {
		// Raw names, not filtered listings: refuse deleting trees containing
		// forbidden entries instead of silently claiming to remove them.
		entries, e := f.(*confinedFile).File.Readdirnames(-1)
		if e != nil {
			f.Close()
			return e
		}
		for _, entry := range entries {
			if e = r.RemoveAll(ctx, path.Join(name, entry)); e != nil {
				f.Close()
				return e
			}
		}
	}
	f.Close()
	fd, base, err := r.parent(name)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	flags := 0
	if info.IsDir() {
		flags = unix.AT_REMOVEDIR
	}
	return unix.Unlinkat(fd, base, flags)
}

// A single file is presented as a virtual one-entry directory to native clients.
type singleDirectory struct {
	root *Root
	read bool
}

func (d *singleDirectory) Close() error                   { return nil }
func (d *singleDirectory) Read([]byte) (int, error)       { return 0, fs.ErrPermission }
func (d *singleDirectory) Write([]byte) (int, error)      { return 0, fs.ErrPermission }
func (d *singleDirectory) Seek(int64, int) (int64, error) { return 0, fs.ErrPermission }
func (d *singleDirectory) Stat() (os.FileInfo, error)     { return directoryInfo{}, nil }
func (d *singleDirectory) Readdir(n int) ([]os.FileInfo, error) {
	if d.read {
		if n > 0 {
			return nil, io.EOF
		}
		return nil, nil
	}
	d.read = true
	info, err := d.root.Stat(context.Background(), path.Base(d.root.source))
	if err != nil {
		return nil, err
	}
	return []os.FileInfo{namedInfo{FileInfo: info, name: path.Base(d.root.source)}}, nil
}

type namedInfo struct {
	os.FileInfo
	name string
}

func (i namedInfo) Name() string                { return i.name }
func (i namedInfo) underlyingInfo() os.FileInfo { return i.FileInfo }

type directoryInfo struct{}

func (directoryInfo) Name() string       { return "/" }
func (directoryInfo) Size() int64        { return 0 }
func (directoryInfo) Mode() os.FileMode  { return os.ModeDir | 0755 }
func (directoryInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (directoryInfo) IsDir() bool        { return true }
func (directoryInfo) Sys() any           { return nil }
