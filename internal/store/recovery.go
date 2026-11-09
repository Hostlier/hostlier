package store

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hostlier/internal/model"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BundleManifest records checksummed controller files and the recovery boundary.
type BundleManifest struct {
	StateDirectory string            `json:"stateDirectory"`
	Version        int               `json:"version"`
	Files          map[string]string `json:"files"`
	Scope          string            `json:"scope"`
}

// Export captures SQLite through VACUUM INTO while holding exclusive controller ownership.
func Export(dir, output, config string) error {
	return exportWithConfiguration(dir, output, config, "/etc/hostlier", "/var/lib/caddy")
}

// exportWithConfiguration keeps system roots explicit for isolated recovery verification.
func exportWithConfiguration(dir, output, config, settingsRoot, caddyRoot string) error {
	// Stable absolute operational paths preserve all runtime and engine ownership hashes.
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("backup requires the original absolute state directory")
	}
	// Staging metadata cannot be re-exported as a newly owned controller.
	if err := model.ValidateRecoveryLocation(dir); err != nil {
		return err
	}
	var state map[string]any
	s, err := Open(dir, &state)
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	defer s.Close()
	scratch, err := os.MkdirTemp("", "hostlier-backup-")
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	snapshot := filepath.Join(scratch, "state.db")
	// Validate the precondition before accepting protected state.
	if _, err = s.db.Exec("VACUUM INTO ?", snapshot); err != nil {
		return err
	}
	files, err := recoveryFiles(dir, snapshot, config)
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	// Installed proxy configuration and TLS keys are staged for explicit init recovery.
	if err = collectServerConfiguration(files, settingsRoot); err != nil {
		return err
	}
	// Caddy keeps its ACME accounts and certificate cache outside the configuration tree.
	if err = collectCaddyState(files, caddyRoot); err != nil {
		return err
	}
	origin, _ := json.Marshal(model.RecoveryOrigin{StateDirectory: dir})
	files["recovery-origin.json"] = origin
	return writeBundle(output, files)
}

// recoveryFiles includes adapter metadata, installed executables and configured server settings.
func recoveryFiles(dir, snapshot, config string) (map[string][]byte, error) {
	files := map[string][]byte{}
	// Collect only safe regular controller files under the exclusive lock.
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		return collectRecoveryStateFile(files, dir, path, entry, walkErr)
	})
	// Propagate failures before publishing recovery state.
	if err != nil {
		return nil, err
	}
	// Managed file modes must survive backup without weakening private directory boundaries.
	if err = collectManagedConfigModes(dir, files); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(snapshot)
	files["state.db"] = data
	// Propagate failures before publishing recovery state.
	if err != nil {
		return nil, err
	}
	// Validate the precondition before accepting protected state.
	if config != "" {
		files["recovered-config.yaml"], err = os.ReadFile(config)
	}
	return files, err
}

// writeBundle exclusively creates an owner-only checksummed portable tar archive.
func writeBundle(output string, files map[string][]byte) error {
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	defer file.Close()
	writer := tar.NewWriter(file)
	var origin model.RecoveryOrigin
	json.Unmarshal(files["recovery-origin.json"], &origin)
	manifest := BundleManifest{StateDirectory: origin.StateDirectory, Version: 1, Files: map[string]string{}, Scope: "Controller metadata only; database contents and application volumes require separate backups. Original controller must remain stopped."}
	// Hash every included byte, including package executables and adapter metadata.
	for name, data := range files {
		digest := sha256.Sum256(data)
		manifest.Files[name] = hex.EncodeToString(digest[:])
		// Propagate failures before publishing recovery state.
		if err = tarEntry(writer, name, data); err != nil {
			return err
		}
	}
	data, err := json.Marshal(manifest)
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	// Propagate failures before publishing recovery state.
	if err = tarEntry(writer, "manifest.json", data); err != nil {
		return err
	}
	// Propagate failures before publishing recovery state.
	if err = writer.Close(); err != nil {
		return err
	}
	return file.Sync()
}

// tarEntry emits regular files without trusting source permissions or links.
func tarEntry(writer *tar.Writer, name string, data []byte) error {
	// Propagate failures before publishing recovery state.
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := writer.Write(data)
	return err
}

// Restore validates all archive contents before creating a fresh destination.
func Restore(input, dir string) error {
	// Existing paths, including symlinks and running controllers, cannot be overwritten.
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		return errors.New("restore requires a new absent state directory")
	}
	files, err := readBundle(input)
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	parent := filepath.Dir(dir)
	// Propagate failures before publishing recovery state.
	if err = rejectSymlinkAncestors(parent); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(parent, ".hostlier-restore-")
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	// Propagate failures before publishing recovery state.
	if err = restoreFiles(temp, files); err != nil {
		return err
	}
	// Reconstruct only confined Certbot links after every archive byte was verified.
	if err = restoreConfigurationLinks(temp, files); err != nil {
		return err
	}
	var state map[string]any
	restored, err := Open(temp, &state)
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	restored.Close()
	// Publish the validated directory only after all restored files are durable.
	if err = os.Rename(temp, dir); err != nil {
		return err
	}
	return syncDirectory(parent)
}

// restoreFiles recreates only verified regular files beneath a private staging directory.
func restoreFiles(dir string, files map[string][]byte) error {
	modes, err := restoredManagedConfigModes(files)
	// Validate all mode metadata before creating even the first restored file.
	if err != nil {
		return err
	}
	// Process each protected record with the same validation rules.
	for name, data := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		// Propagate failures before publishing recovery state.
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		// Validate the precondition before accepting protected state.
		if strings.HasPrefix(name, "packages/") {
			mode = 0700
		}
		// A private parent directory protects read-only container configuration from host users.
		if restored := modes[name]; restored != 0 {
			mode = os.FileMode(restored)
		}
		// Propagate failures before publishing recovery state.
		if err := durableRecoveryFile(path, data, mode); err != nil {
			return err
		}
	}
	return nil
}

