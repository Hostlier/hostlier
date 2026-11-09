package runtime

import (
	"fmt"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// configMounts writes per-operation immutable files, never mutating a live deployment's source inode.
func (d *dockerDeployment) configMounts(p model.Project, member string) ([]any, error) {
	mounts := []any{}
	// The validated schema is also enforced for direct runtime callers.
	if len(p.Configs) == 0 {
		return mounts, nil
	}
	// Config validation must precede any host filesystem mutation.
	if _, err := config.ValidateManagedConfigs(p); err != nil {
		return nil, err
	}
	role := "app"
	// Members receive separate file namespaces and secret scopes.
	if member != "" {
		role = "sidecar-" + member
	}
	dir := filepath.Join(d.runtime.options.StateDir, "config-files", d.name, role)
	// Every directory component is owned and refuses symbolic link traversal.
	if err := privateConfigDirectory(d.runtime.options.StateDir, dir); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(p.Configs))
	// Stable order preserves idempotent replay and deterministic failure reports.
	for name := range p.Configs {
		names = append(names, name)
	}
	sort.Strings(names)
	// Only exact named files become read-only bind mounts.
	for _, name := range names {
		file := p.Configs[name]
		data, err := configFileContent(file, member, name, d.dc.Secrets)
		// Missing pinned secret values must fail before creating any mount.
		if err != nil {
			return nil, err
		}
		source := filepath.Join(dir, name)
		// Immutable replay verifies existing bytes instead of overwriting an active mount.
		if err := writeImmutableConfig(source, []byte(data), os.FileMode(file.Mode)); err != nil {
			return nil, err
		}
		mounts = append(mounts, map[string]any{"Type": "bind", "Source": source, "Target": file.Mount, "ReadOnly": true})
	}
	return mounts, nil
}

// privateConfigDirectory rejects symlinks at every owned path component.
func privateConfigDirectory(root, dir string) error {
	relative, err := filepath.Rel(root, dir)
	// Paths must stay strictly beneath the protected runtime state directory.
	if err != nil || strings.HasPrefix(relative, "..") {
		return fmt.Errorf("invalid managed config directory")
	}
	current := root
	// Build each component without following pre-existing links.
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		// Existing directories are accepted only after checking their actual file type.
		if err := os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(current)
		// Config directories are private regular directories, never redirected paths.
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe managed config directory")
		}
		// Host users cannot inspect secret-backed files through public parent directories.
		if err := os.Chmod(current, 0700); err != nil {
			return err
		}
	}
	return nil
}

// writeImmutableConfig exclusively creates a file or verifies an exact idempotent replay.
func writeImmutableConfig(path string, data []byte, mode os.FileMode) error {
	// Defaults remain read-only inside the container even for direct runtime callers.
	if mode == 0 {
		mode = 0444
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	// Existing files may be reused only when they are ordinary files with the same bytes.
	if os.IsExist(err) {
		return verifyImmutableConfig(path, data, mode)
	}
	// Failed creation cannot fall back to an arbitrary host file.
	if err != nil {
		return err
	}
	defer f.Close()
	// Persist complete content before granting read access through a bind mount.
	if _, err = f.Write(data); err != nil {
		return err
	}
	// A failed sync prevents container creation using incomplete input.
	if err = f.Sync(); err != nil {
		return err
	}
	return f.Chmod(mode)
}

// verifyImmutableConfig rejects redirected or changed replay inputs.
func verifyImmutableConfig(path string, data []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	// Symlinks and special files cannot satisfy an immutable configuration reference.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode.Perm() {
		return model.Err("CONFIG_CONFLICT", "Managed config replay differs from approved input")
	}
	existing, err := os.ReadFile(path)
	// Secret bytes never appear in the public conflict message.
	if err != nil || string(existing) != string(data) {
		return model.Err("CONFIG_CONFLICT", "Managed config replay differs from approved input")
	}
	return nil
}

// configFileContent resolves only the file's exact namespace-scoped secret reference.
func configFileContent(file model.ConfigFile, member, name string, values map[string]string) (string, error) {
	// Nonsecret contents are already part of the immutable project revision.
	if file.Secret == "" {
		return file.Content, nil
	}
	key := "files/" + name
	// Sidecars cannot inherit primary or sibling file credentials.
	if member != "" {
		key = member + "/" + key
	}
	value, ok := values[key]
	// Missing credentials never turn into an empty configuration file.
	if !ok {
		return "", model.Err("CONFIG_SECRET", "Managed config secret revision unavailable")
	}
	return value, nil
}
