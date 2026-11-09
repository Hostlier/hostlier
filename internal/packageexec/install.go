package packageexec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Installation identifies installed executable bytes without exposing package configuration.
type Installation struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// Install atomically activates one administrator-selected optional executable.
func (r Runner) Install(name, source string) (Installation, error) {
	// Safe names cannot overwrite executor configuration or escape the package directory.
	if !packageName.MatchString(name) {
		return Installation{}, errors.New("invalid package name")
	}
	data, err := sourceBinary(source)
	// Reject unsafe artifacts before creating installation directories.
	if err != nil {
		return Installation{}, err
	}
	// Validate the complete installation ancestry before writing executable code.
	if err = prepareDirectory(r.Directory); err != nil {
		return Installation{}, err
	}
	// Existing redirected or writable executable targets are never accepted.
	if err = r.destination(name); err != nil {
		return Installation{}, err
	}
	return r.installBytes(name, data)
}

// sourceBinary reads a bounded regular executable without following a source symlink.
func sourceBinary(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	// Administrator-selected input cannot redirect through a symlink or shared writable file.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 {
		return nil, errors.New("missing or unsafe package binary")
	}
	file, err := os.Open(path)
	// Keep path and executable contents out of installation error messages.
	if err != nil {
		return nil, errors.New("cannot open package binary")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 128*1024*1024+1))
	// Empty and oversized artifacts cannot become installed packages.
	if err != nil || len(data) == 0 || len(data) > 128*1024*1024 {
		return nil, errors.New("invalid package binary")
	}
	return data, nil
}

// prepareDirectory creates private package storage only below a safe existing ancestor.
func prepareDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	// Path resolution must complete before inspecting or creating directories.
	if err != nil {
		return err
	}
	// Existing ancestry must not redirect or permit replacement of trusted package files.
	if err = checkAncestry(absolute, true); err != nil {
		return err
	}
	return os.MkdirAll(absolute, 0700)
}

// checkAncestry rejects redirected directories and shared writable installation paths.
func checkAncestry(path string, private bool) error {
	info, err := os.Lstat(path)
	// Missing children may be created after their nearest existing ancestor validates.
	if os.IsNotExist(err) {
		return checkAncestry(filepath.Dir(path), false)
	}
	// Sticky ancestors permit private temporary test installations without replaceable parent entries.
	if err != nil || !info.IsDir() || (info.Mode().Perm()&0022 != 0 && (private || info.Mode()&os.ModeSticky == 0)) {
		return errors.New("unsafe package installation path")
	}
	// Filesystem root terminates ancestry validation.
	if filepath.Dir(path) == path {
		return nil
	}
	return checkAncestry(filepath.Dir(path), false)
}

// installBytes stages complete executable bytes before atomic activation.
func (r Runner) installBytes(name string, data []byte) (Installation, error) {
	path, cleanup, err := r.stage(data)
	// No installation is changed when staging fails.
	if err != nil {
		return Installation{}, err
	}
	defer cleanup()
	// Persist staged bytes before publishing the new executable identity.
	if err = syncFile(path); err != nil {
		return Installation{}, err
	}
	// Atomic rename publishes the complete validated executable without partial reads.
	if err = os.Rename(path, filepath.Join(r.Directory, name)); err != nil {
		return Installation{}, errors.New("cannot activate package")
	}
	return identity(name, data), syncFile(r.Directory)
}

// syncFile makes a staged executable or containing directory durable.
func syncFile(path string) error {
	file, err := os.Open(path)
	// Durability cannot be confirmed when the staged artifact is unavailable.
	if err != nil {
		return errors.New("cannot sync package installation")
	}
	defer file.Close()
	return file.Sync()
}

// identity hashes the exact executable bytes selected for activation.
func identity(name string, data []byte) Installation {
	sum := sha256.Sum256(data)
	return Installation{Name: name, Digest: hex.EncodeToString(sum[:])}
}

// Installed lists validated executable identities and ignores non-executable configuration artifacts.
func (r Runner) Installed() ([]Installation, error) {
	entries, err := os.ReadDir(r.Directory)
	// An uncreated package directory is an empty optional installation.
	if os.IsNotExist(err) {
		return []Installation{}, nil
	}
	// Unreadable directories cannot supply trustworthy installation information.
	if err != nil {
		return nil, errors.New("cannot list package installations")
	}
	return r.identities(entries)
}

// identities measures only validated package executables in deterministic filename order.
func (r Runner) identities(entries []os.DirEntry) ([]Installation, error) {
	result := []Installation{}
	// Configuration, staging and hidden files are not executable package identities.
	for _, entry := range entries {
		// Package names exclude JSON executor files and private staging names.
		if !packageName.MatchString(entry.Name()) {
			continue
		}
		data, err := r.readPackage(entry.Name())
		// Unsafe executable entries are visible failures rather than trusted installed code.
		if err != nil {
			return nil, err
		}
		result = append(result, identity(entry.Name(), data))
	}
	return result, nil
}

// destination rejects unsafe existing targets before atomic replacement.
func (r Runner) destination(name string) error {
	_, err := os.Lstat(filepath.Join(r.Directory, name))
	// New package identities have no existing target to validate.
	if os.IsNotExist(err) {
		return nil
	}
	_, err = r.packagePath(name)
	return err
}
