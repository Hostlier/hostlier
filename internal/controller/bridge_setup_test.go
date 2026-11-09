package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hostlier/internal/client"
)

// TestBridgeHTTPSetupAndForwarding uses the actual Bridge binary, HTTP approvals and device proofs end to end.
func TestBridgeHTTPSetupAndForwarding(t *testing.T) {
	binary := os.Getenv("HOSTLIER_TEST_BRIDGE_BINARY")
	// Cross-repository verification is opt-in for ordinary standalone Hostlier test runs.
	if binary == "" {
		t.Skip("set HOSTLIER_TEST_BRIDGE_BINARY for Bridge HTTP setup integration")
	}
	c := tunnelController(t, t.TempDir())
	human := attachHumanFixture(t, c)
	credential := bridgeSetupCredential(t, c, human.token)
	// Serve a real local application without an external runtime dependency.
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "local service reached") }))
	defer app.Close()
	edge := httptest.NewTLSServer(c.Handler())
	defer edge.Close()
	c.mu.Lock()
	c.opts.TunnelURL = edge.URL
	c.mu.Unlock()
	dir := t.TempDir()
	home := filepath.Join(dir, "profiles")
	ca := filepath.Join(dir, "ca.pem")
	// Trust only the test certificate, without weakening certificate verification.
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: edge.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"tunnel", app.URL, "--hostlier", edge.URL, "--ca-file", ca, "--token-env", "TEST_HOSTLIER_MANAGEMENT"}
	connector := startBridgeSetupProcess(t, binary, args, credential, home)
	approval := waitBridgeSetupApproval(t, c, "POST", app.URL)
	// The human ceremony grants approval; Bridge itself must execute and recover the receipt over HTTP.
	approveBridgeSetup(t, human, approval)
	config, name := readDefaultBridgeConfig(t, home)
	waitTunnelConnection(t, c, name)
	assertBridgeLocalConfig(t, config, app.URL, credential)
	// Restart using only the local service: origin, CA, identity and scoped token must be remembered.
	connector.cancel()
	<-connector.done
	connector = startBridgeSetupProcess(t, binary, []string{"tunnel", app.URL}, "", home)
	waitBridgeConnectedOutput(t, connector)

	req, _ := http.NewRequest("GET", edge.URL+"/", nil)
	req.Host = name + ".tunnels.example.com"
	response, err := edge.Client().Do(req)
	// Publication must reach the app through the separately authenticated reverse connection.
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	// Fail if setup, signature verification or the local-only configuration boundary is violated.
	if string(data) != "local service reached" {
		t.Fatal(string(data))
	}
	revoke := startBridgeSetupProcess(t, binary, []string{"tunnel", "revoke", "--config", config, "--ca-file", ca, "--token-env", "TEST_HOSTLIER_MANAGEMENT"}, credential, home)
	approveBridgeSetup(t, human, waitBridgeSetupApproval(t, c, "DELETE", app.URL))
	revoke.finish(t)
	// Revocation should terminate the running connector, whose nonzero exit reports the rejected lease.
	select {
	// Distinguish observed process termination from the fixture deadline.
	case <-connector.done:
	// Distinguish observed process termination from the fixture deadline.
	case <-time.After(5 * time.Second):
		t.Fatal("revoked connector did not stop")
	}
}

// bridgeSetupCredential attaches a device key to the fixture identity so every Bridge API request must be signed.
func bridgeSetupCredential(t *testing.T, c *Controller, token string) string {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	// Fail if setup, signature verification or the local-only configuration boundary is violated.
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Bound fixture observation while synchronizing access to controller state.
	for _, identity := range c.state.Clients {
		// Bind only the management identity used by this Bridge subprocess.
		if identity.Verifier == verifier(token) {
			identity.PublicKey = base64.RawStdEncoding.EncodeToString(public)
		}
	}
	return client.EncodeDeviceCredential(token, private)
}

// bridgeSetupProcess owns a cancellable subprocess and a private diagnostics file.
type bridgeSetupProcess struct {
	done   <-chan error
	log    string
	cancel context.CancelFunc
}

