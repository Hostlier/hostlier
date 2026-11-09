package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// initSetupWrite records one reviewed file and the identities owned by this transaction.
type initSetupWrite struct {
	path, value, original, staged string
	replace, applied              bool
	mode                          os.FileMode
	before, installed             os.FileInfo
}

// writeInitSetup exclusively creates scaffolds and atomically updates explicitly reviewed Minion configuration.
func writeInitSetup(root string, files, originals map[string]string) (err error) {
	root = filepath.Clean(root)
	writes, err := planInitSetup(root, files, originals)
	// All original bytes and output conflicts are checked before any filesystem mutation.
	if err != nil {
		return err
	}
	directories := []string{}
	// Cleanup removes staging files and conditionally restores only transaction-owned identities.
	defer func() {
		// Failed transactions must not leave partially initialized projects.
		if err != nil {
			rollbackInitSetup(writes, directories)
		}
		// Uninstalled staged replacements are always disposable.
		for _, write := range writes {
			// Successfully published files no longer have a staging path.
			if write.staged != "" {
				_ = os.Remove(write.staged)
			}
		}
	}()
	// Each replacement is fully staged before it can replace an authored configuration.
	for _, write := range writes {
		// Abort immediately so the deferred rollback owns the successful prefix.
		if err = applyInitSetup(root, write, &directories); err != nil {
			return err
		}
	}
	return nil
}

// planInitSetup validates the complete requested write set without touching file contents.
func planInitSetup(root string, files, originals map[string]string) ([]*initSetupWrite, error) {
	// Only replacements included in this reviewed write set are eligible for modification.
	for path := range originals {
		// An original without a replacement would authorize an unspecified mutation.
		if _, ok := files[path]; !ok {
			return nil, fmt.Errorf("Missing reviewed Minion replacement: %s", path)
		}
	}
	writes := []*initSetupWrite{}
	// Stable ordering makes errors and rollback behavior deterministic.
	for _, path := range sortedInitKeys(files) {
		write, err := planInitSetupFile(root, path, files[path], originals)
		// A single conflict prevents all changes, including unrelated new files.
		if err != nil {
			return nil, err
		}
		writes = append(writes, write)
	}
	return writes, nil
}

// planInitSetupFile admits only new outputs or byte-identical regular minion.json replacements.
func planInitSetupFile(root, path, value string, originals map[string]string) (*initSetupWrite, error) {
	// A file output cannot replace the selected application directory itself.
	if filepath.Clean(path) == root {
		return nil, fmt.Errorf("Scaffold output must be a file beneath the app directory")
	}
	// Lexical containment is checked separately from parent filesystem identities.
	if err := validateInitOutputPath(root, path); err != nil {
		return nil, err
	}
	// Ancestor symlinks cannot redirect future temporary or output files.
	if err := inspectInitSetupParents(root, filepath.Dir(path)); err != nil {
		return nil, err
	}
	original, replace := originals[path]
	write := &initSetupWrite{path: path, value: value, original: original, replace: replace, mode: 0600}
	info, err := os.Lstat(path)
	// New outputs never replace an existing file, directory or symbolic link.
	if !replace {
		// Any existing object or inspection failure blocks exclusive creation.
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("Refusing to overwrite existing file: %s", path)
		}
		return write, nil
	}
	// Only Minion settings and the root starter manifest can receive reviewed replacements.
	if (filepath.Base(path) != "minion.json" && path != filepath.Join(root, ".hostlier")) || err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Init replacement must be a regular settings file: %s", path)
	}
	write.before, write.mode = info, info.Mode().Perm()
	// Source bytes must still match the configuration presented to the user.
	if !matchesInitSetup(path, original, info) {
		return nil, fmt.Errorf("Project settings changed during setup; run init again: %s", path)
	}
	return write, nil
}

// inspectInitSetupParents rejects ancestor symlinks without creating absent directories during preflight.
func inspectInitSetupParents(root, directory string) error {
	// The explicitly selected canonical root is the filesystem ownership boundary.
	if directory == root {
		return nil
	}
	// Inspect every ancestor, including those above an existing leaf directory.
	if err := inspectInitSetupParents(root, filepath.Dir(directory)); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	// Absent directories can be created later by the exclusive scaffold writer.
	if os.IsNotExist(err) {
		return nil
	}
	// Existing filesystem errors cannot be interpreted as safe absence.
	if err != nil {
		return err
	}
	// Lstat identifies symlinks independently of their targets.
	if !info.IsDir() {
		return fmt.Errorf("Scaffold parent is not a directory: %s", directory)
	}
	return nil
}

