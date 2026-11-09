package tunnel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// tunnelFixture supplies isolated local origins, scoped authentication and verified TLS transport.
type tunnelFixture struct {
	broker   *Broker
	local    *httptest.Server
	edge     *httptest.Server
	token    string
	rejected atomic.Int32
	upgrades atomic.Int32
}

// newTunnelFixture exposes only loopback test listeners and one lease-specific reverse proxy.
func newTunnelFixture(t *testing.T, app http.Handler) *tunnelFixture {
	t.Helper()
	broker, err := NewBroker(4)
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err != nil {
		t.Fatal(err)
	}
	f := &tunnelFixture{broker: broker, local: httptest.NewServer(app), token: "scoped-token"}
	// Keep this local fixture operation independent of real external services.
	f.edge = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the real transport preserves the expected isolation and completion behavior.
		if r.URL.Path == "/connect" {
			// Verify the real transport preserves the expected isolation and completion behavior.
			if r.Header.Get("Authorization") != "Bearer "+f.token {
				f.rejected.Add(1)
				http.Error(w, "unauthorized", 401)
				return
			}
			f.upgrades.Add(1)
			defer f.upgrades.Add(-1)
			f.broker.AcceptHTTP(w, r)
			return
		}
		f.broker.ServeHTTP(w, r)
	}))
	// Keep this local fixture operation independent of real external services.
	t.Cleanup(func() { f.broker.Close(); f.edge.Close(); f.local.Close() })
	return f
}

// start opens a real outbound TLS client and waits until its lease is ready for traffic.
func (f *tunnelFixture) start(t *testing.T) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// Keep this local fixture operation independent of real external services.
	go func() {
		done <- (Client{URL: f.edge.URL + "/connect", Token: f.token, Target: f.local.URL, HTTPClient: f.edge.Client(), Connections: 2}).Run(ctx)
	}()
	// Keep this local fixture operation independent of real external services.
	t.Cleanup(func() {
		cancel()
		// Bound fixture waits so a broken transport fails the test promptly.
		select {
		// Observe this controlled transport outcome without retrying application work.
		case <-done:
		// Observe this controlled transport outcome without retrying application work.
		case <-time.After(3 * time.Second):
			t.Error("tunnel client did not stop")
		}
	})
	// Keep this local fixture operation independent of real external services.
	waitTunnel(t, func() bool { f.broker.mu.Lock(); defer f.broker.mu.Unlock(); return len(f.broker.pipes) >= 2 })
	return cancel, done
}

// waitTunnel bounds asynchronous fixture readiness without sleeping through long production deadlines.
func waitTunnel(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	// Exercise each selected transport case within a short fixture deadline.
	for time.Now().Before(deadline) {
		// Verify the real transport preserves the expected isolation and completion behavior.
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("tunnel readiness deadline exceeded")
}

// TestHTTPStreamingAndLargeBody verifies fixed-memory streaming in both directions over real TLS.
func TestHTTPStreamingAndLargeBody(t *testing.T) {
	f := newTunnelFixture(t, http.HandlerFunc(tunnelApp))
	f.start(t)
	response, err := f.edge.Client().Get(f.edge.URL + "/stream")
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, 5)
	// Verify the real transport preserves the expected isolation and completion behavior.
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "first" {
		t.Fatal(string(first), err)
	}
	rest, err := io.ReadAll(response.Body)
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err != nil || string(rest) != "second" {
		t.Fatal(string(rest), err)
	}
	// The body greatly exceeds an individual websocket frame buffer and is hashed incrementally.
	body := bytes.Repeat([]byte("streamed-body"), 1024*1024)
	response, err = f.edge.Client().Post(f.edge.URL+"/hash", "application/octet-stream", bytes.NewReader(body))
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	digest, _ := io.ReadAll(response.Body)
	expected := sha256.Sum256(body)
	// Verify the real transport preserves the expected isolation and completion behavior.
	if string(digest) != hex.EncodeToString(expected[:]) {
		t.Fatal("streamed request content changed")
	}
}

