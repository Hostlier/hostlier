package installer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAutomaticInstallationChoices checks independent detection and explicit k3s selection across host layouts.
func TestAutomaticInstallationChoices(t *testing.T) {
	// Each case names the expected runtime ownership and edge without installing anything.
	cases := []struct {
		name, runtime, proxy               string
		docker, nginx                      bool
		wantRuntime, wantProxy, wantDocker string
	}{
		{"empty", "", "", false, false, "docker", "caddy", "managed"},
		{"docker only", "", "", true, false, "docker", "caddy", "existing"},
		{"nginx only", "", "", false, true, "docker", "nginx-existing", "managed"},
		{"both", "", "", true, true, "docker", "nginx-existing", "existing"},
		{"explicit auto", "auto", "auto", true, true, "docker", "nginx-existing", "existing"},
		{"explicit docker", "docker", "", true, true, "docker", "nginx-existing", "existing"},
		{"explicit standalone nginx", "docker", "nginx", true, false, "docker", "nginx", "existing"},
		{"explicit caddy", "", "caddy", true, true, "docker", "caddy", "existing"},
		{"k3s empty", "k3s", "", false, false, "k3s", "caddy", ""},
		{"k3s docker", "k3s", "", true, false, "k3s", "caddy", ""},
		{"k3s nginx", "k3s", "", false, true, "k3s", "nginx-existing", ""},
		{"k3s both", "k3s", "", true, true, "k3s", "nginx-existing", ""},
	}
	// All combinations use the same pure resolver used by production inspection.
	for _, tc := range cases {
		// Keep failures attached to the exact host layout.
		t.Run(tc.name, func(t *testing.T) {
			o := resolveDetectedOptions(Options{Runtime: tc.runtime, Proxy: tc.proxy}, nil, hostDetection{Docker: tc.docker, Nginx: tc.nginx})
			// Runtime selection must not silently change because an unrelated binary is present.
			if o.Runtime != tc.wantRuntime || o.Proxy != tc.wantProxy || o.DockerMode != tc.wantDocker {
				t.Fatalf("got runtime=%s proxy=%s docker=%s", o.Runtime, o.Proxy, o.DockerMode)
			}
		})
	}
}

