package localui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// uiRemote records exact controller calls while exposing a single stable upstream observation.
type uiRemote struct {
	watches atomic.Int32
	mu      sync.Mutex
	paths   []string
	intent  map[string]any
}

// WatchStatus provides one observation and then waits for the local UI's cancellation.
func (r *uiRemote) WatchStatus(ctx context.Context, consume func(client.StatusEvent) error) error {
	r.watches.Add(1)
	// A worker callback publishes complete immutable public metadata.
	if err := consume(client.StatusEvent{Connected: true, Snapshot: &model.StatusSnapshot{ID: "epoch:1", Hostname: "test.example.com", ObservedAt: time.Now().UTC()}}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

// Request captures proposals without granting any browser request direct mutation authority.
func (r *uiRemote) Request(_ context.Context, method, path string, body, result any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, method+" "+path)
	value := any(map[string]any{"operationId": "operation-1"})
	// Proposal responses contain a real HTTPS review locator and no credentials.
	if path == "/v1/approvals" {
		r.intent = body.(map[string]any)
		value = approvalView{ID: "approval-1", Status: "pending", Code: "ABCDEF0123456789", ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
	}
	data, _ := json.Marshal(value)
	return json.Unmarshal(data, result)
}

// testUIServer creates a handler fixture without requiring real network access.
func testUIServer(t *testing.T) (*Server, *uiRemote) {
	t.Helper()
	remote := &uiRemote{}
	server, err := New(remote, "http://127.0.0.1:7777")
	// Random session creation must succeed before testing local access decisions.
	if err != nil {
		t.Fatal(err)
	}
	return server, remote
}

// uiCall exercises the same-origin local contract with optional browser capability headers.
func uiCall(s *Server, method, path, body, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, s.origin+path, strings.NewReader(body))
	r.Header.Set("X-Hostlier-UI", token)
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// TestLocalUIRejectsForeignAuthorityAndProxying guards the credential-bearing local bridge.
func TestLocalUIRejectsForeignAuthorityAndProxying(t *testing.T) {
	s, remote := testUIServer(t)
	w := uiCall(s, "POST", "/api/actions", `{"name":"sample","action":"restart"}`, "", s.origin)
	// An arbitrary webpage cannot act through the loopback listener without the local session.
	if w.Code != 403 {
		t.Fatal("anonymous action accepted")
	}
	w = uiCall(s, "POST", "/api/actions", `{"name":"sample","action":"restart"}`, s.token, "https://attacker.example")
	// Even a session-bearing mutation must come from the exact UI origin.
	if w.Code != 403 {
		t.Fatal("foreign origin accepted")
	}
	r := httptest.NewRequest("GET", "http://attacker.example/", nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	// DNS rebinding cannot serve a privileged dashboard under an attacker hostname.
	if w.Code != 403 {
		t.Fatal("foreign Host accepted")
	}
	w = uiCall(s, "POST", "/v1/apps/sample/action", `{}`, s.token, s.origin)
	// The narrow bridge never becomes an arbitrary controller proxy.
	if w.Code == 200 || len(remote.paths) != 0 {
		t.Fatal("unexpected upstream authority")
	}
}

// TestLocalUIActionsUseOnlyRetainedApprovals checks proposal and execution path separation.
func TestLocalUIActionsUseOnlyRetainedApprovals(t *testing.T) {
	s, remote := testUIServer(t)
	w := uiCall(s, "POST", "/api/actions", `{"name":"sample","action":"restart"}`, s.token, s.origin)
	// The UI prepares an independently approvable request and returns its terminal review locator.
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ABCDEF0123456789") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = uiCall(s, "POST", "/api/approvals/approval-1/execute", `{}`, s.token, s.origin)
	// Only the exact retained receipt can reach the controller's independent execution gate.
	if w.Code != 200 || len(remote.paths) != 2 {
		t.Fatal("approval execution failed")
	}
	// A UI click never directly invokes the application lifecycle endpoint.
	if remote.paths[0] != "POST /v1/approvals" || remote.paths[1] != "POST /v1/approvals/approval-1/execute" {
		t.Fatal(remote.paths)
	}
	// Intent must retain the exact name and action reviewed by the user.
	if remote.intent["path"] != "/v1/apps/sample/action" || remote.intent["body"].(map[string]any)["action"] != "restart" {
		t.Fatal(remote.intent)
	}
}

// TestLocalUIRejectsUnknownOrDestructiveIntent limits the local API before sending network requests.
func TestLocalUIRejectsUnknownOrDestructiveIntent(t *testing.T) {
	s, remote := testUIServer(t)
	// Neither path injection, unsupported destructive actions nor extra fields can acquire a proposal.
	for _, body := range []string{`{"name":"../sample","action":"restart"}`, `{"name":"sample","action":"delete"}`, `{"name":"sample","action":"stop","purge":true}`, `{"name":"sample","action":"start"} {}`, `{"name":"sample","action":"start"}` + strings.Repeat(" ", 5000) + `{}`} {
		w := uiCall(s, "POST", "/api/actions", body, s.token, s.origin)
		// Invalid browser intent never reaches the controller.
		if w.Code == 200 {
			t.Fatal("invalid intent accepted", body)
		}
	}
	w := uiCall(s, "POST", "/api/approvals/another-session/execute", `{}`, s.token, s.origin)
	// Session isolation is enforced before forwarding arbitrary approval IDs.
	if w.Code == 200 || len(remote.paths) != 0 {
		t.Fatal("unknown approval forwarded")
	}
}

// TestLocalUIBrowsersShareOneUpstream verifies real streaming fanout and clean worker shutdown.
func TestLocalUIBrowsersShareOneUpstream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	// Listener setup must precede upstream worker startup.
	if err != nil {
		t.Fatal(err)
	}
	remote := &uiRemote{}
	s, err := New(remote, "http://"+listener.Addr().String())
	// A fixture session must be available before viewers connect.
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	// The production lifecycle owns one observer, regardless of browser count.
	go func() { done <- s.Serve(ctx, listener) }()
	first := openBrowserStream(t, s)
	defer first.Close()
	second := openBrowserStream(t, s)
	defer second.Close()
	// Two local streaming clients cannot multiply runtime collection or controller requests.
	if remote.watches.Load() != 1 || len(remote.paths) != 0 {
		t.Fatal("browser connections duplicated upstream work")
	}
	first.Close()
	second.Close()
	cancel()
	// Cancellation must release both HTTP and upstream worker goroutines promptly.
	select {
	// A completed shutdown must report no listener or worker failure.
	case err := <-done:
		// Unexpected listener errors cannot be hidden by cancellation.
		if err != nil {
			t.Fatal(err)
		}
	// Bound shutdown time to catch retained upstream observers.
	case <-time.After(time.Second):
		t.Fatal("UI shutdown leaked a worker")
	}
}

// openBrowserStream reads a real initial cached SSE frame with the browser's local capability.
func openBrowserStream(t *testing.T, s *Server) io.ReadCloser {
	t.Helper()
	r, _ := http.NewRequest("GET", s.origin+"/api/events", nil)
	r.Header.Set("X-Hostlier-UI", s.token)
	response, err := (&http.Client{Timeout: time.Second}).Do(r)
	// A successful response confirms the listener, Host and session boundaries were all satisfied.
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	// Frames contain cache updates, not serialized controller credentials.
	if err != nil || line != "event: status\n" {
		response.Body.Close()
		t.Fatal("invalid local event", line, err)
	}
	return response.Body
}

// TestExpiredUISessionReturnsStructuredError avoids frontend parser failures after a CLI restart.
func TestExpiredUISessionReturnsStructuredError(t *testing.T) {
	s, _ := testUIServer(t)
	response := uiCall(s, "GET", "/api/deployments", "", "expired-session", s.origin)
	var envelope struct {
		Error model.Error `json:"error"`
	}
	// Authentication failure is JSON even before ordinary endpoint dispatch is authorized.
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	// Session recovery instructions never leak the current capability into an unauthenticated response.
	if response.Code != 403 || envelope.Error.Code != "UI_SESSION_REQUIRED" || strings.Contains(response.Body.String(), s.token) {
		t.Fatal("invalid expired-session response")
	}
}
