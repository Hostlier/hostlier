package controllercmd

import (
	"encoding/json"
	"hostlier/internal/installer"
	"os"
	"path/filepath"
	"testing"
)

// TestExistingNginxSelectionSurvivesRestart preserves installed runtime selection.
func TestExistingNginxSelectionSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	data, _ := json.Marshal(installer.Installation{Proxy: "nginx-existing", Runtime: "docker", ControllerHostname: "hostlier.example.com"})
	// Restart settings must derive from the durable installation identity.
	if err := os.WriteFile(filepath.Join(dir, "installation.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s := session{flags: map[string]string{}}
	settings, err := s.controllerSettings(dir)
	// Both the proxy mode and runtime must survive without explicit daemon flags.
	if err != nil || settings.proxy != "nginx-existing" || settings.runtime != "docker" {
		t.Fatal(settings, err)
	}
}

// TestGatewaySelectionIsExplicitAndSurvivesSettings verifies the separate controller accepts the reviewed mode.
func TestGatewaySelectionIsExplicitAndSurvivesSettings(t *testing.T) {
	s := session{flags: map[string]string{"proxy": "nginx-gateway"}}
	proxy, err := s.selectedProxy("caddy")
	// An explicit choice must override the previous managed frontend without an implicit fallback.
	if err != nil || proxy != "nginx-gateway" {
		t.Fatal(proxy, err)
	}
	s.flags = map[string]string{}
	proxy, err = s.selectedProxy("nginx-gateway")
	// Restarting from a recorded installation retains the two-layer routing identity.
	if err != nil || proxy != "nginx-gateway" {
		t.Fatal(proxy, err)
	}
}
