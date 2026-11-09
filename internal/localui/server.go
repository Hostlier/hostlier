// Package localui serves a loopback dashboard backed by one authenticated controller subscription.
package localui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/model"
	"hostlier/internal/snapshot"
)

// assets are bundled into the standalone executable without a Node.js dependency.
//
//go:embed assets/*
var assets embed.FS

// Remote is the authenticated connection kept exclusively in the CLI process.
type Remote interface {
	// Request prepares or executes work under the controller's existing approval rules.
	Request(context.Context, string, string, any, any) error
	// WatchStatus supplies shared observations and transport availability.
	WatchStatus(context.Context, func(client.StatusEvent) error) error
}

// Server owns a local session and cache; it never persists controller credentials in the browser.
type Server struct {
	readMu        sync.Mutex
	pageReads     map[string]*readEntry
	readSlots     chan struct{}
	readContext   context.Context
	connectionMu  sync.Mutex
	remote        Remote
	token, origin string
	bus           *snapshot.Hub
	mu            sync.Mutex
	approvals     map[string]bool
	streams       int
}

// New binds a fresh unguessable local session to an already verified controller client.
func New(remote Remote, origin string) (*Server, error) {
	value := make([]byte, 32)
	// Session creation must fail closed when operating-system randomness is unavailable.
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}
	s := &Server{pageReads: make(map[string]*readEntry), readSlots: make(chan struct{}, 4), readContext: context.Background(), remote: remote, token: hex.EncodeToString(value), origin: origin, bus: snapshot.New(), approvals: map[string]bool{}}
	s.bus.Publish(`{"connected":false}`)
	return s, nil
}

// URL places the local session in a fragment, which is never sent in HTTP requests or referrers.
func (s *Server) URL() string { return s.origin + "/#session=" + s.token }

// Serve runs the local HTTP server and one upstream worker until invocation cancellation.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.readContext = ctx
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	workerDone := make(chan struct{})
	// Every browser tab shares this one upstream observer and local immutable cache.
	go func() { defer close(workerDone); s.observe(ctx) }()
	done := make(chan error, 1)
	// HTTP serving remains independent of runtime capture and upstream reconnect delays.
	go func() { done <- server.Serve(listener) }()
	// Cancellation shuts down only the local dashboard and its observation worker.
	select {
	// Unexpected listener failure is surfaced to the CLI.
	case err := <-done:
		cancel()
		<-workerDone
		return err
	// CLI cancellation never cancels accepted controller operations.
	case <-ctx.Done():
		cancel()
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		server.Shutdown(shutdown)
		server.Close()
		<-workerDone
		return nil
	}
}

// observe caches snapshots while retaining the last observation on connection loss.
func (s *Server) observe(ctx context.Context) {
	var latest *model.StatusSnapshot
	// This callback is invoked serially by the single upstream observer.
	err := s.remote.WatchStatus(ctx, func(event client.StatusEvent) error {
		// Disconnection is separate from the freshness of previously observed data.
		// Switching controllers discards observations from the previous authority.
		if event.Reset {
			latest = nil
		}
		// New samples replace cached values only after full upstream delivery.
		if event.Snapshot != nil {
			latest = event.Snapshot
		}
		data, err := json.Marshal(client.StatusEvent{Reset: event.Reset, Connected: event.Connected, Snapshot: latest})
		// Only complete encodable snapshots may replace the browser cache.
		if err == nil {
			s.bus.Publish(string(data))
		}
		return err
	})
	// Permanent authorization or protocol failures close the stream and remain visibly disconnected.
	if err != nil {
		data, _ := json.Marshal(client.StatusEvent{Connected: false, Snapshot: latest})
		s.bus.Publish(string(data))
	}
}

