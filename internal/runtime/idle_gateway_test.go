package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/model"
)

// The public request is never a readiness probe. Two cold POSTs share one
// start, each body is delivered once, and a long request holds capacity open.
func TestIdleGatewayColdRequestsAndRestart(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	waking, ready := make(chan struct{}), make(chan struct{})
	var bodies, wakes, stops atomic.Int32
	backend := idleBackend(t, &bodies, started, release)
	defer backend.Close()
	r := &Runtime{options: Options{StateDir: t.TempDir()}}
	a := appRecord{Deployment: model.Deployment{Name: "web", Status: "running", RuntimeID: "container"}, Spec: model.Project{Name: "web", Route: "web.example.test", SleepAfter: "1s"}, OperationID: "revision", Upstream: strings.TrimPrefix(backend.URL, "http://")}
	// Seed the committed revision before exercising a timer transition.
	if err := r.save(state{Apps: map[string]appRecord{"web": a}}); err != nil {
		t.Fatal(err)
	}
	entry := &idleRoute{name: a.Name, host: "web.example.test", operation: a.OperationID, upstream: a.Upstream, after: time.Second, last: time.Now().Add(-time.Hour)}
	g := &idleGateway{runtime: r, ctx: context.Background(), routes: map[string]*idleRoute{"web.example.test": entry}}
	g.scale = testScale(&wakes, &stops, waking, ready)
	restoreSleepingEntry(t, g, a, stops.Load)
	done := coldRequests(g)
	waitSignal(t, waking, "wake did not start")
	// No customer request may reach the backend during readiness.
	if bodies.Load() != 0 {
		t.Fatal("forwarded before readiness")
	}
	close(ready)
	waitSignal(t, started, "first request was lost")
	g.tick()
	close(release)
	checkResponses(t, done)
	// The observed call counts distinguish a single shared start from retries or request replay.
	if bodies.Load() != 2 || wakes.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("bodies=%d wake=%d sleep=%d", bodies.Load(), wakes.Load(), stops.Load())
	}
	// A restart must not reapply a sleep intent that was successfully woken.
	if readSleeping(t, r) {
		t.Fatal("restart would restore stale sleep state")
	}
}

// Rebuild the transient route from the intent that survived an automatic stop.
func restoreSleepingEntry(t *testing.T, g *idleGateway, a appRecord, stops func() int32) {
	g.tick()
	// Verify the timer performed both the durable and external halves of sleep.
	if !readSleeping(t, g.runtime) || stops() != 1 {
		t.Fatal("sleep intent not retained")
	}
	g.routes["web.example.test"] = &idleRoute{name: a.Name, host: "web.example.test", operation: a.OperationID, upstream: a.Upstream, after: time.Second, sleeping: true}
}

// Read the persisted intent independently of the in-memory route.
func readSleeping(t *testing.T, r *Runtime) bool {
	t.Helper()
	s, err := r.read()
	// A missing state file is not evidence of a successful transition.
	if err != nil {
		t.Fatal(err)
	}
	return s.Apps["web"].IdleSleeping
}

// Bound synchronization failures so a regression cannot hang the suite.
func waitSignal(t *testing.T, c <-chan struct{}, message string) {
	t.Helper()
	// Wait for the expected fixture boundary or report the missing event.
	select {
	// The worker reached its readiness boundary.
	case <-c:
	// A missing event indicates a lost request or deadlock.
	case <-time.After(3 * time.Second):
		t.Fatal(message)
	}
}

// Start concurrent non-idempotent requests through the real proxy handler.
func coldRequests(g *idleGateway) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 2)
	// Both callers arrive while the same service is asleep.
	for i := 0; i < 2; i++ {
		// Give each request its own body, response writer and goroutine.
		go func() {
			req := httptest.NewRequest("POST", "http://web.example.test/work?q=1", strings.NewReader("payload"))
			out := httptest.NewRecorder()
			g.ServeHTTP(out, req)
			done <- out
		}()
	}
	return done
}

// Require one completed response per original request.
func checkResponses(t *testing.T, done <-chan *httptest.ResponseRecorder) {
	// Each caller should observe its own backend response.
	for i := 0; i < 2; i++ {
		result := <-done
		// A probe response or an intermediate 503 is not a successful cold request.
		if result.Code != 200 || result.Body.String() != "completed" {
			t.Fatal(result)
		}
	}
}

