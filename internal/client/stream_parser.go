package client

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"hostlier/internal/model"
)

// readOperationFrames parses chunk-independent SSE frames without consuming partial frames at disconnect.
func (w *operationWatch) readOperationFrames(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaxOperationFrameBytes+1)
	frame := operationFrame{}
	// SSE framing is line based, so arbitrary transport chunk boundaries never change event semantics.
	for scanner.Scan() {
		line := scanner.Text()
		// A blank line commits a complete frame; heartbeat-only frames carry no data.
		if line == "" {
			// Consumer failures must prevent both cursor advancement and reconnect replay loss.
			if err := w.consumeFrame(frame); err != nil {
				return err
			}
			frame = operationFrame{}
			// Terminal status is authoritative only after its complete frame was consumed.
			if w.terminal {
				return nil
			}
			continue
		}
		// Bound comments, identifiers and data together before appending any field.
		if err := frame.addLine(line); err != nil {
			return err
		}
	}
	// Scanner's size failure is a semantic limit violation, not a reconnectable connection drop.
	if scanner.Err() == bufio.ErrTooLong {
		return model.Err("EVENT_LIMIT", "Operation event frame exceeds response limit")
	}
	return io.EOF
}

// addLine accepts SSE field syntax while bounding each unfinished frame's memory use.
func (f *operationFrame) addLine(line string) error {
	f.size += len(line) + 1
	// A server cannot evade the frame limit using many short data lines or heartbeat comments.
	if f.size > MaxOperationFrameBytes {
		return model.Err("EVENT_LIMIT", "Operation event frame exceeds response limit")
	}
	// Comments are heartbeats and never change an operation's resume cursor.
	if strings.HasPrefix(line, ":") {
		return nil
	}
	field, value, _ := strings.Cut(line, ":")
	value = strings.TrimPrefix(value, " ")
	// Unknown SSE fields are ignored for protocol compatibility, but meaningful frames remain strictly validated.
	switch field {
	// Event kinds select the bounded wire schema interpreted by the watcher.
	case "event":
		f.event = value
	// Only log frames may carry a single unambiguous durable cursor.
	case "id":
		// Multiple identifiers make a frame ambiguous even if their values happen to match.
		if f.hasID {
			return model.Err("EVENT_PROTOCOL", "Operation event contains multiple cursors")
		}
		f.id = value
		f.hasID = true
	// Multiple data lines compose one JSON payload with newline separators.
	case "data":
		f.data = append(f.data, value)
	}
	return nil
}

// consumeFrame validates event identity before delivering data and committing its cursor.
func (w *operationWatch) consumeFrame(frame operationFrame) error {
	// Empty frames and heartbeat comments convey no application event.
	if len(frame.data) == 0 {
		return nil
	}
	event := OperationEvent{Event: frame.event, ID: frame.id, Data: json.RawMessage(strings.Join(frame.data, "\n"))}
	// Logs have durable cursors; status and reset notifications deliberately have none.
	if event.Event == "log" {
		return w.consumeLog(event)
	}
	// An identifier on a non-log frame could advance a browser-style cursor without consuming a log.
	if frame.hasID {
		return model.Err("EVENT_PROTOCOL", "Only operation log events may carry cursors")
	}
	// Validate the complete typed payload before invoking application code.
	if err := w.validateNotification(event); err != nil {
		return err
	}
	// Keep consumer failures visible without reconnecting or claiming successful delivery.
	if err := w.deliver(event); err != nil {
		return err
	}
	return nil
}

// consumeLog checks scope and wire sequence and suppresses already-consumed replay duplicates.
func (w *operationWatch) consumeLog(event OperationEvent) error {
	sequence, err := operationCursor(event.ID, w.operationID)
	// Cross-operation or malformed cursors must never be accepted into resume state.
	if err != nil {
		return err
	}
	var log operationLog
	// Require agreement between the cursor and the log payload's durable identity.
	if json.Unmarshal(event.Data, &log) != nil || log.OperationID != w.operationID || log.Sequence != sequence {
		return model.Err("EVENT_PROTOCOL", "Operation log identity does not match its cursor")
	}
	// Reconnection may replay acknowledged events, which must not be printed twice.
	if sequence <= w.sequence {
		return nil
	}
	// Advance resume state only after the consumer confirms delivery.
	if err = w.deliver(event); err != nil {
		return err
	}
	w.sequence = sequence
	w.cursor = event.ID
	return nil
}

