package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/model"
)

// lifecycleFixture provides trusted TLS routing and immediately ready Kubernetes rollouts.
func lifecycleFixture(t *testing.T, handler http.HandlerFunc) *K3s {
	t.Helper()
	// Serve the minimal Caddy configuration and accept route reloads.
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	t.Cleanup(edge.Close)
	public := httptest.NewTLSServer(handler)
	t.Cleanup(public.Close)
	// The injected cluster reports completed commands; public routing remains independently gated.
	adapter, e := New(Options{Type: "k3s", StateDir: t.TempDir(), CaddyAdmin: edge.URL, HealthTimeout: 2 * time.Second, KubeRun: func(context.Context, []string, []byte) ([]byte, error) { return nil, nil }})
	// Require an isolated runtime using the fixture's trusted TLS client.
	if e != nil {
		t.Fatal(e)
	}
	k := adapter.(*K3s)
	k.base.web = public.Client()
	app := appRecord{Deployment: model.Deployment{Name: "app", RuntimeID: "version", URL: public.URL, Status: "stopped"}, Spec: model.Project{Name: "app", Route: "app.example.com"}, Upstream: "127.0.0.1:3000"}
	// Seed durable state before running a start or restart transition.
	if e = k.base.save(state{Apps: map[string]appRecord{"app": app}}); e != nil {
		t.Fatal(e)
	}
	return k
}

// TestK3sLifecycleWaitsForHTTPS covers service propagation after completed start and restart rollouts.
func TestK3sLifecycleWaitsForHTTPS(t *testing.T) {
	// Both lifecycle transitions must wait for the publicly routed workload.
	for _, action := range []string{"start", "restart"} {
		// Exercise each transition with an independently unavailable public route.
		t.Run(action, func(t *testing.T) {
			var requests atomic.Int32
			// The first completed TLS request still sees an unavailable service endpoint.
			k := lifecycleFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				// Report a proxy failure until the next probe observes the usable route.
				if requests.Add(1) == 1 {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				w.WriteHeader(http.StatusOK)
			})
			e := k.Action(context.Background(), "app", action, model.ActionContext{})
			// Success requires a second TLS request after the initial 502.
			if e != nil || requests.Load() < 2 {
				t.Fatalf("action returned before HTTPS readiness: %v, probes %d", e, requests.Load())
			}
		})
	}
}

// TestK3sLifecycleHTTPSFailurePreservesState rejects success when routing never becomes usable.
func TestK3sLifecycleHTTPSFailurePreservesState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel after the unavailable public route is actually probed.
	k := lifecycleFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway); cancel() })
	e := k.Action(ctx, "app", "start", model.ActionContext{})
	// Cancellation must propagate instead of reporting a successful start.
	if e == nil {
		t.Fatal("unavailable public route reported success")
	}
	s, e := k.base.read()
	// A failed route verification cannot persist the optimistic running status.
	if e != nil || s.Apps["app"].Status != "stopped" {
		t.Fatalf("failed readiness changed state: %+v %v", s, e)
	}
}

// TestK3sLifecycleRejectsUntrustedTLS ensures lifecycle readiness retains certificate validation.
func TestK3sLifecycleRejectsUntrustedTLS(t *testing.T) {
	// The fixture endpoint is reachable, but its certificate is absent from system trust.
	k := lifecycleFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	k.base.web = &http.Client{Timeout: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	e := k.Action(ctx, "app", "restart", model.ActionContext{})
	// A completed rollout cannot override an untrusted public certificate.
	if e == nil {
		t.Fatal("untrusted TLS reported successful restart")
	}
}
