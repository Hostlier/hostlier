package nginx

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLiveVMGatewayPrepare installs only the generated private backend on the authorized disposable VM.
func TestLiveVMGatewayPrepare(t *testing.T) {
	// Ordinary test runs never create service assets or start system processes.
	if os.Getenv("HOSTLIER_NGINX_GATEWAY_VM") != "1" {
		t.Skip("explicit disposable Nginx gateway VM required")
	}
	host, _ := os.Hostname()
	marker, err := os.ReadFile("/root/hostlier-cold-cli-e2e/fixture")
	// Exact guest identity, root ownership and the existing test marker fence all service mutations.
	if err != nil || host != "lima-hostlier-cli-deploy" || string(marker) != "hostlier-cold-cli-e2e-v1\n" || os.Geteuid() != 0 {
		t.Fatal("wrong Nginx gateway VM fixture")
	}
	unit, bootstrap, err := GatewayServiceAssets("")
	// Only production-generated service and bootstrap assets are installed.
	if err != nil {
		t.Fatal(err)
	}
	directory := "/etc/hostlier/nginx-gateway"
	// The new private namespace cannot overwrite standalone/operator-owned Nginx assets.
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(directory, "nginx.conf")
	// Subsequent test runs retain already published application routes.
	if _, err := os.Stat(config); os.IsNotExist(err) {
		// Bootstrap is private and returns 503 until the controller publishes approved routes.
		if err := os.WriteFile(config, bootstrap, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeGatewayVMUnit(t, unit)
	runEndpointFixture(t, "nginx", "-t", "-c", config)
	runEndpointFixture(t, "systemctl", "daemon-reload")
	runEndpointFixture(t, "systemctl", "enable", "--now", "hostlier-nginx-gateway.service")
	runEndpointFixture(t, "systemctl", "is-active", "hostlier-nginx-gateway.service")
}

// writeGatewayVMUnit writes only the dedicated, generated private backend unit on this fixture.
func writeGatewayVMUnit(t *testing.T, unit string) {
	t.Helper()
	// No external nginx.service or public port owner is changed by this setup.
	if err := os.WriteFile("/etc/systemd/system/hostlier-nginx-gateway.service", []byte(unit), 0644); err != nil {
		t.Fatal(err)
	}
}
