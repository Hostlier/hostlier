//go:build !linux && !darwin

package memory

import (
	"context"
	"golang.org/x/net/webdav"
	"hostlier/internal/model"
	"io/fs"
	"os"
)

// Serving fails closed on systems without the Linux containment primitives.
// Native clients on macOS and Linux do not depend on this server backend.
type Root struct{}

func OpenRoot(string) (*Root, error) {
	return nil, model.Err("MEMORY_PLATFORM", "Memory controllers require Linux or macOS")
}
func (*Root) Identity() model.MemoryIdentity { return model.MemoryIdentity{} }
func (*Root) Close() error                   { return nil }
func (*Root) Check() error                   { return fs.ErrPermission }
func (*Root) OpenFile(context.Context, string, int, os.FileMode) (webdav.File, error) {
	return nil, fs.ErrPermission
}
func (*Root) Stat(context.Context, string) (os.FileInfo, error) { return nil, fs.ErrPermission }
func (*Root) Mkdir(context.Context, string, os.FileMode) error  { return fs.ErrPermission }
func (*Root) Rename(context.Context, string, string) error      { return fs.ErrPermission }
func (*Root) RemoveAll(context.Context, string) error           { return fs.ErrPermission }
