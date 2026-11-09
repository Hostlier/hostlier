package protected

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Bundle pins every executable and source file in an administrator-installed directory.
type Bundle struct {
	Root     string
	Platform string
	Files    map[string]string
}

// Validate rejects unpinned, redirected, writable or changed bundle contents.
func (b Bundle) Validate() error {
	// Keep private preparation within its bounded validated contract.
	if b.Platform != runtime.GOOS+"/"+runtime.GOARCH || len(b.Files) == 0 {
		return errors.New("unsupported tool bundle")
	}
	info, err := os.Lstat(b.Root)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe bundle root")
	}
	resolved, err := filepath.EvalSymlinks(b.Root)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil || resolved != b.Root {
		return errors.New("redirected bundle root")
	}
	// Stop before publishing an incomplete or invalid preparation.
	if err := b.verifyManifest(); err != nil {
		return err
	}
	return filepath.WalkDir(b.Root, b.validateEntry)
}

// fileHash bounds individual pinned files while computing their content identity.
func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, 256*1024*1024+1))
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil || n > 256*1024*1024 {
		return "", errors.New("bundle file too large")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Run executes a pinned tool directly with explicit argv and scoped credentials.
func (b Bundle) Run(ctx context.Context, tool string, args []string, workspace string, environment map[string]string) ([]byte, error) {
	// Stop before publishing an incomplete or invalid preparation.
	if err := b.Validate(); err != nil {
		return nil, err
	}
	// Keep private preparation within its bounded validated contract.
	if _, ok := b.Files[tool]; !ok || filepath.IsAbs(tool) || strings.Contains(tool, "..") {
		return nil, errors.New("untracked tool")
	}
	// Stop before publishing an incomplete or invalid preparation.
	if err := safeDirectory(workspace); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, filepath.Join(b.Root, tool), args...)
	command.Dir = workspace
	command.Env = []string{"LANG=C", "USER=hostlier", "HOME=" + workspace, "PATH=" + b.Root}
	command.WaitDelay = time.Second
	// Stop before publishing an incomplete or invalid preparation.
	if err := appendEnvironment(command, environment); err != nil {
		return nil, err
	}
	output := &boundedBuffer{}
	command.Stdout = output
	command.Stderr = io.Discard
	// Keep private preparation within its bounded validated contract.
	if command.Run() != nil || output.overflow {
		return nil, errors.New("tool operation failed")
	}
	return output.data, nil
}

// allowedEnvironment restricts transient credentials to explicit provider namespaces.
func allowedEnvironment(name string) bool {
	// Trusted adapters derive pinned Go discovery paths and disable runtime downloads.
	if name == "GOROOT" || name == "GOTOOLCHAIN" || name == "GOPROXY" || name == "GOSUMDB" || name == "GOENV" {
		return true
	}
	// Check each declared identity against the explicit contract.
	for _, prefix := range []string{"AWS_", "AZURE_", "ARM_", "GOOGLE_", "PULUMI_", "TF_VAR_"} {
		// Keep private preparation within its bounded validated contract.
		if strings.HasPrefix(name, prefix) && !strings.ContainsAny(name, "=\x00") {
			return true
		}
	}
	return name == "GOOGLE_APPLICATION_CREDENTIALS"
}

// boundedBuffer drains tool output without unbounded allocation or protocol leakage.
type boundedBuffer struct {
	data     []byte
	overflow bool
}

// Write retains bounded private tool output while draining excess bytes.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 4*1024*1024 - len(b.data)
	// Keep private preparation within its bounded validated contract.
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}

// verifyManifest requires every declared file to exist with the expected digest.
func (b Bundle) verifyManifest() error {
	// Check each declared identity against the explicit contract.
	for name, expected := range b.Files {
		// Keep private preparation within its bounded validated contract.
		if filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || name == "." || strings.HasPrefix(name, "../") {
			return errors.New("invalid bundle path")
		}
		sum, err := fileHash(filepath.Join(b.Root, name))
		// Stop before publishing an incomplete or invalid preparation.
		if err != nil || sum != expected {
			return errors.New("missing or changed bundle file")
		}
	}
	return nil
}

// Digest returns a stable identity for the reviewed platform and complete file manifest.
func (b Bundle) Digest() (string, error) {
	// Stop before publishing an incomplete or invalid preparation.
	if err := b.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Platform string
		Files    map[string]string
	}{b.Platform, b.Files})
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Materialize copies pinned source files into a fresh protected disposable workspace.
func (b Bundle) Materialize(workspace string) error {
	// Stop before publishing an incomplete or invalid preparation.
	if err := b.Validate(); err != nil {
		return err
	}
	// Stop before publishing an incomplete or invalid preparation.
	if err := safeDirectory(workspace); err != nil {
		return err
	}
	// Check each declared identity against the explicit contract.
	for _, name := range sortedNames(b.Files) {
		// Stop before publishing an incomplete or invalid preparation.
		if err := b.copyFile(name, workspace); err != nil {
			return err
		}
	}
	return nil
}

// copyFile uses exclusive creation so existing workspace entries cannot redirect writes.
func (b Bundle) copyFile(name, workspace string) error {
	path := filepath.Join(workspace, filepath.FromSlash(name))
	// Stop before publishing an incomplete or invalid preparation.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Stop before publishing an incomplete or invalid preparation.
	if err := safeDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(b.Root, filepath.FromSlash(name)))
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return err
	}
	source, err := os.Stat(filepath.Join(b.Root, filepath.FromSlash(name)))
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return err
	}
	// Stop before publishing an incomplete or invalid preparation.
	if err := exclusiveWrite(path, data); err != nil {
		return err
	}
	// Only the owner execute bit is retained in the private workspace.
	return os.Chmod(path, 0600|source.Mode().Perm()&0100)
}

// validateEntry rejects redirection and unexpected files while walking pinned content.
func (b Bundle) validateEntry(path string, entry os.DirEntry, walkErr error) error {
	// Keep private preparation within its bounded validated contract.
	if walkErr != nil {
		return walkErr
	}
	// Keep private preparation within its bounded validated contract.
	if path == b.Root {
		return nil
	}
	info, err := entry.Info()
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return err
	}
	// Keep private preparation within its bounded validated contract.
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe bundle entry")
	}
	// Keep private preparation within its bounded validated contract.
	if entry.IsDir() {
		return nil
	}
	return b.validateFile(path, info)
}

// validateFile verifies one regular manifest entry against its pinned content identity.
func (b Bundle) validateFile(path string, info os.FileInfo) error {
	relative, err := filepath.Rel(b.Root, path)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return err
	}
	expected, ok := b.Files[filepath.ToSlash(relative)]
	// Keep private preparation within its bounded validated contract.
	if !ok || !info.Mode().IsRegular() {
		return errors.New("untracked bundle file")
	}
	sum, err := fileHash(path)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil || sum != expected {
		return errors.New("bundle content changed")
	}
	return nil
}

// appendEnvironment admits only scoped provider credentials into a clean process environment.
func appendEnvironment(command *exec.Cmd, environment map[string]string) error {
	// Check each declared identity against the explicit contract.
	for _, name := range sortedNames(environment) {
		value := environment[name]
		// Keep private preparation within its bounded validated contract.
		if !allowedEnvironment(name) || strings.ContainsRune(value, 0) {
			return errors.New("environment variable not allowed")
		}
		command.Env = append(command.Env, name+"="+value)
	}
	return nil
}

// sortedNames makes materialization and process environments reproducible.
func sortedNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	// Check each declared identity against the explicit contract.
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
