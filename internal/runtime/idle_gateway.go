package runtime

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

const idleAddress = "127.0.0.1:7465"

// The public edge owns TLS and route matching. Only opted-in application
// traffic passes through this loopback listener. Host selects a retained
// deployment, never a request-supplied backend address.
type idleGateway struct {
	mu      sync.Mutex
	runtime *Runtime
	scale   func(context.Context, appRecord, bool) (string, error)
	ctx     context.Context
	routes  map[string]*idleRoute
	server  *http.Server
	workers sync.WaitGroup
}

// Each route tracks in-flight requests and the completion time of the last one.
type idleRoute struct {
	name, host, operation, upstream string
	after                           time.Duration
	last                            time.Time
	active                          int
	sleeping, stopped               bool
	wake                            *idleWake
}

// All cold requests for one deployment wait on the same completion signal.
type idleWake struct {
	done chan struct{}
	err  error
}

// StartIdleGateway restores retained sleep intent before the controller accepts
// traffic. The listener is opened only when an application delegates idle scaling.
func StartIdleGateway(ctx context.Context, adapter model.Runtime) (func(), error) {
	var r *Runtime
	var scale func(context.Context, appRecord, bool) (string, error)
	// Both engines serialize idle changes with their ordinary runtime mutation lock.
	switch a := adapter.(type) {
	// Docker stops and restarts the retained container.
	case *Runtime:
		r, scale = a, a.idleCapacity
	// Kubernetes scales the retained Deployment and preserves its Service.
	case *K3s:
		r, scale = a.base, a.idleCapacity
	// Unknown adapters cannot safely own a cold start.
	default:
		return nil, errors.New("runtime does not support idle scaling")
	}
	ctx, cancel := context.WithCancel(ctx)
	g := &idleGateway{runtime: r, scale: scale, ctx: ctx, routes: map[string]*idleRoute{}}
	r.mu.Lock()
	r.idle = g
	s, err := r.read()
	// Restore committed routes even when every opted-in app was asleep at shutdown.
	if err == nil {
		err = r.syncIdleRoutes(s)
	}
	r.mu.Unlock()
	// Join capacity workers before releasing the controller state directory.
	closeGateway := func() {
		cancel()
		g.mu.Lock()
		// No listener exists on a host that never used sleepAfter.
		if g.server != nil {
			_ = g.server.Close()
		}
		g.mu.Unlock()
		g.workers.Wait()
	}
	// An invalid journal or occupied listener must fail startup visibly.
	if err != nil {
		closeGateway()
		return nil, err
	}
	return closeGateway, nil
}

// syncIdleRoutes runs under the runtime mutation lock for the same candidate
// snapshot that the edge publishes. Rollback reinstates the prior snapshot.
func (r *Runtime) syncIdleRoutes(s state) error {
	g := r.idle
	// Embedded adapters can serve ordinary deployments without starting a gateway.
	if g == nil {
		// Look only for applications that would require the missing listener.
		for _, a := range s.Apps {
			// Fail publication before routing requests to an unowned address.
			if a.Spec.SleepAfter != "" {
				return errors.New("idle gateway is unavailable")
			}
		}
		return nil
	}
	return g.publish(s)
}

// publish retains request counters across unrelated deployments.
func (g *idleGateway) publish(s state) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	next := map[string]*idleRoute{}
	// Rebuild from the same candidate set supplied to the public edge.
	for _, a := range s.Apps {
		// Manual stops and deletions withdraw demand-based activation.
		if a.Spec.SleepAfter == "" || a.Status != "running" {
			continue
		}
		host, entry, err := g.route(a)
		// Malformed route metadata must not partially replace the previous map.
		if err != nil {
			return err
		}
		next[host] = entry
	}
	// Bind before enabling the first idle route in Nginx or Caddy.
	if g.server == nil && len(next) != 0 {
		listener, err := net.Listen("tcp", idleAddress)
		// An occupied port cannot be adopted as a trusted workload gateway.
		if err != nil {
			return err
		}
		g.server = &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
		go g.server.Serve(listener)
		g.workers.Add(1)
		go g.monitor()
	}
	g.routes = next
	return nil
}