// operationCursor decodes an operation-scoped positive global log sequence without accepting suffix ambiguity.
func operationCursor(cursor, operationID string) (uint64, error) {
	id, number, found := strings.Cut(cursor, ":")
	sequence, err := strconv.ParseUint(number, 10, 64)
	// Reject zero, signs, leading zero aliases and cross-operation cursor reuse.
	if !found || id != operationID || err != nil || sequence == 0 || strconv.FormatUint(sequence, 10) != number {
		return 0, model.Err("INVALID_EVENT_CURSOR", "Event cursor must identify this operation and a positive log sequence")
	}
	return sequence, nil
}

// validateNotification accepts only complete known operation and history-reset notification schemas.
func (w *operationWatch) validateNotification(event OperationEvent) error {
	// Event payload errors never echo the server's raw message or configuration contents.
	switch event.Event {
	// Operation snapshots must describe the watched operation and a recognized lifecycle state.
	case "operation":
		return w.validateSnapshot(event.Data)
	// A reset warns that older history was evicted, but does not acknowledge any retained log.
	case "reset":
		var reset operationReset
		// Unknown reset reasons must not silently discard the user's resume position.
		if json.Unmarshal(event.Data, &reset) != nil || reset.Reason != "history_expired" {
			return model.Err("EVENT_PROTOCOL", "Invalid operation history reset")
		}
		return nil
	// Unexpected data-bearing event kinds require a client upgrade rather than guessed semantics.
	default:
		return model.Err("EVENT_PROTOCOL", "Unsupported operation event")
	}
}

// validateSnapshot records a terminal boundary only for a valid operation-scoped status payload.
func (w *operationWatch) validateSnapshot(data []byte) error {
	var snapshot operationSnapshot
	// An operation event from another job must never terminate the current watch.
	if json.Unmarshal(data, &snapshot) != nil || snapshot.ID != w.operationID {
		return model.Err("EVENT_PROTOCOL", "Invalid operation status identity")
	}
	w.terminal = OperationTerminal(snapshot.Status)
	// Unknown statuses cannot be treated as successful completion or indefinite queued work.
	if !w.terminal && !operationActive(snapshot.Status) {
		return model.Err("EVENT_PROTOCOL", "Unknown operation status")
	}
	return nil
}

// OperationTerminal recognizes established terminal outcomes without imposing an overall watch deadline.
func OperationTerminal(status string) bool {
	return OperationSucceeded(status) || operationFailed(status)
}

// OperationSucceeded recognizes successful operation snapshots from current and older compatible controllers.
func OperationSucceeded(status string) bool {
	return status == "succeeded" || status == "completed" || status == "success"
}

// operationFailed groups failure and manual-recovery outcomes that end observation.
func operationFailed(status string) bool {
	// Manual recovery and superseded work are terminal even though the server remains available.
	switch status {
	// Failure and manual-recovery outcomes terminate observation without claiming success.
	case "failed", "cancelled", "interrupted", "needs_attention", "superseded":
		return true
	}
	return false
}

// operationActive accepts known in-flight statuses while requiring explicit terminal snapshots to finish.
func operationActive(status string) bool {
	return status == "queued" || status == "running" || status == "accepted" || status == "pending"
}

// deliver marks consumer failures so local EOF cannot be mistaken for a reconnectable server disconnect.
func (w *operationWatch) deliver(event OperationEvent) error {
	// Acknowledgement belongs to the consumer and must succeed before cursor advancement.
	if err := w.consume(event); err != nil {
		return &eventConsumerFailure{cause: err}
	}
	return nil
}
