package controller

import (
	"context"
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
)

// TestBridgeProfileTunnel publishes the selected profile API using the actual Bridge CLI and controller.
func TestBridgeProfileTunnel(t *testing.T) {
	binary := os.Getenv("HOSTLIER_TEST_BRIDGE_BINARY")
	// Keep standalone Hostlier tests independent of the sibling Bridge repository.
	if binary == "" {
		t.Skip("set HOSTLIER_TEST_BRIDGE_BINARY for profile tunnelling integration")
	}
	c := tunnelController(t, t.TempDir())
	human := attachHumanFixture(t, c)
	credential := bridgeSetupCredential(t, c, human.token)
	app := bridgeProfileApp(t)
	edge := httptest.NewTLSServer(c.Handler())
	defer edge.Close()
	c.mu.Lock()
	c.opts.TunnelURL = edge.URL
	c.mu.Unlock()
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace.db")
	home := filepath.Join(dir, "hostlier")
	ca := filepath.Join(dir, "ca.pem")
	// Preserve normal certificate verification for subprocess management and reverse transport.
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: edge.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	connect := startBridgeSetupProcess(t, binary, []string{"connect", app.URL, "--name", "test-api", "--config", workspace}, "", home)
	waitBridgeProcessText(t, connect, "Profile test-api is running")
	connect.cancel()
	<-connect.done
	token := bridgeProfileToken(t, binary, workspace)
	tunnel := startBridgeSetupProcess(t, binary, []string{"tunnel", "--profile", "test-api", "--workspace", workspace, "--hostlier", edge.URL, "--ca-file", ca, "--token-env", "TEST_HOSTLIER_MANAGEMENT"}, credential, home)
	approveBridgeSetup(t, human, waitBridgeSetupApproval(t, c, "POST", app.URL))
	waitBridgeConnectedOutput(t, tunnel)
	config, name := readDefaultBridgeConfig(t, home)
	assertBridgeProfileConfig(t, config, workspace, token)
	assertBridgeProfileRoutes(t, edge, name, token)
	tunnel.cancel()
	<-tunnel.done
	// A new process must recreate the profile listener while retaining its approved public identity.
	tunnel = startBridgeSetupProcess(t, binary, []string{"tunnel", "--profile", "test-api", "--workspace", workspace}, "", home)
	waitBridgeConnectedOutput(t, tunnel)
	assertBridgeProfileRoutes(t, edge, name, token)
	revoke := startBridgeSetupProcess(t, binary, []string{"tunnel", "revoke", "--config", config, "--token-env", "TEST_HOSTLIER_MANAGEMENT"}, credential, home)
	approveBridgeSetup(t, human, waitBridgeSetupApproval(t, c, "DELETE", app.URL))
	revoke.finish(t)
	// Rejecting the scoped connector must also terminate the profile tunnel process.
	select {
	// Observe the connector termination caused by revocation.
	case <-tunnel.done:
	// Bound shutdown independently of the process fixture deadline.
	case <-time.After(5 * time.Second):
		t.Fatal("revoked profile tunnel did not stop")
	}
}

// bridgeProfileApp supplies an OpenAPI service whose read can be called only through selected Bridge operations.
func bridgeProfileApp(t *testing.T) *httptest.Server {
	t.Helper()
	// Handle only fixture discovery and the selected read operation.
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Discovery is read-only and describes both selected and unselected operations.
		if r.URL.Path == "/openapi.json" {
			fmt.Fprint(w, `{"openapi":"3.0.3","info":{"title":"fixture","version":"1"},"paths":{"/status":{"get":{"operationId":"getStatus","responses":{"200":{"description":"ok"}}},"post":{"operationId":"change","responses":{"200":{"description":"ok"}}}}}}`)
			return
		}
		// The test must never invoke an unselected mutation on the underlying service.
		if r.URL.Path == "/status" && r.Method == "GET" {
			fmt.Fprint(w, `{"source":"local-app"}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(app.Close)
	return app
}

// bridgeProfileToken reads the credential through the explicit user-facing command without logging its value.
func bridgeProfileToken(t *testing.T, binary, workspace string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, binary, "profile", "token", "--name", "test-api", "--config", workspace).Output()
	var result struct {
		Token string `json:"token"`
	}
	// Fail without rendering credential-bearing subprocess output.
	if err != nil || json.Unmarshal(raw, &result) != nil || len(result.Token) < 32 {
		t.Fatal("could not retrieve profile token")
	}
	return result.Token
}

// assertBridgeProfileConfig checks that runtime ports and caller credentials never enter tunnel state.
func assertBridgeProfileConfig(t *testing.T, config, workspace, token string) {
	t.Helper()
	workspace, _ = filepath.EvalSymlinks(workspace)
	raw, err := os.ReadFile(config)
	var saved map[string]any
	// The persistent selector must refer to a profile rather than a soon-stale listener port.
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved["profile"] != "test-api" || saved["workspace"] != workspace || saved["target"] != "" {
		t.Fatal("invalid saved profile tunnel")
	}
	// Management and profile API credentials are distinct from the scoped tunnel credential.
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "device-v1:") {
		t.Fatal("credential in tunnel config")
	}
}

// assertBridgeProfileRoutes checks caller authentication, profile operation dispatch and management isolation through Hostlier.
func assertBridgeProfileRoutes(t *testing.T, edge *httptest.Server, name, token string) {
	t.Helper()
	base := "/v1/profiles/test-api"
	cases := []struct {
		path, body, credential string
		status                 int
	}{
		{base, "", "", 401}, {base, "", token, 200}, {"/inspector/", "", token, 404}, {"/v1/profiles/other", "", token, 404}, {"/status", "", token, 404},
		{base + "/invoke", `{"connection":"test-api-http","operation":"http.getStatus","input":{}}`, token, 200},
		{base + "/invoke", `{"connection":"test-api-http","operation":"http.change","input":{}}`, token, 403},
	}
	// Every public request travels through the real broker, reverse connection and isolated profile handler.
	for _, tc := range cases {
		method := "GET"
		// Invocation requires the profile API's POST envelope.
		if tc.body != "" {
			method = "POST"
		}
		request, _ := http.NewRequest(method, edge.URL+tc.path, strings.NewReader(tc.body))
		request.Host = name + ".tunnels.example.com"
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+tc.credential)
		response, err := edge.Client().Do(request)
		// Transport errors must not be mistaken for correct authentication rejection.
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		// Report only route and status, never credential-bearing request headers.
		if response.StatusCode != tc.status {
			t.Fatalf("%s: got %d, want %d", tc.path, response.StatusCode, tc.status)
		}
		// Successful invocation must reach the configured underlying app.
		if tc.body != "" && tc.status == 200 && !strings.Contains(string(data), "local-app") {
			t.Fatal("profile did not invoke the local service")
		}
	}
}

// waitBridgeProcessText observes process readiness without introducing production test hooks.
func waitBridgeProcessText(t *testing.T, process bridgeSetupProcess, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	// Bound readiness checks so a failed runtime cannot hang the test suite.
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(process.log)
		// Readiness output contains public profile metadata only.
		if strings.Contains(string(raw), text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Bridge did not reach the expected runtime state")
}
