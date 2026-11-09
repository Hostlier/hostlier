package controller

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
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

	"github.com/coder/websocket"
	"hostlier/internal/model"
	"hostlier/internal/tunnel"
)

type tunnelIngressFixture struct {
	recordingRuntime
	hosts []string
	fail  bool
}

func (r *tunnelIngressFixture) EnsureTunnelIngress(_ context.Context, publicURL string) error {
	r.hosts = append(r.hosts, publicURL)
	if r.fail {
		return errors.New("edge unavailable")
	}
	return nil
}

func TestTunnelCreationRequiresIngressAndRestoresIt(t *testing.T) {
	dir := t.TempDir()
	runtime := &tunnelIngressFixture{fail: true}
	newController := func() *Controller {
		c, err := New(Options{StateDir: dir, Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: runtime, Hostname: "controller.example.com", TunnelDomain: "tunnels.example.com", TunnelURL: "https://controller.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := newController()
	request := map[string]any{"name": "new", "bridgeId": "laptop", "connections": 4, "durationSeconds": 3600, "tokenVerifier": verifier("credential")}
	if status, _ := call(t, c, true, "POST", "/v1/tunnels", request, ""); status == 200 || len(c.state.Tunnels) != 0 {
		t.Fatal("issued a tunnel without HTTPS ingress")
	}
	runtime.fail = false
	if status, _ := call(t, c, true, "POST", "/v1/tunnels", request, ""); status != 200 {
		t.Fatalf("ingress-ready creation failed: %d", status)
	}
	if len(runtime.hosts) != 2 || runtime.hosts[1] != "https://new.tunnels.example.com" {
		t.Fatalf("wrong ingress publications: %v", runtime.hosts)
	}
	if status, _ := call(t, c, true, "POST", "/v1/tunnels", request, ""); status != 200 || len(runtime.hosts) != 3 {
		t.Fatal("identical retry did not repair ingress")
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime.hosts = nil
	restored := newController()
	defer restored.Close(context.Background())
	if len(runtime.hosts) != 1 || runtime.hosts[0] != "https://new.tunnels.example.com" {
		t.Fatalf("restart did not reconcile HTTPS ingress: %v", runtime.hosts)
	}
}

// tunnelController starts a durable gateway without Docker or external DNS dependencies.
func tunnelController(t *testing.T, dir string) *Controller {
	t.Helper()
	c, err := New(Options{StateDir: dir, Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: &recordingRuntime{}, Hostname: "controller.example.com", TunnelDomain: "tunnels.example.com", TunnelURL: "https://controller.example.com"})
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err != nil {
		t.Fatal(err)
	}
	// Keep the local fixture independent of external services and scheduling.
	t.Cleanup(func() {
		// Fail the test if the expected transport, ownership or credential boundary is violated.
		if !c.closed {
			c.Close(context.Background())
		}
	})
	return c
}

// createTestTunnel exercises the actual durable management handler with a caller-held secret.
func createTestTunnel(t *testing.T, c *Controller, name string) string {
	t.Helper()
	token := strings.Repeat(name, 32)
	status, out := call(t, c, true, "POST", "/v1/tunnels", map[string]any{"name": name, "bridgeId": "laptop", "connections": 4, "durationSeconds": 3600, "tokenVerifier": verifier(token)}, "")
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if status != 200 {
		t.Fatalf("create: %d %v", status, out)
	}
	data, _ := json.Marshal(out)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if strings.Contains(string(data), "tokenVerifier") || strings.Contains(string(data), token) {
		t.Fatal("credential leaked")
	}
	return token
}

// TestTunnelReservationRestartAndBoundaries checks durable ownership, reserved namespaces and API isolation.
func TestTunnelReservationRestartAndBoundaries(t *testing.T) {
	dir := t.TempDir()
	c := tunnelController(t, dir)
	token := createTestTunnel(t, c, "demo")
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if status, _ := call(t, c, false, "POST", "/v1/tunnels", map[string]any{}, ""); status != 401 {
		t.Fatal(status)
	}
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err := c.checkRouteReservation("app", model.Route{Hostname: "else.tunnels.example.com"}); err == nil {
		t.Fatal("namespace was not reserved")
	}
	checkTunnelHostIsolation(t, c)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored := tunnelController(t, dir)
	lease := restored.state.Tunnels["demo"]
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if lease.URL != "https://demo.tunnels.example.com" || lease.TokenVerifier != verifier(token) {
		t.Fatal("reservation lost")
	}
	r := httptest.NewRequest("GET", "https://controller.example.com/v1/tunnels/demo/connect", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if broker, status := restored.authenticateTunnel(r, "demo"); status != 0 || broker == nil {
		t.Fatalf("restored credential: %d", status)
	}
	r.Header.Set("Authorization", "Bearer invalid")
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if _, status := restored.authenticateTunnel(r, "demo"); status != 401 {
		t.Fatal(status)
	}
}

// TestTunnelTrafficAndRevocation exercises real outbound TLS, forwarded app auth and immediate stream teardown.
func TestTunnelTrafficAndRevocation(t *testing.T) {
	c := tunnelController(t, t.TempDir())
	// Keep the local fixture independent of external services and scheduling.
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fail the test if the expected transport, ownership or credential boundary is violated.
		if r.URL.Path == "/ws" {
			tunnelEcho(w, r)
			return
		}
		fmt.Fprintf(w, "%s|%s|%s|%s", r.Host, r.Header.Get("Cookie"), r.Header.Get("Authorization"), r.Header.Get("X-Forwarded-Proto"))
	}))
	defer app.Close()
	token := createTestTunnel(t, c, "demo")
	edge := httptest.NewTLSServer(c.Handler())
	defer edge.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	// Keep the local fixture independent of external services and scheduling.
	go func() {
		done <- (tunnel.Client{URL: edge.URL + "/v1/tunnels/demo/connect", Token: token, Target: app.URL, HTTPClient: edge.Client(), Connections: 2}).Run(ctx)
	}()
	waitTunnelConnection(t, c, "demo")
	request, _ := http.NewRequest("GET", edge.URL+"/", nil)
	request.Host = "demo.tunnels.example.com"
	request.Header.Set("Cookie", "session=app")
	request.Header.Set("Authorization", "Bearer app-token")
	request.Header.Set("X-Forwarded-Proto", "attacker")
	response, err := edge.Client().Do(request)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if string(data) != "demo.tunnels.example.com|session=app|Bearer app-token|https" {
		t.Fatal(string(data))
	}
	checkTunnelRevocation(t, c, edge, ctx, done)
}

// checkTunnelRevocation verifies upgraded application streams terminate with their lease.
func checkTunnelRevocation(t *testing.T, c *Controller, edge *httptest.Server, ctx context.Context, done <-chan error) {
	t.Helper()
	ws, _, err := websocket.Dial(ctx, edge.URL+"/ws", &websocket.DialOptions{HTTPClient: edge.Client(), Host: "demo.tunnels.example.com"})
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err = ws.Write(ctx, websocket.MessageText, []byte("alive")); err != nil {
		t.Fatal(err)
	}
	_, echo, err := ws.Read(ctx)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err != nil || string(echo) != "alive" {
		t.Fatalf("%s %v", echo, err)
	}
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if status, _ := call(t, c, true, "DELETE", "/v1/tunnels/demo", nil, ""); status != 200 {
		t.Fatal(status)
	}
	deadline, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if _, _, err = ws.Read(deadline); err == nil {
		t.Fatal("revoked stream remained open")
	}
	// Observe connector termination within the test deadline.
	select {
	// Distinguish observed completion from a timed-out transport.
	case <-done:
	// Distinguish observed completion from a timed-out transport.
	case <-time.After(3 * time.Second):
		t.Fatal("revoked connector did not stop")
	}
}

// tunnelEcho preserves application WebSocket frames independently of the outer tunnel framing.
func tunnelEcho(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err != nil {
		return
	}
	defer ws.CloseNow()
	// Bound fixture readiness and transport waits so failures terminate the test.
	for {
		typ, data, err := ws.Read(r.Context())
		// Fail the test if the expected transport, ownership or credential boundary is violated.
		if err != nil {
			return
		}
		// Fail the test if the expected transport, ownership or credential boundary is violated.
		if ws.Write(r.Context(), typ, data) != nil {
			return
		}
	}
}

// waitTunnelConnection bounds asynchronous readiness without assuming worker scheduling.
func waitTunnelConnection(t *testing.T, c *Controller, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	// Bound fixture readiness and transport waits so failures terminate the test.
	for time.Now().Before(deadline) {
		c.mu.Lock()
		broker := c.tunnelBrokers[name]
		ready := broker != nil && broker.Connected() > 0
		c.mu.Unlock()
		// Fail the test if the expected transport, ownership or credential boundary is violated.
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("connector did not become ready")
}

// TestTunnelExpiryAndIsolation checks credential scope and closes pipes after lifetime exhaustion.
func TestTunnelExpiryAndIsolation(t *testing.T) {
	c := tunnelController(t, t.TempDir())
	token := createTestTunnel(t, c, "first")
	createTestTunnel(t, c, "second")
	r := httptest.NewRequest("GET", "https://controller.example.com/v1/tunnels/second/connect", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if _, status := c.authenticateTunnel(r, "second"); status != 401 {
		t.Fatal(status)
	}
	c.mu.Lock()
	lease := c.state.Tunnels["first"]
	lease.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	c.state.Tunnels["first"] = lease
	c.mu.Unlock()
	c.expireTunnels()
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if _, status := c.authenticateTunnel(r, "first"); status != 410 {
		t.Fatal(status)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if c.tunnelBrokers["first"] != nil {
		t.Fatal("expired broker retained")
	}
}

// TestBridgeBinaryTunnel verifies the independently built Bridge CLI against the Hostlier controller.
func TestBridgeBinaryTunnel(t *testing.T) {
	binary := os.Getenv("HOSTLIER_TEST_BRIDGE_BINARY")
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if binary == "" {
		t.Skip("set HOSTLIER_TEST_BRIDGE_BINARY to run cross-project integration")
	}
	c := tunnelController(t, t.TempDir())
	// Keep the local fixture independent of external services and scheduling.
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "bridge delivered "+r.Host) }))
	defer app.Close()
	token := createTestTunnel(t, c, "bridge")
	edge := httptest.NewTLSServer(c.Handler())
	defer edge.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: edge.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "tunnel.json")
	raw, _ := json.Marshal(map[string]any{"name": "bridge", "bridgeId": "laptop", "target": app.URL, "url": "https://bridge.tunnels.example.com", "connectUrl": edge.URL + "/v1/tunnels/bridge/connect", "connections": 2, "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "token": token})
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(binary, "tunnel", "--config", config, "--ca-file", ca)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	// Keep the local fixture independent of external services and scheduling.
	defer func() { process.Process.Signal(os.Interrupt); process.Wait() }()
	waitTunnelConnection(t, c, "bridge")
	req, _ := http.NewRequest("GET", edge.URL+"/", nil)
	req.Host = "bridge.tunnels.example.com"
	response, err := edge.Client().Do(req)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if string(body) != "bridge delivered bridge.tunnels.example.com" {
		t.Fatal(string(body))
	}
}

// checkTunnelHostIsolation ensures application and unknown names never dispatch management APIs.
func checkTunnelHostIsolation(t *testing.T, c *Controller) {
	t.Helper()
	r := httptest.NewRequest("GET", "https://demo.tunnels.example.com/v1/status", nil)
	w := httptest.NewRecorder()
	c.Handler().ServeHTTP(w, r)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if w.Code != 503 {
		t.Fatalf("public hostname reached management: %d", w.Code)
	}
	r.Host = "unknown.tunnels.example.com"
	w = httptest.NewRecorder()
	c.Handler().ServeHTTP(w, r)
	// Fail the test if the expected transport, ownership or credential boundary is violated.
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}

// TestTunnelManagementApprovalAndCredentialScope prevents connector tokens from becoming management authority.
func TestTunnelManagementApprovalAndCredentialScope(t *testing.T) {
	c := tunnelController(t, t.TempDir())
	token := createTestTunnel(t, c, "private")
	c.mu.Lock()
	c.state.Clients["other"] = &Client{ID: "other", Verifier: verifier("management-token")}
	c.mu.Unlock()
	// A valid management token still needs owner approval before publishing anything.
	if status, out := call(t, c, false, "POST", "/v1/tunnels", map[string]any{}, "management-token"); status != 403 || code(out) != "HUMAN_APPROVAL_REQUIRED" {
		t.Fatalf("approval bypass: %d %v", status, out)
	}
	// Other management identities cannot inspect or revoke a locally owned lease.
	if status, _ := call(t, c, false, "GET", "/v1/tunnels/private", nil, "management-token"); status != 404 {
		t.Fatal(status)
	}
	// A tunnel capability never authenticates ordinary controller API calls.
	if status, _ := call(t, c, false, "GET", "/v1/status", nil, token); status != 401 {
		t.Fatal(status)
	}
}
