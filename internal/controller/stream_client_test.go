package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/client"
)

// interruptedStreamBridge simulates an intermediary ending one stream after delivering a log event.
type interruptedStreamBridge struct {
	backend string
	proxy   http.Handler
	dropped atomic.Bool
	resumed chan string
}

// ServeHTTP records cursor-bearing reconnects while ordinary API requests pass through unchanged.
func (b *interruptedStreamBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only event subscriptions participate in the deliberate connection interruption.
	if !strings.HasSuffix(r.URL.Path, "/events") {
		b.proxy.ServeHTTP(w, r)
		return
	}
	// Notify the test when the real client resumes using its last delivered cursor.
	if cursor := r.Header.Get("Last-Event-ID"); cursor != "" {
		// A buffered notification avoids blocking request handling on the test goroutine.
		select {
		// Deliver the cursor when the fixture observer has capacity.
		case b.resumed <- cursor:
		// Repeated reconnects must not stall the controller proxy.
		default:
		}
	}
	// Cut only the first subscription; subsequent streams use normal proxy behavior.
	if b.dropped.CompareAndSwap(false, true) {
		b.firstLog(w, r)
		return
	}
	b.proxy.ServeHTTP(w, r)
}

// firstLog forwards complete SSE frames through the first log, then ends the HTTP response.
func (b *interruptedStreamBridge) firstLog(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequestWithContext(r.Context(), "GET", b.backend+r.URL.RequestURI(), nil)
	req.Header = r.Header.Clone()
	response, err := http.DefaultClient.Do(req)
	// Surface a broken fixture connection as a proxy error rather than hiding it.
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.WriteHeader(response.StatusCode)
	forwardThroughLog(w, response.Body)
}

// forwardThroughLog flushes complete events before simulating a clean proxy stream timeout.
func forwardThroughLog(w http.ResponseWriter, body io.Reader) {
	reader := bufio.NewReader(body)
	logFrame := false
	// Read only the finite initial history produced by the controlled deployment fixture.
	for {
		line, err := reader.ReadString('\n')
		// Stop if the upstream subscription itself failed before the interruption point.
		if err != nil {
			return
		}
		_, _ = io.WriteString(w, line)
		logFrame = logFrame || strings.TrimSpace(line) == "event: log"
		// Flush at event boundaries so the real client receives an acknowledged cursor.
		if strings.TrimSpace(line) == "" {
			_ = http.NewResponseController(w).Flush()
			// Returning closes only this subscription, never the accepted deployment.
			if logFrame {
				return
			}
		}
	}
}

// newInterruptedStreamClient enrolls a real TLS client behind the controlled stream-cutting intermediary.
func newInterruptedStreamClient(t *testing.T, c *Controller) (*client.Client, <-chan string) {
	t.Helper()
	backend := httptest.NewServer(c.Handler())
	t.Cleanup(backend.Close)
	target, _ := url.Parse(backend.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	bridge := &interruptedStreamBridge{backend: backend.URL, proxy: proxy, resumed: make(chan string, 4)}
	server := httptest.NewTLSServer(bridge)
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "controller-ca.pem")
	// Trust only the fixture certificate through the production client's normal CA-file path.
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	token := c.credentials("stream-integration")["token"].(string)
	c.mu.Unlock()
	remote, err := client.New(client.Options{URL: server.URL, Token: token, CAFile: ca})
	// Exercise production TLS configuration instead of replacing the HTTP transport.
	if err != nil {
		t.Fatal(err)
	}
	return remote, bridge.resumed
}

