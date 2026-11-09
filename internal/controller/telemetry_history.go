package controller

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"hostlier/internal/model"
	"hostlier/internal/telemetry"
)

// telemetryService keeps bounded diagnostic work separate from controller command locks.
type telemetryService struct {
	mu           sync.Mutex
	store        *telemetry.Store
	active       int
	closed       bool
	accepted     int64
	rejected     int64
	lastReceived time.Time
}

// reserve caps expensive parsing and runtime identity checks before reading request bodies.
func (s *telemetryService) reserve() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Shutdown stops admission before any new diagnostic work begins.
	if s.closed || s.active >= 2 {
		return false
	}
	s.active++
	return true
}

// release returns an ingestion slot after every success, rejection, and cancellation.
func (s *telemetryService) release() { s.mu.Lock(); s.active--; s.mu.Unlock() }

// telemetryStore lazily opens bounded history, without adding a dependency to deployment startup.
func (c *Controller) telemetryStore() (*telemetry.Store, error) {
	s := &c.telemetry
	s.mu.Lock()
	defer s.mu.Unlock()
	// Shutdown stops admission before any new diagnostic work begins.
	if s.closed {
		return nil, model.Err("TELEMETRY_UNAVAILABLE", "Telemetry is closing")
	}
	// Initialize history only when telemetry is actually used.
	if s.store == nil {
		// The controller already owns this directory; canonicalize OS aliases such as macOS /var.
		root, err := filepath.EvalSymlinks(c.opts.StateDir)
		// Never create history when the owned state root cannot be resolved.
		if err != nil {
			return nil, model.Err("TELEMETRY_UNAVAILABLE", "Telemetry state directory is unavailable")
		}
		store, err := telemetry.Open(filepath.Join(root, "observations", "history"))
		// Propagate failure without publishing an incomplete or misleading result.
		if err != nil {
			return nil, model.Err("TELEMETRY_UNAVAILABLE", "Telemetry history is unavailable")
		}
		s.store = store
	}
	return s.store, nil
}

// closeTelemetry closes only diagnostic history, after listener shutdown completes.
func (c *Controller) closeTelemetry() {
	s := &c.telemetry
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	// An unused controller has no telemetry database to close.
	if s.store != nil {
		s.store.Close()
	}
}

// ingestTelemetry returns standard OTLP partial-success counts and never alters deployment observations.
func (c *Controller) ingestTelemetry(r *http.Request, credential TelemetryCollector, signal string, body []byte) (any, error) {
	store, err := c.telemetryStore()
	// Propagate failure without publishing an incomplete or misleading result.
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	resolve := c.telemetryResolver(ctx)
	result, err := store.Ingest(ctx, signal, body, telemetry.Scope{Collector: credential.ID, Runtime: credential.Runtime}, resolve)
	// Propagate failure without publishing an incomplete or misleading result.
	if err != nil {
		// Permanent parse errors must not trigger exporter retries.
		if errors.Is(err, telemetry.ErrInvalid) {
			return nil, model.Err("INVALID_REQUEST", "Invalid telemetry payload")
		}
		return nil, model.Err("TELEMETRY_UNAVAILABLE", "Telemetry history is unavailable")
	}
	c.telemetry.mu.Lock()
	c.telemetry.accepted += int64(result.Accepted)
	c.telemetry.rejected += int64(result.Rejected)
	c.telemetry.lastReceived = time.Now().UTC()
	c.telemetry.mu.Unlock()
	// A fully accepted batch uses the standard empty OTLP success response.
	if result.Rejected == 0 {
		return map[string]any{}, nil
	}
	field := "rejectedLogRecords"
	// OTLP names partial-success counts differently for metrics and logs.
	if signal == "metrics" {
		field = "rejectedDataPoints"
	}
	return map[string]any{"partialSuccess": map[string]any{field: strconv.Itoa(result.Rejected), "errorMessage": "Unsupported, invalid, or unowned telemetry records were rejected"}}, nil
}

// telemetryResolver caches verified resources only within one bounded request.
func (c *Controller) telemetryResolver(ctx context.Context) telemetry.Resolver {
	cache := map[[7]string]telemetry.Identity{}
	// Resource verification remains bound to the current request deadline.
	return func(resource telemetry.Resource) (telemetry.Identity, error) {
		// Honor cancellation even for empty resource groups that never reach the database.
		if err := ctx.Err(); err != nil {
			return telemetry.Identity{}, err
		}
		key := [7]string{resource["container.id"], resource["k8s.namespace.name"], resource["k8s.pod.name"], resource["k8s.pod.uid"], resource["k8s.container.name"], resource["k8s.node.name"], resource["host.name"]}
		// Reuse only an exact resource identity already verified in this request.
		if identity, ok := cache[key]; ok {
			return identity, nil
		}
		identity, err := c.resolveTelemetry(ctx, resource)
		// Propagate failure without publishing an incomplete or misleading result.
		if err == nil {
			cache[key] = identity
		}
		return identity, err
	}
}

