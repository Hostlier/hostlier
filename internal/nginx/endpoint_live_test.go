package nginx

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveVMNamedEndpointProtocols validates generated native Nginx routes against the deployed protocol fixture.
func TestLiveVMNamedEndpointProtocols(t *testing.T) {
	// The VM-specific certificate and client fixture is never inferred on a developer machine.
	if os.Getenv("HOSTLIER_NGINX_ENDPOINT_VM") != "1" {
		t.Skip("explicit disposable endpoint VM fixture required")
	}
	root := "/root/hostlier-engine-e2e/nginx"
	var fixture struct {
		Options Options
		Routes  []Route
	}
	data, err := os.ReadFile(filepath.Join(root, "fixture.json"))
	// Only a complete, deliberate fixture may start the isolated Nginx process.
	if err != nil {
		t.Fatal(err)
	}
	// The generated route contract is exercised directly, without handwritten proxy snippets.
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	config, err := Render(fixture.Options, "hostlier.apps.hostlier.test", "127.0.0.1:7463", fixture.Routes)
	// Invalid transport input cannot become a running edge configuration.
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "nginx.conf")
	// This fixture writes only under its explicitly provided private directory.
	if err = os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	runEndpointFixture(t, "nginx", "-t", "-c", path)
	runEndpointFixture(t, "nginx", "-c", path)
	// Always release this independent test listener without touching the installed Caddy edge.
	t.Cleanup(func() { _ = exec.Command("nginx", "-s", "quit", "-c", path).Run() })
	runEndpointFixture(t, "/tmp/hostlier-protocol-probe.test", "-test.v", "-test.run", "Test(HTTP|SSE|WebSocket|StreamableHTTP|Duplex|NativeGRPC|EngineGRPC)$", "-test.timeout", "45s")
}

// runEndpointFixture bounds external proxy validation and reports only nonsecret fixture diagnostics.
func runEndpointFixture(t *testing.T, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	log, err := os.CreateTemp(t.TempDir(), "process-log-")
	// Daemon workers must not inherit a pipe whose EOF would block the test driver.
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout, command.Stderr = log, log
	err = command.Run()
	output, _ := os.ReadFile(log.Name())
	// Syntax, startup and client failures invalidate the end-to-end protocol result.
	if err != nil {
		t.Fatalf("%s: %v\n%s", name, err, output)
	}
	t.Logf("%s: %s", name, output)
}