// A new revision begins a fresh idle window. Failed or interrupted sleeps
// remain eligible for reconciliation until their capacity is confirmed.
func (g *idleGateway) route(a appRecord) (string, *idleRoute, error) {
	address, err := config.ParseRoute(a.Spec.Route, nil, "")
	// Route matching uses the canonical public hostname.
	if err != nil {
		return "", nil, err
	}
	after, err := time.ParseDuration(a.Spec.SleepAfter)
	// Retained settings must still parse after a controller restart.
	if err != nil {
		return "", nil, err
	}
	entry := g.routes[address.Hostname]
	// Keep counters on edge reload, but isolate distinct deployment revisions.
	if entry == nil || entry.operation != a.OperationID || entry.name != a.Name {
		entry = &idleRoute{name: a.Name, host: address.Hostname, operation: a.OperationID, after: after, last: time.Now()}
	}
	entry.upstream, entry.sleeping = a.Upstream, a.IdleSleeping
	// A manual start clears the prior stop confirmation.
	if !entry.sleeping {
		entry.stopped = false
	}
	return address.Hostname, entry, nil
}

// Restore the live route table when an edge publication fails before activation.
func (r *Runtime) checkIdlePublication(err error) error {
	// Only a failed publish could have left a staged idle route behind.
	if err == nil || r.idle == nil {
		return err
	}
	prior, readErr := r.read()
	// Unreadable runtime state cannot reconstruct trustworthy routes.
	if readErr == nil {
		readErr = r.syncIdleRoutes(prior)
	}
	// Preserve the original edge error when restoring the previous table succeeded.
	if readErr == nil {
		return err
	}
	return errors.Join(err, readErr)
}

// ServeHTTP holds the original request open across readiness and streaming.
func (g *idleGateway) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	host := req.Host
	// The port is not part of the configured route identity.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	entry, wake, status := g.admit(host)
	// Unknown hosts and excess cold requests are rejected before touching the runtime.
	if status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	// Count requests through response completion, including upgraded connections.
	defer func() {
		g.mu.Lock()
		entry.active--
		entry.last = time.Now()
		g.mu.Unlock()
	}()
	// No byte of the original body has been sent when waking fails.
	if err := wake.wait(req.Context()); err != nil {
		http.Error(w, "Service could not wake", http.StatusServiceUnavailable)
		return
	}
	g.mu.Lock()
	upstream := entry.upstream
	valid := g.routes[host] == entry
	g.mu.Unlock()
	// A replaced revision must not receive traffic from its predecessor's queue.
	if !valid {
		http.Error(w, "Service route changed", http.StatusServiceUnavailable)
		return
	}
	// Choose the backend from the retained route, preserving the external host and request URI.
	proxy := httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.Out.URL.Scheme, p.Out.URL.Host = "http", upstream
		p.Out.Host = p.In.Host
		// Preserve the public edge's forwarding metadata.
		for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host"} {
			// Copy only forwarding fields actually supplied by the edge.
			if v := p.In.Header.Values(name); len(v) > 0 {
				p.Out.Header[name] = v
			}
		}
	}, Transport: idleTransport}
	proxy.ServeHTTP(w, req)
}

// Admit and count the request before inspecting idle state, so an in-flight
// stream cannot race the idle deadline. Cold callers share one wake operation.
func (g *idleGateway) admit(host string) (*idleRoute, *idleWake, int) {
	g.mu.Lock()
	entry := g.routes[host]
	// A missing route or a closing runtime cannot initiate a start.
	if entry == nil || g.ctx.Err() != nil {
		g.mu.Unlock()
		return nil, nil, http.StatusNotFound
	}
	// Bound the number of held requests while the backend is unavailable.
	if entry.sleeping && entry.active >= 128 {
		g.mu.Unlock()
		return nil, nil, http.StatusServiceUnavailable
	}
	entry.active++
	wake := entry.wake
	// The first cold caller starts the shared worker while holding the admission lock.
	if entry.sleeping && wake == nil {
		wake = &idleWake{done: make(chan struct{})}
		entry.wake = wake
		g.workers.Add(1)
		go g.wake(entry, wake)
	}
	g.mu.Unlock()
	return entry, wake, 0
}

// Request cancellation releases that caller without cancelling a shared wake.
func (w *idleWake) wait(ctx context.Context) error {
	// Warm requests can proceed immediately.
	if w == nil {
		return nil
	}
	// The client's lifetime and the runtime transition have separate cancellation scopes.
	select {
	// An abandoned request leaves other callers' cold starts intact.
	case <-ctx.Done():
		return ctx.Err()
	// A completed transition publishes its error to all waiting callers.
	case <-w.done:
		return w.err
	}
}

// No environment proxy may receive requests bound for a private workload.
var idleTransport = &http.Transport{MaxIdleConnsPerHost: 16, IdleConnTimeout: 30 * time.Second}