// resolveTelemetry establishes workload membership from runtime records, never arbitrary Hostlier labels.
func (c *Controller) resolveTelemetry(ctx context.Context, resource telemetry.Resource) (telemetry.Identity, error) {
	// Infrastructure samples are separate from application membership.
	if !hasWorkloadCoordinates(resource) {
		return telemetryNode(resource)
	}

	runtime, ok := c.opts.Runtime.(model.TelemetryRuntime)
	// A runtime without identity verification cannot attribute workload telemetry.
	if !ok {
		return telemetry.Identity{}, errors.New("runtime cannot verify telemetry")
	}
	identity, err := runtime.ResolveTelemetry(ctx, map[string]string(resource))
	// Propagate failure without publishing an incomplete or misleading result.
	if err != nil {
		return telemetry.Identity{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	app := c.state.Applications[identity.Application]
	// Reject samples if rollout changed the authoritative runtime revision.
	if app == nil || app.RuntimeID != identity.Revision {
		return telemetry.Identity{}, errors.New("deployment changed during collection")
	}
	return telemetry.Identity{Application: identity.Application, Revision: app.Revision, Component: identity.Component, Instance: identity.Instance, Pod: resource["k8s.pod.uid"], Node: resource["k8s.node.name"]}, nil
}

// queryTelemetry exposes bounded sensitive history to ordinary authenticated administrators only.
func (c *Controller) queryTelemetry(r *http.Request) (any, error) {
	query, err := telemetryQuery(r)
	// Reject malformed selectors before opening storage.
	if err != nil {
		return nil, err
	}

	store, err := c.telemetryStore()
	// Propagate failure without publishing an incomplete or misleading result.
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	result, err := store.Query(ctx, query)
	// Invalid query selectors are permanent client errors, not retryable storage failures.
	if errors.Is(err, telemetry.ErrInvalid) {
		return nil, model.Err("INVALID_REQUEST", "Invalid telemetry query")
	}
	// Propagate failure without publishing an incomplete or misleading result.
	if err != nil {
		return nil, model.Err("TELEMETRY_UNAVAILABLE", "Telemetry history is unavailable")
	}
	return result, nil
}

// telemetryHealth exposes only delivery counters; freshness is independent from application readiness.
func (c *Controller) telemetryHealth() *model.TelemetryHealth {
	c.mu.Lock()
	count := 0
	// Visit the bounded inventory without serializing private credential material.
	for _, credential := range c.state.TelemetryCollectors {
		// Only unexpired live credentials count as configured collectors.
		if !credential.Revoked && c.opts.Now().Before(credential.ExpiresAt) {
			count++
		}
	}
	c.mu.Unlock()
	c.telemetry.mu.Lock()
	defer c.telemetry.mu.Unlock()
	// Unused telemetry does not add a misleading status indicator.
	if count == 0 && c.telemetry.lastReceived.IsZero() && !c.opts.ManagedCollector {
		return nil
	}
	return &model.TelemetryHealth{Managed: c.managedCollectorHealth(), Collectors: count, Accepted: c.telemetry.accepted, Rejected: c.telemetry.rejected, LastReceivedAt: c.telemetry.lastReceived, Fresh: !c.telemetry.lastReceived.IsZero() && time.Since(c.telemetry.lastReceived) < 90*time.Second}
}

// hasWorkloadCoordinates prevents a partial workload identity from falling back to host attribution.
func hasWorkloadCoordinates(r telemetry.Resource) bool {
	return r["container.id"] != "" || r["k8s.pod.uid"] != "" || r["k8s.pod.name"] != "" || r["k8s.container.name"] != ""
}

// telemetryNode admits authenticated collector host samples without trusting application labels.
func telemetryNode(r telemetry.Resource) (telemetry.Identity, error) {
	node := r["k8s.node.name"]
	// Docker host collectors use the standard host.name attribute.
	if node == "" {
		node = r["host.name"]
	}
	// A sample with no concrete infrastructure identity is not useful historical evidence.
	if node == "" {
		return telemetry.Identity{}, errors.New("missing runtime identity")
	}
	return telemetry.Identity{Node: node}, nil
}

// telemetryQuery validates all history limits before constructing a database query.
func telemetryQuery(r *http.Request) (telemetry.Query, error) {
	q := r.URL.Query()
	filter, err := model.ParseHistoryFilter(q)
	if err != nil {
		return telemetry.Query{}, err
	}
	limit, err := telemetryQueryNumber(q.Get("limit"), 100, 1, telemetry.MaxQueryRecords)
	// Invalid row limits must not reach the database.
	if err != nil {
		return telemetry.Query{}, err
	}
	before, err := telemetryQueryNumber(q.Get("before"), 0, 0, 1<<63-1)
	// Cursor validation also rejects arithmetic overflow.
	if err != nil {
		return telemetry.Query{}, err
	}
	// Separate signals prevent ambiguous history and accidental mixed log disclosure.
	if q.Get("signal") != "logs" && q.Get("signal") != "metrics" {
		return telemetry.Query{}, model.Err("INVALID_REQUEST", "History signal must be logs or metrics")
	}
	return telemetry.Query{Signal: q.Get("signal"), Application: q.Get("app"), Component: q.Get("component"), Revision: q.Get("revision"), Limit: int(limit), Before: before, From: filter.From, To: filter.To, BeforeTime: filter.BeforeTime, Search: filter.Search, Level: filter.Level}, nil
}

// telemetryQueryNumber bounds a caller-supplied decimal or supplies an explicit default.
func telemetryQueryNumber(value string, fallback, minimum, maximum int64) (int64, error) {
	// Missing values inherit bounded server defaults.
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	// Reject invalid syntax, overflow, and out-of-range values together.
	if err != nil || n < minimum || n > maximum {
		return 0, model.Err("INVALID_REQUEST", "Invalid telemetry query limit or cursor")
	}
	return n, nil
}
