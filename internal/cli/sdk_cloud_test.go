package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBundledCloudSDKInstall proves the local command never needs npm, Go module fetching or a controller.
func TestBundledCloudSDKInstall(t *testing.T) {
	project := t.TempDir()
	options := Options{CWD: project, Home: t.TempDir(), InstallSDK: func(_ context.Context, _, _, _ string) error {
		t.Fatal("cloud installation called the Go package installer")
		return nil
	}}
	// The command can be repeated to upgrade an earlier CLI-bundled client.
	for i := 0; i < 2; i++ {
		out, err := invoke([]string{"sdk", "install", "cloud", "--directory", project}, "", options)
		if err != nil || !strings.Contains(out, "@hostlier/cloud-sdk") {
			t.Fatalf("install %d: %v %s", i, err, out)
		}
	}
	dir := filepath.Join(project, "node_modules", "@hostlier", "cloud-sdk")
	for _, name := range cloudSDKFiles {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("missing bundled %s: %v", name, err)
		}
	}
}

// TestBundledCloudSDKInstallRejectsConflicts preserves an existing package owned by another tool.
func TestBundledCloudSDKInstallRejectsConflicts(t *testing.T) {
	project := t.TempDir()
	scope := filepath.Join(project, "node_modules", "@hostlier")
	if err := os.MkdirAll(scope, 0755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(scope, "cloud-sdk")
	if err := os.Mkdir(destination, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "package.json"), []byte(`{"name":"@hostlier/cloud-sdk"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke([]string{"sdk", "install", "cloud"}, "", Options{CWD: project, Home: t.TempDir()}); err == nil {
		t.Fatal("overwrote a package managed outside Hostlier")
	}
	if _, err := invoke([]string{"sdk", "install", "cloud", "--version", "v1"}, "", Options{CWD: project, Home: t.TempDir()}); err == nil {
		t.Fatal("accepted conflicting Go SDK version")
	}
}

// TestBundledCloudGoSDKInstall copies the Go wrapper and builds the installed sources without fetching dependencies.
func TestBundledCloudGoSDKInstall(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module example.com/backend\n\ngo 1.26.0\n\nrequire connectrpc.com/connect v1.12.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err := invoke([]string{"sdk", "install", "cloud-go", "--directory", project}, "", Options{CWD: project, Home: t.TempDir()})
		if err != nil || !strings.Contains(out, "example.com/backend/hostliercloud") {
			t.Fatalf("install %d: %v %s", i, err, out)
		}
	}
	if source, err := os.ReadFile(filepath.Join(project, "hostliercloud", "cloudsdk.go")); err != nil || !strings.Contains(string(source), "package cloudsdk") {
		t.Fatal("Go SDK source was not installed", err)
	}
	if source, err := os.ReadFile(filepath.Join(project, "hostliercloud", "api.go")); err != nil || !strings.Contains(string(source), "newAPIRunner") {
		t.Fatal("direct Go controller API transport was not installed", err)
	}
	if mod, err := os.ReadFile(filepath.Join(project, "go.mod")); err != nil || !strings.Contains(string(mod), "gopkg.in/yaml.v3 v3.0.1") {
		t.Fatal("Go YAML dependency was not declared", err)
	}
	if sums, err := os.ReadFile(filepath.Join(project, "go.sum")); err != nil || !strings.Contains(string(sums), "gopkg.in/yaml.v3 v3.0.1 h1:") {
		t.Fatal("Go YAML dependency was not pinned", err)
	}
	cmd := exec.Command("go", "build", "-mod=readonly", "./hostliercloud/...")
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installed SDK did not build: %v: %s", err, output)
	}

}

// TestBundledCloudGoSDKRejectsUnownedPackage avoids overwriting the caller's own Go code.
func TestBundledCloudGoSDKRejectsUnownedPackage(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module example.com/backend\n\ngo 1.26.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(project, "hostliercloud"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke([]string{"sdk", "install", "cloud-go"}, "", Options{CWD: project, Home: t.TempDir()}); err == nil {
		t.Fatal("overwrote an unowned Go package")
	}
}
