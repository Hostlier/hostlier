package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/model"
)

func TestPublicReadinessTarget(t *testing.T) {
	legacy := model.Project{Version: 1, Route: "app.example.com/api"}
	if target, health := publicReadinessTarget(legacy, legacy); target != "https://app.example.com/api" || health {
		t.Fatalf("legacy route was not normalized: %s, %v", target, health)
	}
	for _, tc := range []struct {
		name, route, path, want string
		strip, health           bool
	}{
		{"root", "https://app.example.com", "/ready", "https://app.example.com/ready", false, true},
		{"legacy", "app.example.com", "/ready", "https://app.example.com/ready", false, true},
		{"stripped", "https://app.example.com/api", "/ready", "https://app.example.com/api/ready", true, true},
		{"preserved", "https://app.example.com/api", "/api/ready", "https://app.example.com/api/ready", false, true},
		{"private health path", "https://app.example.com/api", "/ready", "https://app.example.com/api", false, false},
		{"segment boundary", "https://app.example.com/api", "/apiculture", "https://app.example.com/api", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := model.Project{Port: 8080, Route: tc.route, Health: &model.Health{Path: tc.path}, Proxy: &model.Proxy{StripPrefix: tc.strip}}
			if target, health := publicReadinessTarget(p, p); target != tc.want || health != tc.health {
				t.Fatalf("got %s, health=%v", target, health)
			}
			secondary := p
			secondary.Port = 8081
			wantRoute := tc.route
			if tc.name == "legacy" {
				wantRoute = "https://" + wantRoute
			}
			if target, health := publicReadinessTarget(p, secondary); target != wantRoute || health {
				t.Fatalf("used primary health path on another listener: %s, %v", target, health)
			}
		})
	}
}

func TestPublicReadinessUsesFreshConnections(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP1", true: "HTTP2"}[http2], func(t *testing.T) {
			testPublicReadinessConnections(t, http2)
		})
	}
}

func testPublicReadinessConnections(t *testing.T, http2 bool) {
	var connections sync.Map
	var count atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if (req.ProtoMajor == 2) != http2 {
			t.Errorf("unexpected probe protocol %s", req.Proto)
		}
		if _, reused := connections.LoadOrStore(req.RemoteAddr, true); reused {
			t.Error("readiness reused a connection to a potentially retiring proxy worker")
		}
		count.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = http2
	server.StartTLS()
	defer server.Close()
	r := &Runtime{web: server.Client(), options: Options{HealthTimeout: 10 * time.Second}}
	if err := r.publicProbeReady(context.Background(), server.URL, true); err != nil || count.Load() != 3 {
		t.Fatalf("readiness: %v, requests: %d", err, count.Load())
	}
}

func TestPublicHealthDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/login" {
			redirected.Add(1)
			return
		}
		http.Redirect(w, req, "/login", http.StatusFound)
	}))
	defer server.Close()
	r := &Runtime{web: server.Client(), options: Options{HealthTimeout: 1500 * time.Millisecond}}
	if err := r.publicProbeReady(context.Background(), server.URL, true); err == nil {
		t.Fatal("login page reported as healthy application")
	}
	if redirected.Load() != 0 {
		t.Fatal("followed a health redirect")
	}
}