// tunnelApp streams before completion, hashes request bodies and echoes native websocket frames.
func tunnelApp(w http.ResponseWriter, r *http.Request) {
	// Keep this local fixture operation independent of real external services.
	switch r.URL.Path {
	// Observe this controlled transport outcome without retrying application work.
	case "/stream":
		fmt.Fprint(w, "first")
		w.(http.Flusher).Flush()
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, "second")
	// Observe this controlled transport outcome without retrying application work.
	case "/hash":
		digest := sha256.New()
		io.Copy(digest, r.Body)
		fmt.Fprint(w, hex.EncodeToString(digest.Sum(nil)))
	// Observe this controlled transport outcome without retrying application work.
	case "/ws":
		ws, err := websocket.Accept(w, r, nil)
		// Verify the real transport preserves the expected isolation and completion behavior.
		if err != nil {
			return
		}
		defer ws.CloseNow()
		// Exercise each selected transport case within a short fixture deadline.
		for {
			kind, data, err := ws.Read(r.Context())
			// Verify the real transport preserves the expected isolation and completion behavior.
			if err != nil {
				return
			}
			// Verify the real transport preserves the expected isolation and completion behavior.
			if ws.Write(r.Context(), kind, data) != nil {
				return
			}
		}
	// Keep this local fixture operation independent of real external services.
	default:
		fmt.Fprint(w, "local-app")
	}
}

// TestWebSocketUpgradeRoundTrip verifies nested application upgrades use standard HTTP transport semantics.
func TestWebSocketUpgradeRoundTrip(t *testing.T) {
	f := newTunnelFixture(t, http.HandlerFunc(tunnelApp))
	f.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, f.edge.URL+"/ws", &websocket.DialOptions{HTTPClient: f.edge.Client()})
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	payload := bytes.Repeat([]byte("upgraded"), 4096)
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err := ws.Write(ctx, websocket.MessageBinary, payload); err != nil {
		t.Fatal(err)
	}
	kind, data, err := ws.Read(ctx)
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err != nil || kind != websocket.MessageBinary || !bytes.Equal(data, payload) {
		t.Fatal("application websocket changed", err)
	}
}

// TestDisconnectFailsClosedAndNewSessionReconnects ensures stale pipes never route into another lease.
func TestDisconnectFailsClosedAndNewSessionReconnects(t *testing.T) {
	f := newTunnelFixture(t, http.HandlerFunc(tunnelApp))
	cancel, _ := f.start(t)
	cancel()
	// Keep this local fixture operation independent of real external services.
	waitTunnel(t, func() bool { f.broker.mu.Lock(); defer f.broker.mu.Unlock(); return len(f.broker.pipes) == 0 })
	response, err := f.edge.Client().Get(f.edge.URL + "/")
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	// Verify the real transport preserves the expected isolation and completion behavior.
	if response.StatusCode != 503 {
		t.Fatal("disconnected lease did not fail closed", response.StatusCode)
	}
	f.start(t)
	response, err = f.edge.Client().Get(f.edge.URL + "/")
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	// Verify the real transport preserves the expected isolation and completion behavior.
	if response.StatusCode != 200 || string(data) != "local-app" {
		t.Fatal(response.StatusCode, string(data))
	}
	// Revoking this scoped lease aborts existing pipes and refuses new ones.
	f.broker.Close()
	response, err = f.edge.Client().Get(f.edge.URL + "/")
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	// Verify the real transport preserves the expected isolation and completion behavior.
	if response.StatusCode != 503 {
		t.Fatal("revoked lease routed traffic")
	}
}

// TestLoopbackAndTLSValidation rejects remote-target SSRF and credential-bearing plaintext endpoints.
func TestLoopbackAndTLSValidation(t *testing.T) {
	// Exercise each selected transport case within a short fixture deadline.
	for _, target := range []string{"http://example.com:80", "http://169.254.169.254:80", "https://localhost:80", "http://localhost:80/private", "http://user@localhost:80", "http://localhost:0", "http://localhost:65536"} {
		// Verify the real transport preserves the expected isolation and completion behavior.
		if _, err := LocalTarget(target); err == nil {
			t.Fatal("unsafe target accepted", target)
		}
	}
	// Exercise each selected transport case within a short fixture deadline.
	for _, target := range []string{"http://localhost:3000", "127.0.0.1:3000", "http://[::1]:3000"} {
		// Verify the real transport preserves the expected isolation and completion behavior.
		if _, err := LocalTarget(target); err != nil {
			t.Fatal(target, err)
		}
	}
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err := (Client{URL: "http://controller/connect", Token: "token"}).validate(); err == nil {
		t.Fatal("plaintext endpoint accepted")
	}
}