// readBundle checks names, types, lengths and hashes before trusting a recovery archive.
func readBundle(input string) (map[string][]byte, error) {
	// Propagate failures before publishing recovery state.
	if err := regularOrMissing(input); err != nil {
		return nil, err
	}
	file, err := os.Open(input)
	// Propagate failures before publishing recovery state.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	files := map[string][]byte{}
	// Process each protected record with the same validation rules.
	for {
		header, err := reader.Next()
		// Propagate failures before publishing recovery state.
		if errors.Is(err, io.EOF) {
			break
		}
		// Propagate failures before publishing recovery state.
		if err != nil {
			return nil, err
		}
		// Propagate failures before publishing recovery state.
		if err = validArchiveEntry(header, files); err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		// Propagate failures before publishing recovery state.
		if err != nil {
			return nil, err
		}
		files[header.Name] = data
	}
	return verifyBundle(files)
}

// validArchiveEntry rejects traversal, duplicate files and unbounded archive members.
func validArchiveEntry(header *tar.Header, files map[string][]byte) error {
	name := header.Name
	// Validate the precondition before accepting protected state.
	if header.Typeflag != tar.TypeReg || name == "." || strings.Contains(name, "\\") || filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || strings.HasPrefix(name, "../") {
		return errors.New("unsafe recovery archive entry")
	}
	// Validate the precondition before accepting protected state.
	if _, exists := files[name]; exists {
		return errors.New("duplicate recovery archive entry")
	}
	// Validate the precondition before accepting protected state.
	if header.Size < 0 || header.Size > 256<<20 {
		return errors.New("recovery entry exceeds size limit")
	}
	return nil
}

// verifyBundle requires exact manifest coverage and original key material.
func verifyBundle(files map[string][]byte) (map[string][]byte, error) {
	var manifest BundleManifest
	// Propagate failures before publishing recovery state.
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		return nil, err
	}
	delete(files, "manifest.json")
	// Validate the precondition before accepting protected state.
	if manifest.Version != 1 || !filepath.IsAbs(manifest.StateDirectory) || len(manifest.Files) != len(files) {
		return nil, errors.New("unsupported or incomplete recovery bundle")
	}
	// Process each protected record with the same validation rules.
	for name, data := range files {
		sum := sha256.Sum256(data)
		// Validate the precondition before accepting protected state.
		if manifest.Files[name] != hex.EncodeToString(sum[:]) {
			return nil, fmt.Errorf("recovery checksum mismatch: %s", name)
		}
	}
	// Validate the precondition before accepting protected state.
	if len(files["secret.key"]) != 32 || len(files["state.db"]) == 0 {
		return nil, errors.New("recovery bundle missing state or original key")
	}
	return files, nil
}

// rejectSymlinkAncestors ensures a fresh destination cannot redirect through a parent link.
func rejectSymlinkAncestors(path string) error {
	// macOS exposes standard temporary roots through trusted system aliases.
	if path == "/var" || path == "/tmp" {
		resolved, err := filepath.EvalSymlinks(path)
		// Propagate failures before publishing recovery state.
		if err != nil {
			return err
		}
		// Linux temporary roots are real directories rather than aliases.
		if resolved != path {
			return rejectSymlinkAncestors(resolved)
		}
	}
	info, err := os.Lstat(path)
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	// Validate the precondition before accepting protected state.
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe recovery destination ancestor")
	}
	// Validate the precondition before accepting protected state.
	if parent := filepath.Dir(path); parent != path {
		return rejectSymlinkAncestors(parent)
	}
	return nil
}

// excludedRecoveryFile omits writer artifacts and obsolete snapshots.
func excludedRecoveryFile(name string) bool {
	return name == "controller.lock" || name == "state.json" || strings.HasPrefix(name, "state.db")
}

// durableRecoveryFile flushes a restored file before the staging directory is published.
func durableRecoveryFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	// Never overwrite another staging entry or redirected path.
	if err != nil {
		return err
	}
	defer file.Close()
	// Stop before synchronization if writing the payload failed.
	if _, err = file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

// collectRecoveryStateFile excludes stale staging before validating active controller files.
func collectRecoveryStateFile(files map[string][]byte, dir, path string, entry os.DirEntry, walkErr error) error {
	// Propagate failures before publishing recovery state.
	if walkErr != nil {
		return walkErr
	}
	// Reconstructible telemetry and stale staging assets are excluded from authoritative recovery.
	if transientRecoveryDirectory(dir, path) && entry.IsDir() {
		return filepath.SkipDir
	}
	// Validate the precondition before accepting protected state.
	if entry.IsDir() {
		return nil
	}
	name, err := filepath.Rel(dir, path)
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	// Exclude SQLite transient files, obsolete migration source and writer ownership.
	if excludedRecoveryFile(name) {
		return nil
	}
	// Propagate failures before publishing recovery state.
	if err = regularOrMissing(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	files[filepath.ToSlash(name)] = data
	return err
}

// transientRecoveryDirectory identifies reconstructible data and unapproved executable staging.
func transientRecoveryDirectory(dir, path string) bool {
	return path == filepath.Join(dir, "recovery-config") || path == filepath.Join(dir, "observations") || path == filepath.Join(dir, "collector-image") || path == filepath.Join(dir, "package-uploads")
}
