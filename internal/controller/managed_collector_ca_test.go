package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/collectorimage"
	"hostlier/internal/model"
)

// collectorCAProbe captures deployment trust without starting any host processes.
type collectorCAProbe struct {
	recordingRuntime
	deployment model.CollectorDeployment
}

// EnsureCollector records the controller-provided CA separately from the renderer's public YAML contract.
func (r *collectorCAProbe) EnsureCollector(_ context.Context, deployment model.CollectorDeployment) error {
	r.deployment = deployment
	return nil
}

// TestManagedCollectorCarriesPrivateCAOutsideRenderer protects collector startup when the controller uses a private certificate authority.
func TestManagedCollectorCarriesPrivateCAOutsideRenderer(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "collector")
	ca := filepath.Join(root, "ca.pem")
	// Build harmless immutable image fixtures; this test never executes their contents.
	for path, body := range map[string]string{binary: "fixture executable", ca: "fixture public CA"} {
		// Any failed fixture write prevents meaningful trust-propagation verification.
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	err := collectorimage.Build(filepath.Join(root, "collector-image"), []collectorimage.PlatformInput{{Architecture: "arm64", BinaryPath: binary, CAPath: ca}})
	// The controller opens the same verified image layout used by production reconciliation.
	if err != nil {
		t.Fatal(err)
	}
	runtime := &collectorCAProbe{}
	c, err := New(Options{ManagedCollector: true, CollectorEndpoint: "https://controller.example.com", CollectorCAFile: ca, StateDir: root, Runtime: runtime, Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}})
	// Controller initialization must succeed before attempting its optional collector worker.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeController(t, c) }) // Close the test's immutable descriptors and private store after every outcome.
	// Rendering rejects ad-hoc CA options; the runtime instead installs the reviewed exporter CA and mount.
	if err = c.reconcileManagedCollector(context.Background()); err != nil {
		t.Fatal(err)
	}
	deployment := runtime.deployment
	// Trust must survive the controller-to-runtime handoff without disabling certificate verification.
	if deployment.CAFile != ca || !strings.Contains(deployment.Image, "controller.example.com/hostlier/collector@sha256:") {
		t.Fatal("collector trust or image origin was lost")
	}
}