// Hold responses open long enough for an idle scan to encounter active requests.
func idleBackend(t *testing.T, bodies *atomic.Int32, started chan struct{}, release chan struct{}) *httptest.Server {
	// Capture the method, path and body at the real HTTP destination.
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Preserve POST semantics, query parameters and body.
		if r.Method != "POST" || string(body) != "payload" || r.URL.RequestURI() != "/work?q=1" {
			t.Error("request was changed")
		}
		// Notify the test only once while both streams remain open.
		if bodies.Add(1) == 1 {
			close(started)
		}
		<-release
		fmt.Fprint(w, "completed")
	}))
}

// Control readiness independently of the arriving requests.
func testScale(wakes, stops *atomic.Int32, waking, ready chan struct{}) func(context.Context, appRecord, bool) (string, error) {
	// Count simulated runtime starts and stops.
	return func(_ context.Context, app appRecord, awake bool) (string, error) {
		// Pause the shared wake until the test releases readiness.
		if awake {
			// A duplicate wake increments the counter without closing a channel twice.
			if wakes.Add(1) == 1 {
				close(waking)
			}
			<-ready
		} else {
			stops.Add(1)
		}
		return app.Upstream, nil
	}
}

// TestIdleK3sCapacity exercises scale, rollout and private readiness against a Kubernetes command fixture.
func TestIdleK3sCapacity(t *testing.T) {
	f, s := newHPAFixture(t)
	app := s.Apps["app"]
	// A live HTTP server confirms the Service path independently of the CLI fixture.
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer service.Close()
	app.Upstream = strings.TrimPrefix(service.URL, "http://")
	var scales []string
	// Kubernetes object reads use the fixture's stored ownership labels.
	f.runtime.options.KubeRun = func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		// Observe the exact requested replica count.
		if slices.Contains(args, "scale") {
			scales = append(scales, args[len(args)-1])
			return nil, nil
		}
		// The simulated rollout is ready while the private HTTP probe is real.
		if slices.Contains(args, "rollout") {
			return nil, nil
		}
		return f.run(ctx, args, input)
	}
	// The same revision loses all Pods, then regains one ready backend.
	for _, awake := range []bool{false, true} {
		// Both stop and wake must finish before verifying the recorded actions.
		if _, err := f.runtime.idleCapacity(context.Background(), app, awake); err != nil {
			t.Fatal(err)
		}
	}
	// Scale must never select the previous version or any other replica count.
	if !slices.Equal(scales, []string{"--replicas=0", "--replicas=1"}) {
		t.Fatal(scales)
	}
	delete(f.objects, "deployment/version")
	// Missing ownership cannot authorize another write.
	if _, err := f.runtime.idleCapacity(context.Background(), app, false); err == nil || len(scales) != 2 {
		t.Fatal("scaled a missing deployment")
	}
}

// TestIdleWakeFailureRetries ensures a failed first start does not consume a POST or wedge future wakes.
func TestIdleWakeFailureRetries(t *testing.T) {
	var posts, starts atomic.Int32
	// Only the successful request should reach the backend.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer backend.Close()
	r := &Runtime{options: Options{StateDir: t.TempDir()}}
	a := appRecord{Deployment: model.Deployment{Name: "web", Status: "running"}, Spec: model.Project{Route: "web.example.test", SleepAfter: "1m"}, OperationID: "first", IdleSleeping: true, Upstream: strings.TrimPrefix(backend.URL, "http://")}
	// Restore a sleeping revision with no assumption that its old backend is ready.
	if err := r.save(state{Apps: map[string]appRecord{"web": a}}); err != nil {
		t.Fatal(err)
	}
	g := &idleGateway{runtime: r, ctx: context.Background(), routes: map[string]*idleRoute{}}
	host, entry, err := g.route(a)
	// Build the same route that daemon startup would restore.
	if err != nil {
		t.Fatal(err)
	}
	g.routes[host] = entry
	// The first start fails after a partial runtime transition.
	g.scale = func(_ context.Context, _ appRecord, awake bool) (string, error) {
		// Force one readiness failure before the retry succeeds.
		if awake && starts.Add(1) == 1 {
			return "", errors.New("not ready")
		}
		return a.Upstream, nil
	}
	// Each response belongs to one distinct incoming POST.
	for _, code := range []int{http.StatusServiceUnavailable, http.StatusCreated} {
		reply := httptest.NewRecorder()
		g.ServeHTTP(reply, httptest.NewRequest("POST", "http://"+host+"/write", strings.NewReader("once")))
		// A failed wake must be visible to the original caller.
		if reply.Code != code {
			t.Fatalf("want %d, got %d", code, reply.Code)
		}
	}
	// Retried readiness must not replay the failed request.
	if posts.Load() != 1 || readSleeping(t, r) {
		t.Fatal("unexpected request replay or stale sleep intent")
	}
}
