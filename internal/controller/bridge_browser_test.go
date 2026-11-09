package controller

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"hostlier/internal/client"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBridgeBinaryBrowserEnrollment verifies Bridge's browser login against the real controller with email disabled.
func TestBridgeBinaryBrowserEnrollment(t *testing.T) {
	binary := os.Getenv("HOSTLIER_TEST_BRIDGE_BINARY")
	// Cross-repository integration uses the explicitly selected local build.
	if binary == "" {
		t.Skip("set HOSTLIER_TEST_BRIDGE_BINARY to run cross-project integration")
	}
	c := tunnelController(t, t.TempDir())
	attachHumanFixture(t, c)
	edge := httptest.NewTLSServer(c.Handler())
	defer edge.Close()
	c.state.Hostname = strings.TrimPrefix(edge.URL, "https://")
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	// Private fixture trust never disables TLS certificate validation.
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: edge.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "tunnel", "login", "--hostlier", edge.URL, "--ca-file", ca, "--no-browser")
	command.Env = append(os.Environ(), "FUSED_BRIDGE_HOSTLIER_HOME="+filepath.Join(dir, "profiles"), "HOSTLIER_TOKEN=")
	approveBridgeProcess(t, ctx, cancel, c, command)
	credential := bridgeFixtureCredential(t, dir, edge.URL)
	api, err := client.New(client.Options{URL: edge.URL, Token: credential})
	// The saved envelope must be compatible with Hostlier's device proof protocol.
	if err != nil {
		t.Fatal(err)
	}
	api.HTTP = edge.Client()
	// A signed read proves that the approved device can manage tunnels.
	if err = api.Request(ctx, "GET", "/v1/tunnels", nil, new(any)); err != nil {
		t.Fatal(err)
	}
}

// bridgeFixtureCredential reads only the disposable test profile, never the user's real credentials.
func bridgeFixtureCredential(t *testing.T, dir, origin string) string {
	t.Helper()
	hash := sha256.Sum256([]byte(origin))
	raw, err := os.ReadFile(filepath.Join(dir, "profiles", hex.EncodeToString(hash[:])+".json"))
	// Enrollment must atomically persist the resulting device identity.
	if err != nil {
		t.Fatal(err)
	}
	var profile struct{ Credential string }
	// The private key stays in the device envelope and is never logged.
	if json.Unmarshal(raw, &profile) != nil || !strings.HasPrefix(profile.Credential, "device-v1:") {
		t.Fatal("missing Bridge device credential")
	}
	return profile.Credential
}

// approveBridgeProcess approves only the disposable child's pending identity and drains its output.
func approveBridgeProcess(t *testing.T, ctx context.Context, cancel context.CancelFunc, c *Controller, command *exec.Cmd) {
	t.Helper()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	// Process setup must succeed before approval can be issued.
	if err != nil {
		t.Fatal(err)
	}
	// A failed launch cannot leave an unobserved subprocess.
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	request := make(chan string, 1)
	// Drain stdout continuously while retaining only the public enrollment identity.
	go func() {
		scanner := bufio.NewScanner(stdout)
		// The request ID identifies the owner review, never its proof secret.
		for scanner.Scan() {
			// Capture only the initiating request printed by Bridge.
			if id, ok := strings.CutPrefix(scanner.Text(), "Request ID: "); ok {
				request <- id
			}
		}
	}()
	// Approval must arrive before the bounded subprocess context expires.
	select {
	// Only the isolated protected test administrator approves this device.
	case id := <-request:
		status, _ := humanHTTP(t, c, true, "POST", "/v1/access/pair/code", map[string]any{"id": id, "scope": deploymentScope{}}, "", "")
		// Approval failures stop the child instead of leaving it polling.
		if status != 200 {
			cancel()
			command.Wait()
			t.Fatal("fixture owner approval failed", status)
		}
	// Bound a broken pairing flow without user interaction.
	case <-ctx.Done():
		command.Wait()
		t.Fatal("Bridge did not request browser enrollment", stderr.String())
	}
	// Successful browser approval must return without any email interaction.
	if err = command.Wait(); err != nil {
		t.Fatal(err, stderr.String())
	}
}