// Wake the committed revision once, then release its waiters.
func (g *idleGateway) wake(entry *idleRoute, wake *idleWake) {
	defer g.workers.Done()
	g.runtime.mu.Lock()
	err := g.transition(entry, true)
	g.runtime.mu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	// Keep runtime diagnostics local and return a generic failure to public callers.
	if err != nil {
		// A partial start may have created capacity. Recheck it on the next idle deadline.
		entry.stopped = false
		log.Printf("idle wake %s: %v", entry.name, err)
	}
	entry.wake = nil
	wake.err = err
	close(wake.done)
}

// A single controller-owned timer monitors all opted-in applications.
func (g *idleGateway) monitor() {
	defer g.workers.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	// Tick until the controller begins shutdown.
	for {
		// Cancellation stops scheduling new mutations.
		select {
		// Shutdown always cancels the runtime operation context.
		case <-g.ctx.Done():
			return
		// Scan the routes regularly even when there are no incoming requests.
		case <-ticker.C:
			g.tick()
		}
	}
}

// Try a scan without delaying a deployment or lifecycle operation already in progress.
func (g *idleGateway) tick() {
	g.mu.Lock()
	var due []*idleRoute
	// Collect due routes under the gateway lock, then release it before doing I/O.
	for _, entry := range g.routes {
		// Leave active streams and confirmed sleeping workloads alone.
		if entry.active == 0 && !entry.stopped && time.Since(entry.last) >= entry.after {
			due = append(due, entry)
		}
	}
	g.mu.Unlock()
	// Recheck each candidate under the runtime mutation lock.
	for _, entry := range due {
		// Let deploys and cold starts run between independent stop operations.
		if !g.runtime.mu.TryLock() {
			return
		}
		err := g.transition(entry, false)
		g.runtime.mu.Unlock()
		// A failed stop stays eligible for a later retry.
		if err != nil {
			log.Printf("idle sleep %s: %v", entry.name, err)
		}
	}
}

// The runtime lock fences deploy, rollback and manual lifecycle actions.
// Mark sleep intent before stopping so a crash mid-transition can be retried.
func (g *idleGateway) transition(entry *idleRoute, awake bool) error {
	s, err := g.runtime.read()
	// Never change capacity from an unreadable or missing journal.
	if err != nil {
		return err
	}
	app, ok := s.Apps[entry.name]
	// A replaced, manually stopped or deleted revision has lost automatic authority.
	if !ok || !g.current(entry, app) {
		return errors.New("deployment changed")
	}
	// A request may have arrived since the timer selected the route.
	if !awake && !g.claimSleep(entry) {
		return nil
	}
	ctx, cancel := context.WithTimeout(g.ctx, 90*time.Second)
	defer cancel()
	target, err := g.applyCapacity(ctx, s, app, awake)
	// Keep the route behind the wake path if a mutation fails partway through.
	if err != nil {
		return err
	}
	g.mu.Lock()
	entry.sleeping, entry.stopped, entry.upstream = !awake, !awake, target
	g.mu.Unlock()
	return nil
}

// Persist a pending stop before mutating the runtime. Publish a warm port only after readiness.
func (g *idleGateway) applyCapacity(ctx context.Context, s state, app appRecord, awake bool) (string, error) {
	// Journal sleep intent before issuing the first external stop.
	if !awake && !app.IdleSleeping {
		// Without durable intent a restart could misclassify a stopped workload.
		if err := g.saveCapacity(s, app, true, app.Upstream); err != nil {
			return "", err
		}
	}
	target, err := g.scale(ctx, app, awake)
	// A partial engine failure must not publish readiness.
	if err != nil {
		return "", err
	}
	// Wake may allocate a new backend address, particularly after Docker restarts.
	if awake {
		// Do not send waiting clients to a port until it is durably recorded.
		if err := g.saveCapacity(s, app, false, target); err != nil {
			return "", err
		}
	}
	return target, nil
}

// Fence stale timers and requests to the exact admission entry. A manual
// stop/start can replace a route without changing its deployment operation ID.
func (g *idleGateway) current(e *idleRoute, a appRecord) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.routes[e.host] == e && a.OperationID == e.operation && a.Status == "running" && a.Spec.SleepAfter != ""
}

// claimSleep rechecks activity immediately before closing admission to the warm backend.
func (g *idleGateway) claimSleep(entry *idleRoute) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Activity since the scan restarts the full idle window.
	if entry.active > 0 || time.Since(entry.last) < entry.after {
		return false
	}
	entry.sleeping = true
	return true
}

// Save the discovered port only after health succeeds. Docker may assign a new one on restart.
func (g *idleGateway) saveCapacity(s state, app appRecord, sleeping bool, target string) error {
	app.IdleSleeping, app.Upstream = sleeping, target
	s.Apps[app.Name] = app
	return g.runtime.save(s)
}
