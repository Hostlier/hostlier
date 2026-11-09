package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"hostlier/internal/model"
)

// eventStreamLimits bounds subscriptions, write stalls and connection lifetimes independently of jobs.
const (
	eventStreamCapacity     = 32
	eventStreamLifetime     = 20 * time.Second
	eventStreamHeartbeat    = 5 * time.Second
	eventStreamPoll         = 200 * time.Millisecond
	eventStreamWriteTimeout = 2 * time.Second
)

// acquireEventStream validates authentication and replay scope before reserving bounded stream capacity.
func (c *Controller) acquireEventStream(r *http.Request, local bool, id string) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Authentication precedes all operation existence and replay information.
	if _, err := c.authenticate(r, local, false); err != nil {
		return 0, err
	}
	// Hide unavailable operation identities behind the ordinary safe not-found error.
	if c.state.Operations[id] == nil {
		return 0, model.Err("NOT_FOUND", "Operation not found")
	}
	after, err := parseEventCursor(r.Header.Get("Last-Event-ID"), id, c.state.EventSequence)
	// Reject invalid cursors before consuming subscription capacity.
	if err != nil {
		return 0, err
	}
	// Bound slow or abandoned subscriptions without blocking ordinary APIs.
	if c.streams >= eventStreamCapacity {
		return 0, model.Err("RATE_LIMITED", "Operation stream capacity exceeded")
	}
	c.streams++
	return after, nil
}

// releaseEventStream releases capacity independently of disconnect, cancellation or terminal completion.
func (c *Controller) releaseEventStream() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streams--
}

// eventStreamError returns only known safe pre-stream failures using ordinary API error semantics.
func eventStreamError(w http.ResponseWriter, err error) {
	public, ok := err.(*model.Error)
	// Never expose raw storage, transport or runtime diagnostics through the progress endpoint.
	if !ok {
		public = &model.Error{Code: "REQUEST_FAILED", Message: "Request failed"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(errorStatus(public.Code))
	_ = json.NewEncoder(w).Encode(map[string]any{"error": public})
}

// serveOperationEvents handles one resumable read-only subscription without owning the operation lifetime.
func (c *Controller) serveOperationEvents(w http.ResponseWriter, r *http.Request, local bool) {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	id := p[2]
	after, err := c.acquireEventStream(r, local, id)
	// Invalid authentication and replay requests must remain ordinary JSON responses.
	if err != nil {
		eventStreamError(w, err)
		return
	}
	defer c.releaseEventStream()
	initial, err := c.snapshotEvents(r, local, id, after, "")
	// Validate the initial durable snapshot before committing SSE response headers.
	if err != nil {
		eventStreamError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	// Flush each immutable frame outside the state lock so slow readers cannot stall APIs or jobs.
	if err = writeEventFrames(w, initial.frames); err != nil {
		return
	}
	// Terminal subscriptions replay their final logs and result, then close immediately.
	if initial.terminal {
		return
	}
	c.streamEventUpdates(w, r, local, id, initial)
}

// streamEventUpdates rotates connections and polls state without coupling disconnects to background work.
func (c *Controller) streamEventUpdates(w http.ResponseWriter, r *http.Request, local bool, id string, current eventSnapshot) {
	poll := time.NewTicker(eventStreamPoll)
	defer poll.Stop()
	heartbeat := time.NewTicker(eventStreamHeartbeat)
	defer heartbeat.Stop()
	lifetime := time.NewTimer(eventStreamLifetime)
	defer lifetime.Stop()
	// Bound each connection while preserving durable replay across intentional rotation.
	for {
		// All waits observe controller shutdown and request cancellation independently of job execution.
		select {
		// Client disconnect never cancels the controller's operation context.
		case <-r.Context().Done():
			return
		// Controller cancellation stops all subscriptions.
		case <-c.ctx.Done():
			return
		// Rotation occurs before ordinary client or proxy deadlines.
		case <-lifetime.C:
			return
		// Comments keep idle intermediaries aware that this stream remains live.
		case <-heartbeat.C:
			// Stop writing after disconnect or a bounded transport stall.
			if err := writeEventHeartbeat(w); err != nil {
				return
			}
		// Reauthenticate and snapshot changed status and retained logs regularly.
		case <-poll.C:
			next, keep := c.refreshEventStream(w, r, local, id, current)
			current = next
			// Failed authentication, writes and terminal outcomes all end this subscription.
			if !keep {
				return
			}
		}
	}
}

// writeEventFrames emits a complete bounded snapshot and flushes frames without a controller lock.
func writeEventFrames(w http.ResponseWriter, frames []eventFrame) error {
	// Write in snapshot order so cursor replay and terminal status remain consistent.
	for _, frame := range frames {
		control := http.NewResponseController(w)
		// Reset the short write deadline before every bounded frame.
		if err := control.SetWriteDeadline(time.Now().Add(eventStreamWriteTimeout)); err != nil {
			return err
		}
		// Only sanitized durable log frames carry a replay cursor.
		if frame.id != "" {
			// Propagate write failures before attempting any further frame bytes.
			if _, err := fmt.Fprintf(w, "id: %s\n", frame.id); err != nil {
				return err
			}
		}
		// JSON escaping prevents event data from injecting additional SSE fields.
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", frame.kind, frame.data); err != nil {
			return err
		}
		// Flush errors signal disconnected readers without changing operation execution.
		if err := control.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// writeEventHeartbeat emits an SSE comment with the same bounded transport behavior as data frames.
func writeEventHeartbeat(w http.ResponseWriter) error {
	control := http.NewResponseController(w)
	// A stalled reader must not hold subscription capacity indefinitely.
	if err := control.SetWriteDeadline(time.Now().Add(eventStreamWriteTimeout)); err != nil {
		return err
	}
	// Heartbeats never advance the durable replay cursor.
	if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
		return err
	}
	return control.Flush()
}

// refreshEventStream sends one independently authenticated snapshot without holding the lock during writes.
func (c *Controller) refreshEventStream(w http.ResponseWriter, r *http.Request, local bool, id string, current eventSnapshot) (eventSnapshot, bool) {
	next, err := c.snapshotEvents(r, local, id, current.after, current.status)
	// Revoked credentials and failed durable snapshots close without exposing diagnostics.
	if err != nil {
		return current, false
	}
	// Network writes happen only after the snapshot has released the controller lock.
	if err = writeEventFrames(w, next.frames); err != nil {
		return current, false
	}
	return next, !next.terminal
}