// acceptStreamDeployment submits an approved deployment with a secret that must never enter SSE output.
func acceptStreamDeployment(t *testing.T, remote *client.Client, ctx context.Context, f *humanFixture) string {
	t.Helper()
	a := f.propose(t, "PUT", "/v1/secrets/key", map[string]any{"value": "stream-private-credential", "requestId": "stream-secret"})
	status, result := approveFixtureRequest(t, f, a)
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, result)
	}
	spec := project()
	spec.Secrets = map[string]string{"KEY": "key"}
	plan := streamRequest(t, remote, ctx, "POST", "/v1/plans", map[string]any{"spec": spec})
	a = f.propose(t, "POST", "/v1/plans/"+plan["id"].(string)+"/apply", map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": "stream-apply"})
	status, accepted := approveFixtureRequest(t, f, a)
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, accepted)
	}
	return accepted["operationId"].(string)
}

// streamRequest keeps setup failures distinct from reconnect and delivery assertions.
func streamRequest(t *testing.T, remote *client.Client, ctx context.Context, method, path string, body any) map[string]any {
	t.Helper()
	var result map[string]any
	// Require each authenticated setup request to succeed before starting observation.
	if err := remote.Request(ctx, method, path, body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// TestOperationClientResumesRealHTTPSStream verifies client/controller compatibility across a deliberate disconnect.
func TestOperationClientResumesRealHTTPSStream(t *testing.T) {
	runtime := &recordingRuntime{gate: make(chan struct{})}
	c := newTestController(t, t.TempDir(), runtime, nil)
	t.Cleanup(func() { closeController(t, c) }) // Release blocked deployment work if the test fails.
	remote, resumed := newInterruptedStreamClient(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := attachHumanFixture(t, c)
	f.token = remote.Token
	id := acceptStreamDeployment(t, remote, ctx, f)
	events := []client.OperationEvent{}
	done := make(chan error, 1)
	// Collect events on one goroutine; the main test reads them only after completion.
	go func() {
		// Record exactly what the production client delivers after replay deduplication.
		done <- remote.WatchOperation(ctx, id, "", func(event client.OperationEvent) error { events = append(events, event); return nil })
	}()
	// Require a real reconnect carrying a cursor before releasing the server-side job.
	select {
	// A cursor-bearing request proves the client reattached after the deliberate drop.
	case cursor := <-resumed:
		// Cursors must be scoped to the accepted operation rather than another stream.
		if !strings.HasPrefix(cursor, id+":") {
			t.Fatal("reconnect cursor belongs to another operation")
		}
	// Bound the regression even if reconnection is broken.
	case <-ctx.Done():
		t.Fatal("client did not resume interrupted stream")
	}
	close(runtime.gate)
	// A network disconnect must not cancel or duplicate the accepted deployment.
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertStreamDelivery(t, events)
	runtime.mu.Lock()
	calls := runtime.calls
	runtime.mu.Unlock()
	// Observing or reconnecting cannot submit another execution.
	if calls != 1 {
		t.Fatalf("deployment executed %d times", calls)
	}
}

// assertStreamDelivery checks replay uniqueness, final success and credential redaction together.
func assertStreamDelivery(t *testing.T, events []client.OperationEvent) {
	t.Helper()
	seen := map[string]bool{}
	terminal := ""
	// Inspect the actual delivered wire events after the watcher has completed.
	for _, event := range events {
		// Neither log nor operation snapshots may expose resolved secret values.
		if strings.Contains(string(event.Data), "stream-private-credential") {
			t.Fatal("credential leaked in SSE")
		}
		// Only log events carry replay identities; snapshots may legitimately repeat.
		if event.ID != "" {
			// Replayed events already consumed before the drop must be suppressed.
			if seen[event.ID] {
				t.Fatal("duplicate replayed log")
			}
			seen[event.ID] = true
		}
		// The terminal operation snapshot is the authoritative completion signal.
		if event.Event == "operation" {
			var operation Operation
			// Require valid snapshots rather than silently accepting an undecodable terminal result.
			if err := json.Unmarshal(event.Data, &operation); err != nil {
				t.Fatal(err)
			}
			terminal = operation.Status
		}
	}
	// Resumption must deliver both progress and the final durable success result.
	if len(seen) < 2 || terminal != "succeeded" {
		t.Fatalf("incomplete event stream: logs=%d status=%s", len(seen), terminal)
	}
}