// Handler isolates static assets from authenticated local API access and rejects DNS rebinding.
func (s *Server) Handler() http.Handler {
	directory, _ := fs.Sub(assets, "assets")
	files := http.FileServer(http.FS(directory))
	// Security headers apply to both assets and local API responses.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		// An attacker-controlled hostname must not resolve into a privileged local dashboard.
		if "http://"+r.Host != s.origin {
			http.Error(w, "Invalid local host", http.StatusForbidden)
			return
		}
		// Asset requests never expose controller state or the local session token.
		if r.URL.Path == "/" || r.URL.Path == "/app.js" || r.URL.Path == "/style.css" {
			files.ServeHTTP(w, r)
			return
		}
		// Named pages share embedded assets while retaining real browser URLs and history.
		if dashboardPage(r) {
			clone := r.Clone(r.Context())
			copyURL := *r.URL
			copyURL.Path = "/"
			clone.URL = &copyURL
			files.ServeHTTP(w, clone)
			return
		}
		s.serveAPI(w, r)
	})
}

// authorized requires the fragment-derived session and exact same-origin authority for mutations.
func (s *Server) authorized(r *http.Request) bool {
	// Comparing fixed-length session values avoids accidental empty-token authority.
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Hostlier-UI")), []byte(s.token)) != 1 {
		return false
	}
	// Browsers send Origin on fetch mutations; accepting a foreign or absent origin permits CSRF.
	if r.Method != "GET" && r.Header.Get("Origin") != s.origin {
		return false
	}
	return true
}

// serveAPI provides a narrow dashboard contract instead of a general credential-bearing proxy.
func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	// Every data and action request requires the local session capability.
	if !s.authorized(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"error": model.Err("UI_SESSION_REQUIRED", "Your dashboard session has expired. Open the current link printed by hostlier ui.")})
		return
	}
	// Status comes exclusively from the shared cache, never another controller request.
	if r.Method == "GET" && r.URL.Path == "/api/events" {
		s.events(w, r)
		return
	}
	value, err := s.dashboardAPI(r)
	w.Header().Set("Content-Type", "application/json")
	// Public errors reveal neither controller credentials nor raw upstream errors.
	if err != nil {
		status := http.StatusBadRequest
		var public *model.Error
		// Known controller error codes retain their actionable safe messages.
		if !errors.As(err, &public) {
			public = &model.Error{Code: "UI_REQUEST", Message: "Dashboard request failed"}
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": public})
		return
	}
	json.NewEncoder(w).Encode(value)
}

// events fans out cache changes with bounded admission and independent slow-reader deadlines.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	// Local tabs cannot create an unbounded set of stalled subscriptions.
	if !s.reserveStream() {
		http.Error(w, "Too many viewers", http.StatusTooManyRequests)
		return
	}
	defer s.releaseStream()
	updates, unsubscribe := s.bus.Subscribe()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	// The initial replacement removes any need to replay intermediate local snapshots.
	if writeLocalSnapshot(w, s.bus.Latest()) != nil {
		return
	}
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	// Browser cancellation releases the subscription without affecting upstream capture.
	for {
		// Every wait observes the local HTTP request's lifecycle.
		select {
		// The viewer disconnected or the local server is closing.
		case <-r.Context().Done():
			return
		// Notify all tabs from the same upstream capture.
		case <-updates:
			// Slow readers release capacity instead of holding a worker indefinitely.
			if writeLocalSnapshot(w, s.bus.Latest()) != nil {
				return
			}
		// Heartbeats bound stalled writes and let browsers detect a closed local process.
		case <-heartbeat.C:
			// Repeating immutable state has no controller cost.
			if writeLocalSnapshot(w, s.bus.Latest()) != nil {
				return
			}
		}
	}
}

// reserveStream bounds browser subscriptions independently of upstream observation.
func (s *Server) reserveStream() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A slow or abandoned set of tabs must not consume unbounded handler goroutines.
	if s.streams >= 32 {
		return false
	}
	s.streams++
	return true
}

// releaseStream frees local capacity on every streaming exit path.
func (s *Server) releaseStream() { s.mu.Lock(); s.streams--; s.mu.Unlock() }

// writeLocalSnapshot bounds writes without holding any cache or authoritative state lock.
func writeLocalSnapshot(w http.ResponseWriter, data string) error {
	control := http.NewResponseController(w)
	// A stalled browser must not occupy a goroutine indefinitely.
	if err := control.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	// JSON is already encoded without literal newlines, preserving SSE framing.
	if _, err := io.WriteString(w, "event: status\ndata: "+data+"\n\n"); err != nil {
		return err
	}
	return control.Flush()
}