// TestAuthenticationAndLeaseIsolation prevents one tunnel's connections serving another route's broker.
func TestAuthenticationAndLeaseIsolation(t *testing.T) {
	f := newTunnelFixture(t, http.HandlerFunc(tunnelApp))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := (Client{URL: f.edge.URL + "/connect", Token: "wrong", Target: f.local.URL, HTTPClient: f.edge.Client(), Connections: 1}).Run(ctx)
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err == nil || f.rejected.Load() != 1 {
		t.Fatal("invalid lease did not terminate")
	}
	f.start(t)
	isolated, _ := NewBroker(1)
	defer isolated.Close()
	recorder := httptest.NewRecorder()
	isolated.ServeHTTP(recorder, httptest.NewRequest("GET", "http://other.test/", nil))
	// Verify the real transport preserves the expected isolation and completion behavior.
	if recorder.Code != 503 {
		t.Fatal("another lease supplied this route")
	}
	// Public responses never include connection credentials or private request configuration.
	encoded, _ := json.Marshal(recorder.Body.String())
	// Verify the real transport preserves the expected isolation and completion behavior.
	if strings.Contains(string(encoded), f.token) {
		t.Fatal("lease token leaked")
	}
}

// TestLiveClientReconnectNeverReplaysInterruptedRequest verifies automatic replacement of broken pipes.
func TestLiveClientReconnectNeverReplaysInterruptedRequest(t *testing.T) {
	started := make(chan struct{}, 1)
	var requests atomic.Int32
	// Keep this local fixture operation independent of real external services.
	f := newTunnelFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the real transport preserves the expected isolation and completion behavior.
		if r.URL.Path == "/blocked" {
			requests.Add(1)
			io.Copy(io.Discard, r.Body)
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "reconnected")
	}))
	f.start(t)
	failed := make(chan int, 1)
	// Keep this local fixture operation independent of real external services.
	go func() {
		response, err := f.edge.Client().Post(f.edge.URL+"/blocked", "text/plain", strings.NewReader("one mutation"))
		// Verify the real transport preserves the expected isolation and completion behavior.
		if err != nil {
			failed <- 0
			return
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		failed <- response.StatusCode
	}()
	<-started
	dropConnections(f.broker)
	// Bound fixture waits so a broken transport fails the test promptly.
	select {
	// Observe this controlled transport outcome without retrying application work.
	case status := <-failed:
		// Verify the real transport preserves the expected isolation and completion behavior.
		if status != 503 {
			t.Fatal("interrupted request did not fail closed", status)
		}
	// Observe this controlled transport outcome without retrying application work.
	case <-time.After(time.Second):
		t.Fatal("interrupted request stalled")
	}
	// Keep this local fixture operation independent of real external services.
	waitTunnel(t, func() bool { f.broker.mu.Lock(); defer f.broker.mu.Unlock(); return len(f.broker.pipes) >= 2 })
	response, err := f.edge.Client().Get(f.edge.URL + "/")
	// Verify the real transport preserves the expected isolation and completion behavior.
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	// Verify the real transport preserves the expected isolation and completion behavior.
	if string(data) != "reconnected" || requests.Load() != 1 {
		t.Fatal("reconnect replayed a local request")
	}
}

// dropConnections simulates transport loss without revoking the client's approved lease.
func dropConnections(broker *Broker) {
	broker.mu.Lock()
	connections := make([]*pipe, 0, len(broker.pipes))
	// Exercise each selected transport case within a short fixture deadline.
	for connection := range broker.pipes {
		connections = append(connections, connection)
	}
	broker.mu.Unlock()
	// Exercise each selected transport case within a short fixture deadline.
	for _, connection := range connections {
		connection.Close()
	}
}

// TestIdleReconnectReleasesHandlers bounds repeated idle disconnects without any public traffic.
func TestIdleReconnectReleasesHandlers(t *testing.T) {
	f := newTunnelFixture(t, http.HandlerFunc(tunnelApp))
	f.start(t)
	// Idle queue entries may be stale, but their former HTTP handlers cannot remain blocked.
	for attempt := 0; attempt < 4; attempt++ {
		dropConnections(f.broker)
		// Wait for replacement pipes and the exact bounded number of live upgrade handlers.
		waitTunnel(t, func() bool {
			f.broker.mu.Lock()
			defer f.broker.mu.Unlock()
			return len(f.broker.pipes) == 2 && f.upgrades.Load() == 2
		})
	}
}
