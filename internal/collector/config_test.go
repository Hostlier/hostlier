package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRenderContracts checks deployment shape, signal paths, and credential separation.
func TestRenderContracts(t *testing.T) {
	// Exercise both supported native telemetry paths.
	for _, runtime := range []string{"docker", "k3s"} {
		checkBundle(t, runtime)
	}
}

// checkBundle validates shared exporter guarantees before runtime-specific discovery checks.
func checkBundle(t *testing.T, runtime string) {
	t.Helper()
	b, err := Render(Options{Runtime: runtime, Endpoint: "https://hostlier.example.com/"})
	// Rendering must succeed for a canonical HTTPS origin.
	if err != nil {
		t.Fatal(err)
	}
	var config object
	// Inspect the serialized public configuration rather than private helper objects.
	if err = yaml.Unmarshal([]byte(b.Config), &config); err != nil {
		t.Fatal(err)
	}
	exporter := config["exporters"].(map[string]any)["otlp_http/hostlier"].(map[string]any)
	// These wire settings match the controller's deliberately narrow OTLP endpoint.
	if exporter["encoding"] != "json" || exporter["compression"] != "none" || exporter["logs_endpoint"] != "https://hostlier.example.com/v1/telemetry/otlp/logs" {
		t.Fatal(exporter)
	}
	contains(t, b.Config, "${env:HOSTLIER_TELEMETRY_TOKEN}", true)
	contains(t, b.Deployment, DefaultImage, true)
	contains(t, b.Config, "insecure_skip_verify", false)
	contains(t, b.Config, "4317", false)
	contains(t, b.Deployment, "hostNetwork:", false)
	checkRuntime(t, runtime, b)
	exportFixture(t, runtime, b)
}

// checkRuntime protects discovery and credential boundaries for each platform.
func checkRuntime(t *testing.T, runtime string, b Bundle) {
	t.Helper()
	// Kubernetes must schedule shared collectors without embedding a Secret.
	if runtime == "k3s" {
		contains(t, b.Deployment, "kind: DaemonSet", true)
		contains(t, b.Deployment, "secretKeyRef:", true)
		contains(t, b.Deployment, "kind: Secret", false)
		return
	}
	contains(t, b.Deployment, "collector.env", true)
	contains(t, b.Config, "port == 0", true)
	contains(t, b.Config, "container_id", true)
	contains(t, b.Config, `resource.attributes["hostlier.managed"] != "true"`, true)
}

// contains reports the specific security or compatibility invariant that changed.
func contains(t *testing.T, body, want string, present bool) {
	t.Helper()
	// A missing or unexpected fragment signals a public manifest regression.
	if strings.Contains(body, want) != present {
		t.Fatalf("expected presence=%v for %q", present, want)
	}
}

// exportFixture writes rendered files only when a live-validation operator opts in.
func exportFixture(t *testing.T, runtime string, b Bundle) {
	t.Helper()
	dir := os.Getenv("HOSTLIER_COLLECTOR_CONFIG_DIR")
	// Ordinary unit tests do not create runtime fixture files.
	if dir == "" {
		return
	}
	// Keep operator-generated fixture material private on disk.
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Both files are required to validate the actual deployment contract.
	for name, body := range map[string]string{runtime + ".yaml": b.Config, runtime + "-deployment.yaml": b.Deployment} {
		// Surface disk failures rather than testing stale fixture files.
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRenderRejectsUnsafeOptions rejects settings that bypass certificate or secret handling.
func TestRenderRejectsUnsafeOptions(t *testing.T) {
	// Each malformed input crosses a separate rendering boundary.
	for _, o := range []Options{
		{Runtime: "unknown", Endpoint: "https://hostlier.example.com"},
		{Runtime: "docker", Endpoint: "http://hostlier.example.com"},
		{Runtime: "docker", Endpoint: "https://user:password@hostlier.example.com"},
		{Runtime: "docker", Endpoint: "https://hostlier.example.com?token=x"},
		{Runtime: "docker", Endpoint: "https://hostlier.example.com/path"},
		{Runtime: "k3s", Endpoint: "https://hostlier.example.com", Namespace: "../invalid"},
		{Runtime: "docker", Endpoint: "https://hostlier.example.com", Image: "latest"},
		{Runtime: "docker", Endpoint: "https://hostlier.example.com", CAFile: "/tmp/ca.pem"},
	} {
		// Invalid options must be rejected before producing configuration.
		if _, err := Render(o); err == nil {
			t.Fatalf("accepted invalid options: %#v", o)
		}
	}
}
