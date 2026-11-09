package installer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"hostlier/internal/collectorimage"
	"hostlier/internal/model"
)

// CollectorSource locates release assets beside the executable, reusing installed assets on retry.
func CollectorSource(executable, stateDir string) string {
	sibling := filepath.Join(filepath.Dir(executable), "collector-image")
	// A supplied release takes precedence so upgrades install its new image.
	if _, err := os.Lstat(sibling); err == nil {
		return sibling
	}
	installed := filepath.Join(stateDir, "collector-image")
	// A normal installed controller can repair its installation without the original release folder.
	if _, err := os.Lstat(installed); err == nil {
		return installed
	}
	return sibling
}

// inspectCollector binds the entire immutable multi-platform image into installation approval.
func inspectCollector(p *Plan) {
	// Programmatic callers receive the same release discovery as the CLI.
	if p.CollectorSource == "" {
		executable, err := os.Executable()
		// Without a source executable the installer cannot discover a release bundle.
		if err != nil {
			p.Errors = append(p.Errors, "Cannot locate collector release assets")
			return
		}
		p.CollectorSource = CollectorSource(executable, p.StateDir)
	}
	image, err := collectorimage.Open(p.CollectorSource)
	// A bare controller binary cannot promise automatic collection without its verified image assets.
	if err != nil {
		p.Errors = append(p.Errors, fmt.Sprintf("Bundled minimal collector image at %s is missing or invalid: %v. Re-extract the checksum-verified Hostlier server bundle.", p.CollectorSource, err))
		return
	}
	defer image.Close()
	p.Snapshot.CollectorDigest = image.Digest()
}

// collectorImage installs the reviewed image before any runtime or controller service starts.
func (a *installationRunner) collectorImage() error {
	return installCollectorImage(a.plan.CollectorSource, filepath.Join(a.plan.StateDir, "collector-image"), a.plan.Snapshot.CollectorDigest)
}

// installCollectorImage stages verified content before atomically switching the visible image index.
func installCollectorImage(source, target, expected string) error {
	image, err := collectorimage.Open(source)
	// Revalidate the reviewed digest immediately before copying executable image data.
	if err != nil {
		return err
	}
	defer image.Close()
	// Empty or changed approvals cannot authorize a different collector executable.
	if expected == "" || image.Digest() != expected {
		return model.Err("STALE_PLAN", "Collector image changed after inspection")
	}
	sourceAbs, _ := filepath.Abs(source)
	targetAbs, _ := filepath.Abs(target)
	// An installed retry needs no destructive rewrite of its own source directory.
	if sourceAbs == targetAbs {
		return nil
	}
	stage, err := os.MkdirTemp(filepath.Dir(target), ".collector-image-")
	// Staging remains on the same filesystem as the final atomic index replacement.
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	// Copy only validated OCI references; arbitrary files from a release are never installed.
	for _, relative := range image.Files() {
		// Any incomplete copy invalidates the complete staged image.
		if err = copyCollectorFile(source, stage, relative); err != nil {
			return err
		}
	}
	verified, err := collectorimage.Open(stage)
	// Detect source replacement races and incomplete copies before publishing the image.
	if err != nil {
		return err
	}
	digest := verified.Digest()
	verified.Close()
	// The staged result must still match the approved immutable index.
	if digest != expected {
		return model.Err("STALE_PLAN", "Collector image changed while staging")
	}
	return activateCollectorImage(stage, target, image.Files())
}

// copyCollectorFile bounds release-file reads and creates private staging files exclusively.
func copyCollectorFile(source, target, relative string) error {
	// Defense in depth: callers may only supply clean relative OCI filenames.
	if !safeCollectorRelative(relative) {
		return errors.New("invalid collector image path")
	}
	destination := filepath.Join(target, relative)
	// Every staging parent is created inside the owner-only temporary directory.
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	in, err := os.OpenFile(filepath.Join(source, relative), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	// Missing release data cannot be replaced by an empty file.
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	// Source changes cannot turn a regular release blob into a blocking device or pipe.
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("collector image source must be regular")
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	// Exclusive creation prevents accidental reuse of staged file names.
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(in, (256<<20)+1))
	// One collector blob cannot consume unbounded staging space.
	if err != nil || n > 256<<20 {
		return errors.New("collector image copy failed or exceeded size limit")
	}
	return out.Sync()
}

// activateCollectorImage keeps old immutable blobs for concurrent pulls and publishes index last.
func activateCollectorImage(stage, target string, files []string) error {
	// Refuse redirecting persistent image storage through symlinked directories.
	for _, dir := range []string{target, filepath.Join(target, "blobs"), filepath.Join(target, "blobs", "sha256")} {
		// Directory ownership is established before moving any release content.
		if err := collectorDirectory(dir); err != nil {
			return err
		}
	}
	// Immutable blobs can be safely introduced before readers see the new index.
	for _, relative := range files {
		// The root index is the sole activation pointer and is replaced last.
		if relative == "index.json" {
			continue
		}
		// Same-filesystem rename never follows an existing destination file symlink.
		if err := os.Rename(filepath.Join(stage, relative), filepath.Join(target, relative)); err != nil {
			return err
		}
	}
	// Sync the blob directory before making the new index observable.
	if err := syncCollectorDirectory(filepath.Join(target, "blobs", "sha256")); err != nil {
		return err
	}
	// Atomic index publication preserves the previous valid release on earlier failures.
	if err := os.Rename(filepath.Join(stage, "index.json"), filepath.Join(target, "index.json")); err != nil {
		return err
	}
	return syncCollectorDirectory(target)
}

// collectorDirectory refuses symlinks and restricts release metadata to local administration.
func collectorDirectory(path string) error {
	info, err := os.Lstat(path)
	// Newly installed state gets an explicitly private directory.
	if os.IsNotExist(err) {
		return os.Mkdir(path, 0700)
	}
	// Existing non-directory entries are never adopted as image storage.
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("collector image storage must be a directory")
	}
	return os.Chmod(path, 0700)
}

// syncCollectorDirectory makes image activation durable across a host restart.
func syncCollectorDirectory(path string) error {
	dir, err := os.Open(path)
	// A directory-open error prevents claiming durability.
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// safeCollectorRelative confines release copies to clean relative OCI references.
func safeCollectorRelative(path string) bool {
	return !filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.HasPrefix(path, "..")
}
