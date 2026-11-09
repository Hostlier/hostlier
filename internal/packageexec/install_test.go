package packageexec

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInstallAndListMeasureActivatedBytes checks replacement and independent executable identities.
func TestInstallAndListMeasureActivatedBytes(t *testing.T) {
	dir, source := installFixture(t)
	runner := Runner{Directory: filepath.Join(dir, "state", "packages")}
	item, err := runner.Install("postgres", source)
	// The installer must report the exact activated executable digest.
	if err != nil || item.Digest != identity("postgres", []byte("#!/bin/sh\nexit 0\n")).Digest {
		t.Fatal(item, err)
	}
	// Executor configuration files are not executable package identities.
	if err = os.WriteFile(filepath.Join(runner.Directory, "aiven.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := runner.Installed()
	// Listings contain only validated executable files in filename order.
	if err != nil || len(items) != 1 || items[0] != item {
		t.Fatal(items, err)
	}
	_, digest, cleanup, err := runner.Snapshot("postgres", item.Digest)
	defer cleanup()
	// Installed bytes must work with the production snapshot verifier.
	if err != nil || digest != item.Digest {
		t.Fatal(digest, err)
	}
}

// TestInstallerRejectsRedirectedAndSharedPaths tests the executable installation trust boundary.
func TestInstallerRejectsRedirectedAndSharedPaths(t *testing.T) {
	dir, source := installFixture(t)
	target := filepath.Join(dir, "packages")
	// A valid private directory establishes the target for subsequent unsafe-path checks.
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	// Redirected package directories must never receive installed executable bytes.
	if err := os.Symlink(target, filepath.Join(dir, "redirect")); err != nil {
		t.Fatal(err)
	}
	_, err := (Runner{Directory: filepath.Join(dir, "redirect")}).Install("postgres", source)
	// Directory symlinks cannot select a trusted installation root.
	if err == nil {
		t.Fatal("redirected installation accepted")
	}
	// Shared writable package storage cannot hold trusted executable bytes.
	if err = os.Chmod(target, 0777); err != nil {
		t.Fatal(err)
	}
	_, err = (Runner{Directory: target}).Install("postgres", source)
	// Writable destinations must fail before staging.
	if err == nil {
		t.Fatal("shared writable directory accepted")
	}
}

// TestInstallerRejectsUnsafeNamesAndSources prevents traversal and source redirection.
func TestInstallerRejectsUnsafeNamesAndSources(t *testing.T) {
	dir, source := installFixture(t)
	runner := Runner{Directory: filepath.Join(dir, "packages")}
	_, err := runner.Install("../escape", source)
	// Package names cannot escape the protected destination directory.
	if err == nil {
		t.Fatal("traversal name accepted")
	}
	redirect := filepath.Join(dir, "source-link")
	// A source symlink cannot redirect the administrator's selected artifact.
	if err = os.Symlink(source, redirect); err != nil {
		t.Fatal(err)
	}
	_, err = runner.Install("postgres", redirect)
	// Symlink source artifacts must fail before installation begins.
	if err == nil {
		t.Fatal("source symlink accepted")
	}
}

// TestInstallerRefusesTargetSymlink prevents activating through existing redirected identities.
func TestInstallerRefusesTargetSymlink(t *testing.T) {
	dir, source := installFixture(t)
	runner := Runner{Directory: filepath.Join(dir, "packages")}
	// Prepare a secure directory containing an intentionally redirected package identity.
	if err := os.Mkdir(runner.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	// The external source must remain separate from any installed identity.
	if err := os.Symlink(source, filepath.Join(runner.Directory, "postgres")); err != nil {
		t.Fatal(err)
	}
	_, err := runner.Install("postgres", source)
	// Administrators must remove unsafe entries before installing trusted executable code.
	if err == nil {
		t.Fatal("target symlink accepted")
	}
}

// installFixture creates a private regular executable under a canonical directory path.
func installFixture(t *testing.T) (string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	// Canonical paths avoid operating-system aliases such as macOS /var during ancestry tests.
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "binary")
	// This fixture exercises installation only and does not provision an engine.
	if err = os.WriteFile(source, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return dir, source
}
