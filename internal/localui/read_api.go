package localui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"hostlier/internal/model"
)

// readEntry coalesces identical active-page reads across local browser tabs.
type readEntry struct {
	ready   chan struct{}
	data    json.RawMessage
	err     error
	expires time.Time
}

// pageReadRoute defines the narrow dashboard read contract; it cannot proxy arbitrary controller URLs.
func pageReadRoute(r *http.Request) (string, error) {
	// Every page endpoint is an authenticated GET with no mutation payload.
	if r.Method != "GET" {
		return "", model.Err("NOT_FOUND", "Dashboard read endpoint not found")
	}
	// The fixed inventory map never accepts an upstream path from browser input.
	if path := inventoryReadRoutes[r.URL.Path]; path != "" {
		return path, nil
	}
	if r.URL.Path == "/api/activity" {
		return historyReadPath(r)
	}
	// Only the observability route may read agent history.
	if r.URL.Path == "/api/observability" {
		return telemetryReadPath(r)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// A named detail route contains a single canonical identifier and optional documented suffix.
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "api" || !validReadIdentity(parts[2]) {
		return "", model.Err("NOT_FOUND", "Dashboard read endpoint not found")
	}
	return namedReadPath(r, parts)
}

// namedReadPath maps only deployment/operation details and their bounded diagnostic endpoints.
func namedReadPath(r *http.Request, parts []string) (string, error) {
	name := url.PathEscape(parts[2])
	// Deployment detail and its redacted log tail remain distinct reads.
	if parts[1] == "deployments" {
		// The bare deployment route returns current metadata and allowlisted runtime diagnostics.
		if len(parts) == 3 {
			return "/v1/apps/" + name, nil
		}
		// Component logs retain exact deployment authority.
		if parts[3] == "workflows" {
			return "/v1/workflows/" + name, nil
		}
		// Component selection stays on the named application log endpoint.
		if parts[3] == "logs" {
			return deploymentLogPath(r, name)
		}
	}
	// Operation details and sanitized execution events never start or retry remote work.
	if parts[1] == "operations" {
		// A bare operation route reads its exact durable receipt.
		if len(parts) == 3 {
			return "/v1/operations/" + name, nil
		}
		// Execution events are filtered to one retained operation.
		if parts[3] == "events" {
			return "/v1/logs?" + url.Values{"operationId": {parts[2]}}.Encode(), nil
		}
	}
	return domainDetailRoute(parts)
}

// validReadIdentity prevents encoded separators, traversal and ambiguous path interpretation.
func validReadIdentity(value string) bool {
	return value != "" && len(value) <= 200 && value != "." && value != ".." && !strings.ContainsAny(value, "/%?#\\\x00\r\n")
}

// deploymentLogPath validates component selectors before constructing the one allowed query.
func deploymentLogPath(r *http.Request, name string) (string, error) {
	component := r.URL.Query().Get("component")
	// Default logs select the primary workload, never another application's component.
	if component == "" {
		return "/v1/apps/" + name + "/logs", nil
	}
	// Reject malformed component identities before constructing the log query.
	if !validReadIdentity(component) {
		return "", model.Err("INVALID_REQUEST", "Invalid component selector")
	}
	return "/v1/apps/" + name + "/logs?" + url.Values{"component": {component}}.Encode(), nil
}

// telemetryReadPath validates the existing controller history selectors and applies bounded defaults.
func telemetryReadPath(r *http.Request) (string, error) {
	supplied := r.URL.Query()
	if _, err := model.ParseHistoryFilter(supplied); err != nil {
		return "", err
	}
	query := url.Values{"signal": {supplied.Get("signal")}, "limit": {"100"}}
	// Metrics and logs have different data contracts and must be selected explicitly.
	if query.Get("signal") != "metrics" && query.Get("signal") != "logs" {
		return "", model.Err("INVALID_REQUEST", "Choose metrics or logs")
	}
	// Pass only documented bounded selectors; user input never supplies an upstream path.
	for _, key := range []string{"app", "component", "revision"} {
		// Omitted identity filters select all records within the authenticated controller.
		if value := supplied.Get(key); value != "" {
			// Each nonempty selector must remain a single bounded identity.
			if !validReadIdentity(value) {
				return "", model.Err("INVALID_REQUEST", "Invalid observation filter")
			}
			query.Set(key, value)
		}
	}
	// Bound the requested number of observation records.
	if err := historyNumber(supplied, query, "limit", 1, 200); err != nil {
		return "", err
	}
	// Older pages retain an exclusive validated sequence cursor.
	if err := historyNumber(supplied, query, "before", 0, 1<<63-1); err != nil {
		return "", err
	}
	for _, key := range []string{"from", "to", "q", "level", "beforeTime"} {
		if value := supplied.Get(key); value != "" {
			query.Set(key, value)
		}
	}
	return "/v1/telemetry/history?" + query.Encode(), nil
}

// historyNumber copies only validated positive decimal limits/cursors into the remote request.
func historyNumber(input, output url.Values, key string, minimum, maximum int64) error {
	value := input.Get(key)
	// Missing parameters retain the explicit bounded defaults.
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	// Reject overflow, negative cursors and oversized limits before network work.
	if err != nil || parsed < minimum || parsed > maximum {
		return model.Err("INVALID_REQUEST", "Invalid observation pagination")
	}
	output.Set(key, strconv.FormatInt(parsed, 10))
	return nil
}

// readPage resolves one bound controller and shares a short cache across browser views.
func (s *Server) readPage(r *http.Request) (any, error) {
	path, err := pageReadRoute(r)
	// Unknown routes cannot reach the authenticated upstream client.
	if err != nil {
		return nil, err
	}
	remote, generation := s.readAuthority()
	// First-run dashboards have no controller read authority.
	if remote == nil {
		return nil, model.Err("NO_SERVER", "Link a controller first")
	}
	key := strconv.FormatUint(generation, 10) + "|" + path
	s.readMu.Lock()
	entry := s.pageReads[key]
	// A pending read or a fresh cached result avoids one controller query per browser tab.
	if entry == nil || time.Now().After(entry.expires) {
		// Discard old cache keys before allocating another bounded selector entry.
		if len(s.pageReads) >= 64 {
			s.pageReads = make(map[string]*readEntry)
		}
		// Admit before starting a goroutine so queued viewers cannot create unbounded read workers.
		select {
		case s.readSlots <- struct{}{}: // This worker owns its slot until completion or cancellation.
		default:
			s.readMu.Unlock()
			return nil, model.Err("RATE_LIMITED", "Dashboard reads are busy; retry shortly") // Status and ordinary controller operations remain independent.
		}
		entry = &readEntry{ready: make(chan struct{}), expires: time.Now().Add(5 * time.Second)}
		s.pageReads[key] = entry
		go s.fetchPage(remote, path, entry) // A bounded request deadline owns this independent read worker.
	}
	s.readMu.Unlock()
	// One tab closing must not cancel a shared query still needed by another tab.
	select {
	// A closing tab abandons only its own wait.
	case <-r.Context().Done():
		return nil, r.Context().Err() // Only this viewer abandons the shared result.
	case <-entry.ready:
		return entry.data, entry.err // Payloads remain immutable after the worker closes ready.
	}
}

// readAuthority freezes selected identity so a connection change never reuses another controller's cache.
func (s *Server) readAuthority() (Remote, uint64) {
	manager, ok := s.remote.(*ManagedRemote)
	// Fixed injected clients have one immutable cache namespace.
	if !ok {
		return s.remote, 0
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.remote, manager.generation
}

// fetchPage bounds upstream concurrency independently of capture and connection/approval locks.
func (s *Server) fetchPage(remote Remote, path string, entry *readEntry) {
	defer close(entry.ready)
	ctx, cancel := context.WithTimeout(s.readContext, 4*time.Second)
	defer cancel()
	defer func() { <-s.readSlots }() // Every admitted worker releases capacity on success, failure and shutdown.

	entry.err = remote.Request(ctx, "GET", path, nil, &entry.data)
}

// dashboardPage recognizes only real client routes; API and arbitrary filesystem paths never receive the app shell.
func dashboardPage(r *http.Request) bool {
	// Page navigation cannot admit mutation methods.
	if r.Method != "GET" {
		return false
	}
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	// Collection pages and detail pages have separate fixed route allowlists.
	if len(parts) == 1 {
		return dashboardCollections[path]
	}
	return len(parts) == 2 && dashboardDetails[parts[0]] && validReadIdentity(parts[1])
}