// TestAutomaticRetryRetainsOwnership checks current and legacy manifests with both binaries now present.
func TestAutomaticRetryRetainsOwnership(t *testing.T) {
	// Existing records must retain their ownership even when the installed binary is detected again.
	records := []Installation{
		{Runtime: "docker", Proxy: "caddy", DockerMode: "existing"},
		{Runtime: "docker", Proxy: "nginx", DockerMode: "managed"},
		{Runtime: "docker", Proxy: "nginx-existing", DockerMode: "managed"},
		{Runtime: "k3s", Proxy: "nginx-existing"},
	}
	// Persist and reload the same JSON used by installation retries.
	for _, installed := range records {
		data, _ := json.Marshal(installed)
		dir := t.TempDir()
		// A failed durable fixture cannot prove retry behavior.
		if err := os.WriteFile(filepath.Join(dir, "installation.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := readInstallation(dir)
		// Do not confuse a missing record with a successful ownership round trip.
		if err != nil {
			t.Fatal(err)
		}
		o := resolveDetectedOptions(Options{}, loaded, hostDetection{Docker: true, Nginx: true})
		// Re-detection must retain each independently recorded choice.
		if o.Runtime != installed.Runtime || o.Proxy != installed.Proxy || o.DockerMode != installed.DockerMode {
			t.Fatalf("ownership changed: %+v -> %+v", installed, o)
		}
	}
	legacy := resolveDetectedOptions(Options{}, &Installation{Runtime: "docker", Proxy: "nginx-existing"}, hostDetection{})
	// The first coexistence version did not have a separate Docker ownership field.
	if legacy.DockerMode != "existing" {
		t.Fatal("legacy external Docker claimed")
	}
	legacy = resolveDetectedOptions(Options{}, &Installation{Runtime: "docker"}, hostDetection{Docker: true, Nginx: true})
	// Original installations used Caddy and a managed Docker runtime.
	if legacy.Proxy != "caddy" || legacy.DockerMode != "managed" {
		t.Fatal("legacy managed installation changed")
	}
}

// TestAutomaticRetryRestoresIdentity lets an installed controller preview an upgrade without repeated identity flags.
func TestAutomaticRetryRestoresIdentity(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	data, err := json.Marshal(Installation{Runtime: "docker", Proxy: "nginx-existing", ControllerHostname: "control.example.com", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "installation.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("version: 1\ndomains:\n  - example.com\n  - apps.example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	requested := Options{StateDir: dir, ConfigPath: configPath}
	resolved, _, err := detectInstallation(context.Background(), requested)
	if err != nil || resolved.Domain != "example.com" || resolved.ControllerHostname != "control.example.com" || resolved.Runtime != "docker" || resolved.Proxy != "nginx-existing" {
		t.Fatal("installed identity was not restored", resolved, err)
	}
	// Explicit choices remain authoritative for a separately reviewed change.
	requested.Domain, requested.ControllerHostname = "other.example.com", "other-control.example.com"
	resolved, _, err = detectInstallation(context.Background(), requested)
	if err != nil || resolved.Domain != requested.Domain || resolved.ControllerHostname != requested.ControllerHostname {
		t.Fatal("explicit identity was replaced", resolved, err)
	}
	// An incomplete installation needs a clear repair instruction when its domain file is gone.
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	requested.Domain = ""
	if _, _, err := detectInstallation(context.Background(), requested); err == nil || !strings.Contains(err.Error(), "--domain") {
		t.Fatal("missing installed domain was silently ignored", err)
	}
}

// TestReadInstallationFailsClosed prevents corrupt state from triggering automatic takeover.
func TestReadInstallationFailsClosed(t *testing.T) {
	// Corruption and unknown ownership must all stop automatic infrastructure selection.
	for _, contents := range []string{"not json", "null", `{"runtime":"docker","dockerMode":"unknown"}`} {
		dir := t.TempDir()
		// A deliberately malformed record must not be treated as first installation.
		if err := os.WriteFile(filepath.Join(dir, "installation.json"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		// Detection cannot safely select infrastructure from unreadable ownership metadata.
		if _, _, err := detectInstallation(context.Background(), Options{StateDir: dir}); err == nil {
			t.Fatal("corrupt state ignored", contents)
		}
	}
}

// TestDetectionRecognizesResidualConfiguration prevents inactive installations from being overwritten.
func TestDetectionRecognizesResidualConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nginx.conf")
	// A configuration file alone is sufficient evidence of external ownership.
	if err := os.WriteFile(path, []byte("# operator configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	// Presence must be detected even without package, binary or running-service evidence.
	if !detectComponent(context.Background(), "unused-test-service", nil, nil, []string{path}) {
		t.Fatal("existing configuration ignored")
	}
	o := resolveDetectedOptions(Options{}, nil, hostDetection{Nginx: true})
	// An unsupported or stopped instance is sent to coexistence validation, never a replacement install.
	if o.Proxy != "nginx-existing" {
		t.Fatal("replacement selected")
	}
}

// recordedInstallationCommands executes only injectable command recorders, never privileged host tools.
func recordedInstallationCommands(t *testing.T, o Options) []string {
	t.Helper()
	var calls []string
	// Even a stale firewall rule in the plan must not mutate an externally managed host policy.
	a := installationRunner{ctx: context.Background(), plan: Plan{Options: o, Snapshot: Snapshot{Firewall: "ufw", Rules: []int{80, 443}}}, caddyBinaryOverride: func(context.Context, bool) error { return nil }, runOverride: func(file string, args ...string) error {
		calls = append(calls, file+" "+strings.Join(args, " "))
		return nil
	}}
	// These stages cover all dependency and service mutations affected by independent detection.
	for _, stage := range []func() error{a.packages, a.firewall, a.services} {
		// A failed stage invalidates the command audit.
		if err := stage(); err != nil {
			t.Fatal(err)
		}
	}
	return calls
}

// TestDockerOnlyReuseLeavesDaemonAlone verifies a new Caddy installation does not take over existing Docker.
func TestDockerOnlyReuseLeavesDaemonAlone(t *testing.T) {
	o := resolveDetectedOptions(Options{Domain: "example.com"}, nil, hostDetection{Docker: true})
	calls := recordedInstallationCommands(t, o)
	// Existing Docker may be inspected but never enabled, restarted, installed or reconfigured.
	for _, call := range calls {
		// The shared daemon can appear in read-only readiness checks only.
		if strings.Contains(call, "docker") && !strings.HasPrefix(call, "systemctl is-active ") {
			t.Fatal("mutated existing Docker:", call)
		}
		// No host firewall commands belong to this reuse plan.
		if strings.HasPrefix(call, "ufw ") {
			t.Fatal("mutated existing firewall:", call)
		}
	}
	assets, err := InstallationAssets(o)
	// A Requires dependency would let systemd start external Docker indirectly.
	if err != nil || strings.Contains(assets.Service, "Requires=docker.service") {
		t.Fatal("external service dependency", err)
	}
}

// TestNginxOnlyInstallsMissingDocker verifies coexistence no longer suppresses an independently needed runtime.
func TestNginxOnlyInstallsMissingDocker(t *testing.T) {
	o := resolveDetectedOptions(Options{Domain: "example.com"}, nil, hostDetection{Nginx: true})
	calls := strings.Join(recordedInstallationCommands(t, o), "\n")
	// Both the absent runtime package and its service must be installed through the approved plan.
	if !strings.Contains(calls, "docker.io="+DockerVersion) || !strings.Contains(calls, "systemctl enable --now docker.service") {
		t.Fatal(calls)
	}
	// Existing Nginx receives only read-only readiness checks at installation service stages.
	if strings.Contains(calls, "restart nginx.service") || strings.Contains(calls, "disable --now nginx.service") || strings.Contains(calls, "enable --now nginx.service") {
		t.Fatal("external Nginx changed", calls)
	}
}

// TestExplicitK3sKeepsExistingDockerAndNginx verifies the override does not collapse back to Docker.
func TestExplicitK3sKeepsExistingDockerAndNginx(t *testing.T) {
	o := resolveDetectedOptions(Options{Domain: "example.com", Runtime: "k3s"}, nil, hostDetection{Docker: true, Nginx: true})
	calls := strings.Join(recordedInstallationCommands(t, o), "\n")
	// K3s owns its own service and containerd; no Docker action belongs to this plan.
	if strings.Contains(calls, "docker") || !strings.Contains(calls, "enable --now k3s.service") {
		t.Fatal(calls)
	}
	assets, err := InstallationAssets(o)
	// Restart uses k3s and the existing host proxy, without installing a competing edge.
	if err != nil || !strings.Contains(assets.Service, "--runtime k3s") || !strings.Contains(assets.Service, "--proxy nginx-existing") {
		t.Fatal(assets.Service, err)
	}
}
