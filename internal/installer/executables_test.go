package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallControllerOnly ensures the server bundle needs no client binary beside it.
func TestInstallControllerOnly(t *testing.T) {
	directory := t.TempDir()
	source := t.TempDir()
	runner := installationRunner{plan: Plan{Options: defaults(Options{Executable: filepath.Join(directory, "hostlier-controller")})}, options: ApplyOptions{SourceExecutable: filepath.Join(source, "hostlier-controller")}}
	if err := os.WriteFile(filepath.Join(source, "hostlier-controller"), []byte("controller"), 0755); err != nil {
		t.Fatal(err)
	}
	// Installation must succeed without a sibling CLI in the release directory.
	if err := runner.installExecutable(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "hostlier-controller")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "controller" {
		t.Fatalf("controller: %q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("permissions: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "hostlier")); !os.IsNotExist(err) {
		t.Fatalf("controller installer created a CLI: %v", err)
	}
}

// TestServiceStartsController verifies the generated unit selects the standalone daemon entry point.
func TestServiceStartsController(t *testing.T) {
	assets, err := InstallationAssets(Options{Domain: "example.com"})
	// The system service must never depend on CLI command dispatch.
	if err != nil || !strings.Contains(assets.Service, "ExecStart=/usr/local/bin/hostlier-controller run ") {
		t.Fatal(assets.Service, err)
	}
}

// TestControllerDestinationMustNotReplaceCLI protects an existing client executable.
func TestControllerDestinationMustNotReplaceCLI(t *testing.T) {
	_, err := InstallationAssets(Options{Domain: "example.com", Executable: "/usr/local/bin/hostlier"})
	// A custom controller destination cannot overwrite an existing client binary.
	if err == nil {
		t.Fatal("CLI destination accepted for controller")
	}
}