// startBridgeSetupProcess starts the actual CLI with credentials supplied only through its environment.
func startBridgeSetupProcess(t *testing.T, binary string, args []string, credential, home string) bridgeSetupProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	log := filepath.Join(t.TempDir(), "output.log")
	output, err := os.OpenFile(log, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	// Fail if setup, signature verification or the local-only configuration boundary is violated.
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append(os.Environ(), "TEST_HOSTLIER_MANAGEMENT="+credential, "FUSED_BRIDGE_HOSTLIER_HOME="+home, "HOSTLIER_TOKEN=")
	command.Stdout = output
	command.Stderr = output
	// Fail if setup, signature verification or the local-only configuration boundary is violated.
	if err = command.Start(); err != nil {
		output.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	// Wait owns the process resources; cancellation also bounds test-failure cleanup.
	go func() { err := command.Wait(); output.Close(); done <- err }()
	return bridgeSetupProcess{done: done, log: log, cancel: cancel}
}

// finish bounds setup completion and checks that credentials were never rendered as diagnostics.
func (p bridgeSetupProcess) finish(t *testing.T) {
	t.Helper()
	// Bound subprocess completion so broken setup cannot hang the suite.
	select {
	// Distinguish observed process termination from the fixture deadline.
	case err := <-p.done:
		data, _ := os.ReadFile(p.log)
		// Fail if setup, signature verification or the local-only configuration boundary is violated.
		if err != nil {
			t.Fatalf("Bridge setup: %v\n%s", err, data)
		}
		// Fail if setup, signature verification or the local-only configuration boundary is violated.
		if strings.Contains(string(data), "device-v1:") {
			t.Fatal("credential exposed in diagnostics")
		}
	// Distinguish observed process termination from the fixture deadline.
	case <-time.After(15 * time.Second):
		t.Fatal("Bridge setup did not complete")
	}
}

// waitBridgeSetupApproval observes proposed intent under the controller lock without granting any authority.
func waitBridgeSetupApproval(t *testing.T, c *Controller, method, localTarget string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	// Bound fixture observation while synchronizing access to controller state.
	for time.Now().Before(deadline) {
		c.mu.Lock()
		var view map[string]any
		// Bound fixture observation while synchronizing access to controller state.
		for _, approval := range c.state.HumanApprovals {
			// Inspect only the pending mutation initiated by this test phase.
			if approval.Status == "pending" && approval.Method == method {
				request, err := c.openHumanRequest(approval)
				// Fail if setup, signature verification or the local-only configuration boundary is violated.
				if err != nil {
					c.mu.Unlock()
					t.Fatal(err)
				}
				// Fail if setup, signature verification or the local-only configuration boundary is violated.
				if strings.Contains(string(request.Body), localTarget) || strings.Contains(string(request.Body), `"target"`) {
					c.mu.Unlock()
					t.Fatal("local target sent to Hostlier")
				}
				view = c.humanApprovalView(approval)
			}
		}
		c.mu.Unlock()
		// Return the immutable review once the real API has retained it.
		if view != nil {
			return view
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Bridge did not propose an approval")
	return nil
}

// approveBridgeSetup exercises the normal independent owner-signature ceremony without executing the mutation.
func approveBridgeSetup(t *testing.T, human *humanFixture, approval map[string]any) {
	t.Helper()
	start := human.beginDecision(t, approval, "approve")
	ceremony := human.session(t, start)
	proof := map[string]any{"ceremony": start["ceremony"], "signature": human.signature(t, ceremony.Message, 0x05)}
	status, result := humanHTTP(t, human.c, false, "POST", "/v1/human/decision/finish", proof, "", "")
	// Fail if setup, signature verification or the local-only configuration boundary is violated.
	if status != 200 {
		t.Fatal(status, result)
	}
}

// assertBridgeLocalConfig verifies the forwarding target is saved locally and management authority is not.
func assertBridgeLocalConfig(t *testing.T, config, target, credential string) {
	t.Helper()
	data, err := os.ReadFile(config)
	// Fail if setup, signature verification or the local-only configuration boundary is violated.
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	// Fail if setup, signature verification or the local-only configuration boundary is violated.
	if json.Unmarshal(data, &saved) != nil || saved["target"] != target || saved["pendingAction"] != nil {
		t.Fatal("invalid local setup state")
	}
	// Fail if setup, signature verification or the local-only configuration boundary is violated.
	if strings.Contains(string(data), credential) || strings.Contains(string(data), "device-v1:") {
		t.Fatal("management credential persisted in connector config")
	}
}

// waitBridgeConnectedOutput requires the restarted process itself to finish connecting.
func waitBridgeConnectedOutput(t *testing.T, process bridgeSetupProcess) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	// Poll only this subprocess's private output, never authentication material.
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(process.log)
		// The connector emits this after its reverse and local sockets are ready.
		if strings.Contains(string(data), "Tunnel connected:") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Bridge did not reconnect using remembered settings")
}

// readDefaultBridgeConfig checks the config and public name chosen by the one-command flow.
func readDefaultBridgeConfig(t *testing.T, home string) (string, string) {
	t.Helper()
	configs, err := filepath.Glob(filepath.Join(home, "tunnel-*.json"))
	// Require a single stable file for the service selected by this fixture.
	if err != nil || len(configs) != 1 {
		t.Fatal("Bridge did not save its default configuration")
	}
	var saved struct {
		Name string `json:"name"`
	}
	data, err := os.ReadFile(configs[0])
	// Decode only public identity; scoped credentials must not appear in diagnostics.
	if err != nil || json.Unmarshal(data, &saved) != nil {
		t.Fatal("invalid generated configuration")
	}
	return configs[0], saved.Name
}
