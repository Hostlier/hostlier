// Package controllerbinary locates the separately distributed server executable.
package controllerbinary

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve requires the controller beside the CLI, including when launched through a symlink.
func Resolve() (string, error) {
	executable, err := os.Executable()
	// Resolve the actual release directory before choosing a sibling executable.
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	// A broken CLI installation cannot select a trusted sibling.
	if err != nil {
		return "", err
	}
	return sibling(executable)
}

// sibling validates the server artifact without searching an unrelated working directory or PATH.
func sibling(executable string) (string, error) {
	name := "hostlier-controller"
	// Raw cross-compiled artifacts retain their target suffix until extracted or installed.
	if suffix, ok := strings.CutPrefix(filepath.Base(executable), "hostlier-linux-"); ok {
		name += "-linux-" + suffix
	}
	path := filepath.Join(filepath.Dir(executable), name)
	info, err := os.Stat(path)
	// A missing or nonexecutable artifact must fail before installation changes the host.
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("hostlier-controller executable required beside %s; install both executables from the same release", executable)
	}
	return path, nil
}
