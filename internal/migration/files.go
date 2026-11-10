package migration

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// Archive retains the typed inputs required by this migration step.
type Archive struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// newArchivePath keeps this migration step within its validated recovery boundary.
func newArchivePath(dir, prefix string) (string, error) {
	random := make([]byte, 8)
	// Preserve failure before advancing to the next external-effect boundary.
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return filepath.Join(dir, prefix+"-"+hex.EncodeToString(random)+".tar"), nil
}

// safeAncestors rejects symlinked ancestry and special filesystem objects.
func safeAncestors(p string) error {
	var err error
	p, err = resolveSystemAlias(p)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}

	info, err := os.Lstat(p)
	// Preserve failure before advancing to the next external-effect boundary.
	if os.IsNotExist(err) {
		parent := filepath.Dir(p)
		// Reject incompatible state before continuing the protected migration step.
		if parent == p {
			return err
		}
		return safeAncestors(parent)
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe directory: %s", p)
	}
	// Reject incompatible state before continuing the protected migration step.
	if parent := filepath.Dir(p); parent != p {
		return safeAncestors(parent)
	}
	return nil
}

// resolveSystemAlias permits only the standard macOS-owned temporary root aliases.
func resolveSystemAlias(p string) (string, error) {
	// Reject incompatible state before continuing the protected migration step.
	if p == "/var" || p == "/tmp" {
		return filepath.EvalSymlinks(p)
	}
	return p, nil
}

// writeJSON keeps this migration step within its validated recovery boundary.
func writeJSON(p string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	return writeFile(p, data, 0600)
}

// writeFile atomically publishes a fully synchronized, privately staged record.
func writeFile(p string, data []byte, mode os.FileMode) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := safeOutputFile(p); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".migration-")
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	// Preserve failure before advancing to the next external-effect boundary.
	if err := writeStagedFile(f, data, mode); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := os.Rename(f.Name(), p); err != nil {
		return err
	}
	return syncDir(filepath.Dir(p))
}

// safeOutputFile refuses redirected metadata and unsafe directory ancestry.
func safeOutputFile(p string) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := safeAncestors(filepath.Dir(p)); err != nil {
		return err
	}
	info, err := os.Lstat(p)
	// Preserve failure before advancing to the next external-effect boundary.
	if os.IsNotExist(err) {
		return nil
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsafe file: %s", p)
	}
	return nil
}

// writeStagedFile flushes permissions and every byte before the rename boundary.
func writeStagedFile(f *os.File, data []byte, mode os.FileMode) error {
	defer f.Close()
	// Preserve failure before advancing to the next external-effect boundary.
	if err := f.Chmod(mode); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if _, err := f.Write(data); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// syncDir keeps this migration step within its validated recovery boundary.
func syncDir(p string) error {
	d, err := os.Open(p)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// hashFile streams a regular backup file into its journal checksum.
func hashFile(p string) (Archive, error) {
	var a Archive
	info, err := os.Lstat(p)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return a, err
	}
	// Reject incompatible state before continuing the protected migration step.
	if !info.Mode().IsRegular() {
		return a, fmt.Errorf("archive must be a regular file")
	}
	f, err := os.Open(p)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return a, err
	}
	defer f.Close()
	hash := sha256.New()
	a.Bytes, err = io.Copy(hash, f)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return a, err
	}
	a.File = p
	a.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return a, nil
}

// verifyArchive reads the entire file back and validates entry paths and links
// before any source or destination can be replaced.
func verifyArchive(a Archive) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := verifyArchiveChecksum(a); err != nil {
		return err
	}
	f, err := os.Open(a.File)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	defer f.Close()
	reader := tar.NewReader(f)
	// Bound repeated observations by completion, input exhaustion or cancellation.
	for {
		h, err := reader.Next()
		// Preserve failure before advancing to the next external-effect boundary.
		if err == io.EOF {
			return nil
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return err
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if err := validateArchiveHeader(h); err != nil {
			return err
		}
	}
}

