package controller

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"hostlier/internal/model"
)

// eventFrame holds an immutable JSON snapshot ready for emission outside the controller lock.
type eventFrame struct {
	kind string
	id   string
	data []byte
}

// eventSnapshot separates locked state inspection from potentially slow network writes.
type eventSnapshot struct {
	frames   []eventFrame
	after    uint64
	status   string
	terminal bool
	changed  <-chan struct{}
}

// migrateEventSequences assigns durable cursors to legacy events without changing their order.
func (c *Controller) migrateEventSequences() {
	var previous uint64
	// Preserve existing increasing sequences and initialize older unsequenced entries in place.
	for i := range c.state.Events {
		entry := &c.state.Events[i]
		// A legacy zero or nonincreasing cursor needs the next sequence in retained order.
		if entry.Sequence <= previous {
			entry.Sequence = previous + 1
		}
		previous = entry.Sequence
	}
	// Retain the high watermark even when its events have already expired.
	if c.state.EventSequence < previous {
		c.state.EventSequence = previous
	}
}

// operationEventsRoute recognizes only the exact read-only operation event endpoint.
func operationEventsRoute(r *http.Request) bool {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return r.Method == "GET" && len(p) == 4 && p[0] == "v1" && p[1] == "operations" && p[3] == "events"
}

// parseEventCursor accepts only canonical sequence cursors bound to the requested operation.
func parseEventCursor(raw, id string, latest uint64) (uint64, error) {
	// A new subscriber requests the retained history from its beginning.
	if raw == "" {
		return 0, nil
	}
	prefix, number, ok := strings.Cut(raw, ":")
	sequence, err := strconv.ParseUint(number, 10, 64)
	// Reject malformed, cross-operation and future cursors before sending stream headers.
	if !ok || prefix != id || err != nil || strconv.FormatUint(sequence, 10) != number || sequence > latest {
		return 0, model.Err("INVALID_EVENT_CURSOR", "Invalid operation event cursor")
	}
	return sequence, nil
}

// operationTerminal identifies outcomes whose durable execution will not produce further progress.
func operationTerminal(status string) bool {
	return status != "queued" && status != "accepted" && status != "pending" && status != "running"
}

// snapshotEvents reauthenticates and snapshots durable public events while holding the state lock.
func (c *Controller) snapshotEvents(r *http.Request, local bool, id string, after uint64, status string) (eventSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.operationChange == nil {
		c.operationChange = make(chan struct{})
	}
	result := eventSnapshot{after: after, changed: c.operationChange}
	// Revocation or controller shutdown closes a live stream without touching its background job.
	if _, err := c.authenticate(r, local, false); err != nil {
		return result, err
	}
	// Shutdown must not leave streams waiting for their rotation deadline.
	if c.closed {
		return result, model.Err("UNAVAILABLE", "Controller shutting down")
	}
	op := c.state.Operations[id]
	// Missing operations remain unavailable even after an earlier successful snapshot.
	if op == nil {
		return result, model.Err("NOT_FOUND", "Operation not found")
	}
	result.frames, result.after = c.retainedEventFrames(id, after)
	// Persist newly observable event cursors before any client can use them for replay.
	if len(result.frames) > 0 && c.state.EventSequence > c.eventPersistedSequence {
		// A failed durable snapshot must never expose reusable progress identifiers.
		if err := c.store.Save(c.state); err != nil {
			return eventSnapshot{}, model.Err("STATE_WRITE_FAILED", "Unable to persist execution events")
		}
		c.eventPersistedSequence = c.state.EventSequence
	}
	// Publish the initial operation and each changed status, including the terminal outcome.
	if op.Status != status {
		data, _ := json.Marshal(op)
		result.frames = append(result.frames, eventFrame{kind: "operation", data: data})
	}
	result.status = op.Status
	result.terminal = operationTerminal(op.Status)
	return result, nil
}

// retainedEventFrames reports retention gaps before replaying only the requested operation's events.
// The caller holds the controller lock.
func (c *Controller) retainedEventFrames(id string, after uint64) ([]eventFrame, uint64) {
	frames := []eventFrame{}
	// A cursor below the global retention floor cannot imply complete replay.
	if after > 0 && len(c.state.Events) > 0 && after < c.state.Events[0].Sequence-1 {
		frames = append(frames, eventFrame{kind: "reset", data: []byte(`{"reason":"history_expired"}`)})
		after = c.state.Events[0].Sequence - 1
	}
	// Preserve durable event order while excluding unrelated operations and acknowledged entries.
	for _, entry := range c.state.Events {
		// Only newer entries belonging to this operation are public on this stream.
		if entry.OperationID == id && entry.Sequence > after {
			data, _ := json.Marshal(entry)
			frames = append(frames, eventFrame{kind: "log", id: id + ":" + strconv.FormatUint(entry.Sequence, 10), data: data})
			after = entry.Sequence
		}
	}
	return frames, after
}

// signalOperationChange wakes subscribed streams without waiting for readers.
// Call while holding the state lock; the snapshot captures its next wakeup
// under the same lock, so a change racing delivery cannot be missed.
func (c *Controller) signalOperationChange() {
	if c.operationChange != nil {
		close(c.operationChange)
		c.operationChange = nil
	}
}
