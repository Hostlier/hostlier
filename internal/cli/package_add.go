package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"gopkg.in/yaml.v3"
	"hostlier/internal/config"
	"hostlier/internal/packageexec"
)

// addProjectPackage scaffolds the installed contract without planning or provisioning.
func (s *session) addProjectPackage() error {
	// Validate the local operation before discovery.
	if err := s.validatePackageAdd(); err != nil {
		return err
	}
	contract, err := s.enrollmentContract(s.target)
	// A broken installed executable must not be mistaken for an offline enrollment.
	if err != nil {
		return err
	}
	path := filepath.Join(s.cwd, ".hostlier")
	lock, err := lockProjectDirectory(s.cwd)
	// All project manifest read-modify-writes use the same lock across renames.
	if err != nil {
		return err
	}
	defer lock.Close()
	changed, err := enrollPackage(path, s.target, contract)
	// Report success only after the project file is published.
	if err != nil {
		return err
	}
	result := map[string]any{"package": s.target, "added": changed}
	// Advertise only the public names declared by the installed package.
	if names := contract.OutputNames(); len(names) > 0 {
		result["outputs"] = names
	}
	return s.print(result)
}

// enrollPackage preserves existing package settings and unrelated YAML, including comments.
func enrollPackage(path, name string, contract *packageContract) (bool, error) {
	original, err := readInitFile(path)
	// A first enrollment can create the project record without invoking init.
	if os.IsNotExist(err) {
		data, err := addPackageYAML([]byte("version: 2\n"), name, contract)
		// Refuse to create a file from an invalid contract.
		if err != nil {
			return false, err
		}
		return true, createInitFile(path, string(data))
	}
	// An unreadable manifest is never treated as an empty project.
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	// Capture the inode to detect replacement by another editor.
	if err != nil {
		return false, err
	}
	parsed, err := config.ReadProjectSources(path, "")
	// Validate ordinary deployment settings before changing the document.
	if err != nil {
		return false, err
	}
	// Package hooks require the v2 project contract.
	if parsed.Project.Version != 2 {
		return false, fmt.Errorf("package add requires a version 2 .hostlier")
	}
	updated, err := addPackageYAML(original, name, contract)
	// Do not stage a partially encoded project.
	if err != nil {
		return false, err
	}
	// Repeated adds with no missing fields preserve the exact file bytes.
	if bytes.Equal(updated, original) {
		return false, nil
	}
	return publishEnrollment(path, original, updated, info)
}

// publishEnrollment atomically replaces only the file that was read.
func publishEnrollment(path string, original, updated []byte, info os.FileInfo) (bool, error) {
	staged, err := stageInitSetup(path, string(updated), info.Mode().Perm())
	// Staging inherits the mode so secret references retain their original visibility.
	if err != nil {
		return false, err
	}
	defer os.Remove(staged)
	// An editor outside the CLI may have changed the source while staging.
	if !matchesInitSetup(path, string(original), info) {
		return false, fmt.Errorf(".hostlier changed during package add; retry")
	}
	return true, os.Rename(staged, path)
}

// addPackageYAML edits only the local package namespace after strict project parsing.
func addPackageYAML(original []byte, name string, contract *packageContract) ([]byte, error) {
	var doc yaml.Node
	// Enrollment edits the syntax tree so authored comments and secret references survive.
	if err := yaml.Unmarshal(original, &doc); err != nil || len(doc.Content) != 1 {
		return nil, fmt.Errorf("invalid .hostlier")
	}
	root := doc.Content[0]
	changed, err := scaffoldPackage(root, name, contract)
	// Keep the original formatting when an enrollment is already complete.
	if err != nil || !changed {
		return original, err
	}
	return encodePackageYAML(&doc)
}

// encodePackageYAML serializes a complete manifest within the project size limit.
func encodePackageYAML(doc *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	// Only complete YAML may reach the atomic publisher.
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	// Keep the manifest within the same limit used to read it.
	if out.Len() > 1024*1024 {
		return nil, fmt.Errorf("generated .hostlier exceeds 1 MiB")
	}
	return out.Bytes(), encoder.Close()
}

// enrollmentContract requires local installation before modifying a project manifest.
func (s *session) enrollmentContract(name string) (*packageContract, error) {
	runner, err := s.localPackageRunner(name)
	// Unsafe paths and unreadable installations must fail closed.
	if err != nil {
		return nil, err
	}
	cap, err := runner.Describe(s.ctx, name)
	// An installed package's invalid description should be fixed, not ignored.
	if err != nil {
		return nil, err
	}
	return compileContract(cap)
}

// validatePackageAdd accepts local discovery and output flags only.
func (s *session) validatePackageAdd() error {
	// Validate the name before it can become a YAML key.
	if len(s.parsed.args) != 3 || !packageexec.ValidName(s.target) {
		return fmt.Errorf("Usage: hostlier package add NAME")
	}
	// Enrollment is local even when a default remote profile exists.
	for flag := range s.flags {
		// Remote, install and build flags would imply actions outside enrollment.
		if flag != "json" && flag != "toon" && flag != "state-dir" {
			return fmt.Errorf("package add edits the current .hostlier; unsupported --%s", flag)
		}
	}
	return nil
}

// lockProjectDirectory serializes enrollment and explicit init without creating a lock file.
func lockProjectDirectory(root string) (*os.File, error) {
	dir, err := os.Open(root)
	// A missing project cannot host a manifest transaction.
	if err != nil {
		return nil, err
	}
	// Closing the descriptor releases the lock even if publishing fails.
	if err = syscall.Flock(int(dir.Fd()), syscall.LOCK_EX); err != nil {
		dir.Close()
		return nil, err
	}
	return dir, nil
}
