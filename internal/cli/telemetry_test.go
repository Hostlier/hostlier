package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTelemetryRenderIsOffline verifies rendering needs no controller or credential and never overwrites files.
func TestTelemetryRenderIsOffline(t *testing.T) {
	dir := t.TempDir()
	args := []string{"telemetry", "render", "--runtime", "k3s", "--url", "https://hostlier.example.com", "--output", "collector"}
	var out bytes.Buffer
	err := RunWithOptions(context.Background(), args, strings.NewReader(""), &out, &out, Options{CWD: dir, Home: filepath.Join(dir, "no-profile")})
	// Offline rendering must succeed without an enrolled controller.
	if err != nil {
		t.Fatal(err)
	}
	// Both generated artifacts must remain private to the invoking user.
	for _, name := range []string{"collector.yaml", "deployment.yaml"} {
		file, err := os.Stat(filepath.Join(dir, "collector", name))
		// Missing or world-readable deployment files violate the output contract.
		if err != nil || file.Mode().Perm() != 0600 {
			t.Fatalf("private rendered file %s: %v", name, err)
		}
	}
	// A second rendering cannot overwrite a previously reviewed bundle.
	if err = RunWithOptions(context.Background(), args, strings.NewReader(""), &out, &out, Options{CWD: dir}); err == nil {
		t.Fatal("render overwrote existing directory")
	}
}

// TestTelemetryRenderRejectsInsecureEndpoint prevents accidental credential transport over plaintext HTTP.
func TestTelemetryRenderRejectsInsecureEndpoint(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	args := []string{"telemetry", "render", "--runtime", "docker", "--url", "http://hostlier.example.com", "--output", "collector"}
	// Collector credentials must never be rendered for plaintext transport.
	if err := RunWithOptions(context.Background(), args, strings.NewReader(""), &out, &out, Options{CWD: dir}); err == nil {
		t.Fatal("accepted insecure collector endpoint")
	}
	// Invalid endpoint validation must precede all filesystem changes.
	if _, err := os.Stat(filepath.Join(dir, "collector")); !os.IsNotExist(err) {
		t.Fatal("invalid render created files")
	}
}

// TestTelemetryRenderCustomImage verifies the image override reaches both deployment formats offline.
func TestTelemetryRenderCustomImage(t *testing.T) {
	// The same minimal image supports Docker hosts and Kubernetes nodes.
	for _, runtime := range []string{"docker", "k3s"} {
		// Each platform gets a separate output directory and no controller profile.
		t.Run(runtime, func(t *testing.T) {
			dir := t.TempDir()
			selected := "registry.example.com/hostlier/collector:reviewed"
			args := []string{"telemetry", "render", "--runtime", runtime, "--url", "https://hostlier.example.com", "--output", "collector", "--image", selected}
			var out bytes.Buffer
			// Successful rendering must not require an installed registry image or network access.
			if err := RunWithOptions(context.Background(), args, strings.NewReader(""), &out, &out, Options{CWD: dir, Home: filepath.Join(dir, "no-profile")}); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(dir, "collector", "deployment.yaml"))
			// Inspect serialized deployment output so a dropped CLI flag cannot pass unnoticed.
			if err != nil || !strings.Contains(string(body), "image: "+selected) {
				t.Fatalf("selected image missing: %v %s", err, body)
			}
		})
	}
}
