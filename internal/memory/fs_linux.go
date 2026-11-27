//go:build linux

package memory

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/net/webdav"
	"golang.org/x/sys/unix"
	"hostlier/internal/model"
	"io/fs"
	"os"
	"path"
)

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
	var sx unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &sx); err != nil {
		return model.MemoryIdentity{}, err
	}
	if sx.Mask&unix.STATX_MNT_ID == 0 {
		return model.MemoryIdentity{}, fs.ErrPermission
	}
	return model.MemoryIdentity{Device: uint64(st.Dev), Inode: st.Ino, Mount: sx.Mnt_id, File: st.Mode&unix.S_IFMT == unix.S_IFREG}, nil
}

func OpenRoot(source string) (*Root, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, source, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, fmt.Errorf("open export: %w", err)
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
		return unix.Dup(r.fd)
	}
	return unix.Openat2(r.fd, name, &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV})
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
		if err := r.Check(); err != nil {
			return nil, err
		}
		return &singleDirectory{root: r}, nil
	}
	fd, err := r.resolve(name)
	created := false
	if errors.Is(err, unix.ENOENT) && flags&os.O_CREATE != 0 && !r.identity.File {
		// O_EXCL ensures a race cannot replace the absent path with a link.
		fd, err = unix.Openat2(r.fd, name, &unix.OpenHow{
			Flags: uint64((flags &^ os.O_TRUNC) | os.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK),
			Mode:  uint64(perm.Perm()), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
		created = err == nil
	}
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	id, err := identity(fd)
	if err != nil {
		return nil, err
	}
	if !created && flags&os.O_CREATE != 0 && flags&os.O_EXCL != 0 {
		return nil, fs.ErrExist
	}
	// Open the inspected inode, never re-resolve a caller-controlled pathname.
	f, err := os.OpenFile(fmt.Sprintf("/proc/self/fd/%d", fd), flags&^(os.O_CREATE|os.O_EXCL|os.O_TRUNC), perm)
	if err != nil {
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