// verifyArchiveChecksum binds the archive's full length and bytes to its journal entry.
func verifyArchiveChecksum(a Archive) error {
	// Reject incompatible state before continuing the protected migration step.
	if a.Bytes < 1024 {
		return fmt.Errorf("backup archive is incomplete")
	}
	actual, err := hashFile(a.File)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if actual.SHA256 != a.SHA256 || actual.Bytes != a.Bytes {
		return fmt.Errorf("backup checksum mismatch: %s", a.File)
	}
	return nil
}

// validateArchiveHeader confines regular data and links to the restored filesystem.
func validateArchiveHeader(h *tar.Header) error {
	// Reject incompatible state before continuing the protected migration step.
	if !safeArchivePath(h.Name) {
		return fmt.Errorf("unsafe backup entry")
	}
	// Select only representations explicitly supported by the migration contract.
	switch h.Typeflag {
	// Keep this alternative within the validated operation or archive contract.
 case tar.TypeReg, tar.TypeGNUSparse, tar.TypeDir, tar.TypeXGlobalHeader:
		return nil
	// Keep this alternative within the validated operation or archive contract.
	case tar.TypeSymlink, tar.TypeLink:
		// Reject incompatible state before continuing the protected migration step.
		if !safeArchivePath(h.Linkname) {
			return fmt.Errorf("backup contains an escaping link")
		}
		return nil
	// Keep this alternative within the validated operation or archive contract.
	default:
		return fmt.Errorf("backup contains unsupported special file")
	}
}

// safeArchivePath keeps this migration step within its validated recovery boundary.
func safeArchivePath(p string) bool {
	// Reject incompatible state before continuing the protected migration step.
	if path.IsAbs(p) || strings.Contains(p, "\\") {
		return false
	}
	// Complete every selected item before advancing the migration boundary.
	for _, segment := range strings.Split(p, "/") {
		// Reject incompatible state before continuing the protected migration step.
		if segment == ".." {
			return false
		}
	}
	return true
}

// createArchive keeps this migration step within its validated recovery boundary.
func createArchive(ctx context.Context, source, destination string) (Archive, error) {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := safeAncestors(source); err != nil {
		return Archive{}, err
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return Archive{}, err
	}
	good := false
	// Keep the callback bound to the current migration and its checkpoint.
	defer func() {
		f.Close()
		// Reject incompatible state before continuing the protected migration step.
		if !good {
			os.Remove(destination)
		}
	}()
	cmd := exec.CommandContext(ctx, "tar", "--acls", "--xattrs", "--numeric-owner", "--sparse", "-cp", "-C", source, ".")
	cmd.Stdout = f
	// Preserve failure before advancing to the next external-effect boundary.
	if err := cmd.Run(); err != nil {
		return Archive{}, fmt.Errorf("cold volume archive failed: %w", err)
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := f.Sync(); err != nil {
		return Archive{}, err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := f.Close(); err != nil {
		return Archive{}, err
	}
	a, err := hashFile(destination)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return a, err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := verifyArchive(a); err != nil {
		return a, err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := syncDir(filepath.Dir(destination)); err != nil {
		return a, err
	}
	good = true
	return a, nil
}

// restoreArchive keeps this migration step within its validated recovery boundary.
func restoreArchive(ctx context.Context, a Archive, destination string, replace bool) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := verifyArchive(a); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := safeAncestors(destination); err != nil {
		return err
	}
	entries, err := os.ReadDir(destination)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if !replace && len(entries) > 0 {
		return fmt.Errorf("restore destination must be empty")
	}
	// Reject incompatible state before continuing the protected migration step.
	if replace {
		// Complete every selected item before advancing the migration boundary.
		for _, entry := range entries {
			// Preserve failure before advancing to the next external-effect boundary.
			if err := os.RemoveAll(filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
	}
	cmd := exec.CommandContext(ctx, "tar", "--acls", "--xattrs", "--numeric-owner", "--same-owner", "--same-permissions", "-xp", "-f", a.File, "-C", destination)
	// Preserve failure before advancing to the next external-effect boundary.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cold volume restore failed: %w", err)
	}
	// Flush data before handing a restored filesystem to another runtime.
	cmd = exec.CommandContext(ctx, "sync", "-f", destination)
	return cmd.Run()
}
