package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"hostlier/internal/packageexec"
	"os"
	"path/filepath"
	"testing"
)

// TestLocalPackageInstallAndListRequiresNoController verifies offline administrator package commands.
func TestLocalPackageInstallAndListRequiresNoController(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	// Canonical temporary paths satisfy the installer's ancestry protection.
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "engine")
	// A fixture executable is sufficient to verify local command installation semantics.
	if err = os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(directory, "state")
	var output bytes.Buffer
	err = RunWithOptions(context.Background(), []string{"package", "install", "postgres", "--file", binary, "--state-dir", state}, bytes.NewReader(nil), &output, &output, Options{Home: directory})
	// No controller server or interactive approval is needed for explicit local installation.
	if err != nil {
		t.Fatal(err)
	}
	checkInstalledList(t, directory, state)
}

// checkInstalledList confirms installed digests are readable without a controller profile.
func checkInstalledList(t *testing.T, home, state string) {
	t.Helper()
	var output bytes.Buffer
	err := RunWithOptions(context.Background(), []string{"package", "list", "--state-dir", state}, bytes.NewReader(nil), &output, &output, Options{Home: home})
	// The listing command runs before authenticated remote-command setup.
	if err != nil {
		t.Fatal(err)
	}
	var items []packageexec.Installation
	// A measured SHA-256 identity is returned for the installed executable.
	if json.Unmarshal(output.Bytes(), &items) != nil || len(items) != 1 || items[0].Name != "postgres" || len(items[0].Digest) != 64 {
		t.Fatal(output.String())
	}
}
