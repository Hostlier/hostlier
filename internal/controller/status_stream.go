package controller

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"hostlier/internal/model"
)

// serveStatusObservation intercepts exact read-only routes before ordinary state persistence.
func (c *Controller) serveStatusObservation(w http.ResponseWriter, r *http.Request, local bool) bool {
	// Other methods and paths retain the ordinary API's authorization and routing rules.
	if r.Method != "GET" || (r.URL.Path != "/v1/status/snapshot" && r.URL.Path != "/v1/status/events") {
		return false
	}
	// Cached observations require the same currently live authority as other controller reads.
	if err := c.statusAuthority(r, local); err != nil {
		eventStreamError(w, err)
		return true
	}
	c.observations.touch()
	// A snapshot read schedules shared capture without waiting for runtime probes.
	if r.URL.Path == "/v1/status/snapshot" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(c.latestStatus()))
		return true
	}
	c.serveStatusEvents(w, r, local)
	return true
}

// statusAuthority briefly locks only identity records; no runtime or network work is included.
func (c *Controller) statusAuthority(r *http.Request, local bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// New subscriptions cannot prolong shutdown or race the worker wait group.
	if c.closed {
		return model.Err("UNAVAILABLE", "Controller is shutting down")
	}
	_, err := c.authenticate(r, local, false)
	return err
}

// latestStatus returns a complete replacement; cold snapshots honestly mark observations pending.
func (c *Controller) latestStatus() string {
	value := c.observations.bus.Latest()
	var identity struct {
		ID string `json:"id"`
	} // Old-process telemetry is never a current readiness observation.
	json.Unmarshal([]byte(value), &identity)
	// Retained broker data must wait for a fresh capture under the current controller epoch.
	if !strings.HasPrefix(identity.ID, c.observations.epoch+":") {
		out, err := c.statusMetadata()
		if err != nil {
			return value
		}
		out.ID = c.observations.epoch + ":0"
		data, _ := json.Marshal(out)
		return string(data)
	}
	return value
}

// reserveStatusStream bounds long-lived observers independently of authoritative execution.
func (s *statusCapture) reserveStatusStream() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Reject excess subscribers before committing streaming response headers.
	if s.streams >= 32 {
		return false
	}
	s.streams++
	return true
}

// releaseStatusStream returns capacity for every cancellation, error and rotation path.
func (s *statusCapture) releaseStatusStream() { s.mu.Lock(); s.streams--; s.mu.Unlock() }

// serveStatusEvents fans out cached replacements without polling state or inspecting runtimes per viewer.
func (c *Controller) serveStatusEvents(w http.ResponseWriter, r *http.Request, local bool) {
	s := c.observations
	// Admission is independent of slow network writes.
	if !s.reserveStatusStream() {
		eventStreamError(w, model.Err("RATE_LIMITED", "Status stream capacity exceeded"))
		return
	}
	defer s.releaseStatusStream()
	updates, unsubscribe := s.bus.Subscribe()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	// Every connection starts with a full replacement, so any older cursor is safely reset.
	if err := c.writeStatusSnapshot(w, r, local); err != nil {
		return
	}
	c.statusEventLoop(w, r, local, updates)
}

// writeStatusSnapshot rechecks authority before releasing each cached observation.
func (c *Controller) writeStatusSnapshot(w http.ResponseWriter, r *http.Request, local bool) error {
	// Revoked clients cannot continue receiving newly captured state.
	if err := c.statusAuthority(r, local); err != nil {
		return err
	}
	return writeEventFrames(w, []eventFrame{{kind: "snapshot", data: []byte(c.latestStatus())}})
}

// statusEventLoop watches coalesced bus notifications, reauthenticates and bounds stream lifetime.
func (c *Controller) statusEventLoop(w http.ResponseWriter, r *http.Request, local bool, updates <-chan struct{}) {
	heartbeat := time.NewTicker(eventStreamHeartbeat)
	defer heartbeat.Stop()
	lifetime := time.NewTimer(eventStreamLifetime)
	defer lifetime.Stop()
	// No network write occurs while either state or capture locks are held.
	for {
		// Each wait observes viewer cancellation and controller shutdown independently.
		select {
		// Closing a viewer only releases its subscription.
		case <-r.Context().Done():
			return
		// Controller shutdown ends otherwise idle streams promptly.
		case <-c.ctx.Done():
			return
		// Clients reconnect to the latest complete replacement snapshot.
		case <-lifetime.C:
			return
		// All subscribers reuse the same capture rather than triggering new probes.
		case <-updates:
			// A failed write or revoked credential releases stream capacity.
			if c.writeStatusSnapshot(w, r, local) != nil {
				return
			}
		// Idle streams keep capture active and check credential revocation at least every five seconds.
		case <-heartbeat.C:
			// Cached state is inaccessible after remote authority is revoked.
			if c.statusAuthority(r, local) != nil {
				return
			}
			c.observations.touch()
			// A stalled client must release capacity within the bounded write deadline.
			if writeEventHeartbeat(w) != nil {
				return
			}
		}
	}
}