// applyInitSetup stages complete replacements and rechecks authored bytes immediately before atomic publication.
func applyInitSetup(root string, write *initSetupWrite, directories *[]string) error {
	// Recheck parent ownership while creating only missing scaffold directories.
	if err := ensureInitDirectory(root, filepath.Dir(write.path), directories); err != nil {
		return err
	}
	// Ordinary scaffold files retain exclusive creation semantics.
	if !write.replace {
		// Concurrent output creation is a conflict rather than overwrite authority.
		if err := createInitFile(write.path, write.value); err != nil {
			return err
		}
		write.installed, _ = os.Lstat(write.path)
		write.applied = true
		return nil
	}
	staged, err := stageInitSetup(write.path, write.value, write.mode)
	// Staging errors leave the authored file unchanged.
	if err != nil {
		return err
	}
	write.staged = staged
	// Detect edits or symlink replacement since the preflight snapshot.
	if !matchesInitSetup(write.path, write.original, write.before) {
		return fmt.Errorf("Project settings changed during setup; run init again: %s", write.path)
	}
	// Rename publishes a complete document while retaining the original permission mode.
	if err := os.Rename(staged, write.path); err != nil {
		return err
	}
	write.staged = ""
	write.installed, _ = os.Lstat(write.path)
	write.applied = true
	return nil
}

// stageInitSetup prepares a same-directory replacement without exposing partial configuration bytes.
func stageInitSetup(path, value string, mode os.FileMode) (name string, err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".hostlier-init-*")
	// A failed temporary file allocation cannot affect the original configuration.
	if err != nil {
		return "", err
	}
	name = file.Name()
	// Cleanup remains responsible for every staging error, including a failed close.
	defer func() {
		closeErr := file.Close()
		// Report close failures without revealing configuration contents.
		if err == nil {
			err = closeErr
		}
		// Failed staging files never become reviewable outputs.
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	// Permissions are inherited exactly rather than resetting a user's private file mode.
	if err = file.Chmod(mode); err != nil {
		return name, err
	}
	_, err = file.WriteString(value)
	return name, err
}

// matchesInitSetup checks both content and inode identity without following symbolic links.
func matchesInitSetup(path, value string, expected os.FileInfo) bool {
	current, err := os.Lstat(path)
	// Identity changes, links and absent files are all concurrent modifications.
	if err != nil || expected == nil || !current.Mode().IsRegular() || !os.SameFile(current, expected) {
		return false
	}
	data, err := readInitFile(path)
	return err == nil && string(data) == value
}

// rollbackInitSetup restores only files still matching the bytes and identities written by this attempt.
func rollbackInitSetup(writes []*initSetupWrite, directories []string) {
	// Reverse order mirrors the successful prefix of the transaction.
	for i := len(writes) - 1; i >= 0; i-- {
		rollbackInitSetupFile(writes[i])
	}
	// Empty owned directories are removable; directories populated by other processes survive.
	for i := len(directories) - 1; i >= 0; i-- {
		_ = os.Remove(directories[i])
	}
}

// rollbackInitSetupFile avoids erasing a user's edit made after an earlier successful write.
func rollbackInitSetupFile(write *initSetupWrite) {
	// Changed or replaced files no longer belong to this transaction's rollback.
	if !write.applied || !matchesInitSetup(write.path, write.value, write.installed) {
		return
	}
	// New scaffolds can simply be removed when they are still our exact outputs.
	if !write.replace {
		_ = os.Remove(write.path)
		return
	}
	staged, err := stageInitSetup(write.path, write.original, write.mode)
	// A failed rollback stage leaves the complete current config rather than truncating it.
	if err != nil {
		return
	}
	defer os.Remove(staged)
	// Recheck identity and bytes after staging to preserve a competing editor's changes.
	if matchesInitSetup(write.path, write.value, write.installed) {
		_ = os.Rename(staged, write.path)
	}
}
