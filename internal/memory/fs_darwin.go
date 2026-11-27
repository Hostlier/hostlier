//go:build darwin

package memory

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"

	"golang.org/x/net/webdav"
	"golang.org/x/sys/unix"
	"hostlier/internal/model"
)

// identity uses the mounted filesystem's identity as well as the file identity.
// Inspect each component, not just the leaf: a path must not cross a nested
// mount and return to the original filesystem through a second mount.
func identity(fd int) (model.MemoryIdentity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return model.MemoryIdentity{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR && st.Mode&unix.S_IFMT != unix.S_IFREG {
		return model.MemoryIdentity{}, fs.ErrPermission
	}
	if st.Mode&unix.S_IFMT == unix.S_IFREG && st.Nlink != 1 {
		return model.MemoryIdentity{}, fs.ErrPermission
	}
	var mounted unix.Statfs_t
	if err := unix.Fstatfs(fd, &mounted); err != nil {
		return model.MemoryIdentity{}, err
	}
	mount := uint64(uint32(mounted.Fsid.Val[0]))<<32 | uint64(uint32(mounted.Fsid.Val[1]))
	return model.MemoryIdentity{Device: uint64(uint32(st.Dev)), Inode: st.Ino, Mount: mount, File: st.Mode&unix.S_IFMT == unix.S_IFREG}, nil
}

// openComponent refuses special files before opening and checks again on the
// resulting descriptor. O_NOFOLLOW is enforced by the kernel on every component.
// O_EVTONLY pins the resource without preventing removal of an external volume.
func openComponent(parent int, name string, directory bool) (int, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return -1, err
	}
	kind := st.Mode & unix.S_IFMT
	if kind != unix.S_IFDIR && kind != unix.S_IFREG {
		return -1, fs.ErrPermission
	}
	flags := unix.O_EVTONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(parent, name, flags, 0)
	if err != nil {
		return -1, err
	}
	if _, err = identity(fd); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}
func walkDescriptor(start int, name string, boundary *model.MemoryIdentity) (int, error) {
	fd, err := unix.Dup(start)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		next, err := openComponent(fd, part, i < len(parts)-1)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		id, err := identity(next)
		if err != nil || boundary != nil && (id.Device != boundary.Device || id.Mount != boundary.Mount) {
			unix.Close(next)
			return -1, fs.ErrPermission
		}
		fd = next
	}
	unix.CloseOnExec(fd)
	return fd, nil
}
func OpenRoot(source string) (*Root, error) {
	if !path.IsAbs(source) || path.Clean(source) != source || strings.ContainsAny(source, "\\\x00") {
		return nil, fs.ErrPermission
	}
	anchor, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(anchor)
	name := strings.TrimPrefix(source, "/")
	if name == "" {
		name = "."
	}
	fd, err := walkDescriptor(anchor, name, nil)
	if err != nil {
		return nil, err
	}
	id, err := identity(fd)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &Root{fd: fd, source: source, identity: id}, nil
}
func (r *Root) resolve(name string) (int, error) {
	if err := r.Check(); err != nil {
		return -1, err
	}
	if r.identity.File {
		if name != path.Base(r.source) {
			return -1, fs.ErrNotExist
		}
		fd, err := unix.Dup(r.fd)
		if err == nil {
			unix.CloseOnExec(fd)
		}
		return fd, err
	}
	return walkDescriptor(r.fd, name, &r.identity)
}
func (r *Root) OpenFile(ctx context.Context, name string, flags int, perm os.FileMode) (webdav.File, error) {
	name, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if r.identity.File && name == "." {
		if flags != os.O_RDONLY {
			return nil, fs.ErrPermission
		}
		if err = r.Check(); err != nil {
			return nil, err
		}
		return &singleDirectory{root: r}, nil
	}
	inspected, err := r.resolve(name)
	create := errors.Is(err, fs.ErrNotExist) && flags&os.O_CREATE != 0 && !r.identity.File
	if err != nil && !create {
		return nil, err
	}
	var expected model.MemoryIdentity
	if !create {
		defer unix.Close(inspected)
		expected, err = identity(inspected)
		if err != nil {
			return nil, err
		}
		if flags&os.O_CREATE != 0 && flags&os.O_EXCL != 0 {
			return nil, fs.ErrExist
		}
	}
	// macOS cannot reopen an event-only descriptor for writing. Resolve the
	// parent safely, open without truncation, and compare the opened inode with
	// the inspected inode before any read, write or truncation.
	var parent int
	if r.identity.File {
		dir, err := OpenRoot(path.Dir(r.source))
		if err != nil {
			return nil, err
		}
		defer dir.Close()
		parent = dir.fd
	} else {
		parent, err = r.resolve(path.Dir(name))
		if err != nil {
			return nil, err
		}
		defer unix.Close(parent)
	}
	openFlags := flags&^(os.O_TRUNC|os.O_CREATE|os.O_EXCL) | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if create {
		openFlags |= os.O_CREATE | os.O_EXCL
	}
	fd, err := unix.Openat(parent, path.Base(name), openFlags, uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	id, err := identity(fd)
	if err != nil || !create && id != expected || id.Device != r.identity.Device || id.Mount != r.identity.Mount {
		unix.Close(fd)
		return nil, fs.ErrPermission
	}
	f := os.NewFile(uintptr(fd), name)
	if err = r.Check(); err != nil {
		f.Close()
		return nil, err
	}
	if flags&os.O_TRUNC != 0 {
		if !id.File {
			f.Close()
			return nil, fs.ErrPermission
		}
		if err = f.Truncate(0); err != nil {
			f.Close()
			return nil, err
		}
	}
	return &confinedFile{File: f, root: r, name: name, writable: flags&(os.O_WRONLY|os.O_RDWR) != 0}, nil
}
