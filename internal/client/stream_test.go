package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// chunkedEventReader forces transport-sized reads smaller than any complete SSE field.
type chunkedEventReader struct {
	reader io.Reader
	size   int
}

// Read exposes split field names, JSON payloads and frame separators without changing byte order.
func (r chunkedEventReader) Read(p []byte) (int, error) {
	return r.reader.Read(p[:min(len(p), r.size)])
}

// eventTransport injects bounded HTTP responses without external network or cloud dependencies.
type eventTransport func(*http.Request) (*http.Response, error)

// RoundTrip supplies the exact response sequence requested by a stream fixture.
func (f eventTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// eventResponse returns a small SSE or typed-error HTTP response body.
func eventResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
}

// logEvent supplies a sanitized log whose wire cursor agrees with its durable sequence.
func logEvent(sequence uint64) string {
	return fmt.Sprintf("event: log\nid: job:%d\ndata: {\"operationId\":\"job\",\"sequence\":%d,\"message\":\"[REDACTED]\"}\n\n", sequence, sequence)
}

// statusEvent supplies an operation-scoped lifecycle snapshot without a cursor.
func statusEvent(status string) string {
	return "event: operation\ndata: {\"id\":\"job\",\"status\":\"" + status + "\"}\n\n"
}

// TestOperationParserSplitFramesReplayAndHeartbeats checks framing independently of HTTP packet boundaries.
func TestOperationParserSplitFramesReplayAndHeartbeats(t *testing.T) {
	var events []OperationEvent
	// Record consumed progress to prove replay duplicates and heartbeats are not delivered as new logs.
	watch := &operationWatch{operationID: "job", consume: func(event OperationEvent) error { events = append(events, event); return nil }}
	stream := ": heartbeat\r\n\r\n" + logEvent(1) + logEvent(1) + "event: reset\ndata: {\"reason\":\"history_expired\"}\n\n" + logEvent(3) + statusEvent("succeeded")
	err := watch.readOperationFrames(chunkedEventReader{reader: strings.NewReader(stream), size: 1})
	// Only unique logs, the reset notification and final status are application events.
	if err != nil || len(events) != 4 || watch.cursor != "job:3" || !watch.terminal {
		t.Fatalf("incorrect framing/replay: %v, events=%d, cursor=%s", err, len(events), watch.cursor)
	}
}

// TestOperationParserRejectsInvalidFrames keeps malformed identity and lifecycle data out of consumers.
func TestOperationParserRejectsInvalidFrames(t *testing.T) {
	cases := []string{
		"event: unknown\ndata: {}\n\n",
		strings.Replace(logEvent(1), "id: job:1", "id: other:1", 1),
		strings.Replace(logEvent(1), "\"sequence\":1", "\"sequence\":2", 1),
		strings.Replace(logEvent(1), "id: job:1\n", "", 1),
		strings.Replace(logEvent(1), "id: job:1", "id: job:01", 1),
		strings.Replace(logEvent(1), "id: job:1", "id: job:1\nid: job:1", 1),
		strings.Replace(statusEvent("succeeded"), "\"job\"", "\"other\"", 1),
		statusEvent("unrecognized"),
		"event: reset\ndata: {\"reason\":\"unknown\"}\n\n",
		"event: operation\nid: job:1\ndata: {}\n\n",
		"event: log\nid: job:1\ndata: not-json-secret\n\n",
	}
	// Every invalid frame must fail before its payload is acknowledged or reproduced in an error.
	for _, input := range cases {
		checkInvalidOperationFrame(t, input)
	}
}

// checkInvalidOperationFrame asserts safe semantic errors without printing attacker-controlled frame data.
func checkInvalidOperationFrame(t *testing.T, input string) {
	consumed := false
	// A validation failure must occur before application-level delivery.
	watch := &operationWatch{operationID: "job", consume: func(OperationEvent) error { consumed = true; return nil }}
	err := watch.readOperationFrames(strings.NewReader(input))
	// Raw server payloads must not leak through parser diagnostics or advance a resume position.
	if err == nil || consumed || watch.cursor != "" || strings.Contains(err.Error(), "not-json-secret") {
		t.Fatal("invalid event was consumed or leaked its contents")
	}
}

// TestOperationParserBoundsAndPartialDisconnect checks both memory bounds and unacknowledged partial frames.
func TestOperationParserBoundsAndPartialDisconnect(t *testing.T) {
	// This consumer should never see either an oversized field or a frame missing its final separator.
	watch := &operationWatch{operationID: "job", consume: func(OperationEvent) error { t.Fatal("partial or oversized event consumed"); return nil }}
	err := watch.readOperationFrames(strings.NewReader("data: " + strings.Repeat("x", MaxOperationFrameBytes+1)))
	// Oversized frames are permanent protocol-limit failures rather than reconnectable EOF.
	if !eventErrorCode(err, "EVENT_LIMIT") {
		t.Fatal("oversized event did not fail its bound")
	}
	err = watch.readOperationFrames(strings.NewReader(strings.TrimSuffix(logEvent(1), "\n\n")))
	// A disconnected partial frame is replayable because no cursor was committed.
	if !errors.Is(err, io.EOF) || watch.cursor != "" {
		t.Fatal("partial event advanced cursor")
	}
}

// TestOperationConsumerFailureDoesNotAcknowledge preserves replay after an output consumer fails.
func TestOperationConsumerFailureDoesNotAcknowledge(t *testing.T) {
	failure := errors.New("output failed")
	// Consumer acknowledgement is deliberately withheld for this otherwise valid log.
	watch := &operationWatch{operationID: "job", consume: func(OperationEvent) error { return failure }}
	err := watch.readOperationFrames(strings.NewReader(logEvent(1)))
	// Neither sequence nor cursor may move before successful consumption.
	if !errors.Is(err, failure) || watch.sequence != 0 || watch.cursor != "" {
		t.Fatal("failed consumer advanced resume state")
	}
}

// TestWatchOperationReconnectsWithConsumedCursor checks HTTP reconnect headers and duplicate suppression end to end.
func TestWatchOperationReconnectsWithConsumedCursor(t *testing.T) {
	calls := 0
	logs := 0
	// Split the final log across a disconnected response, then replay already consumed history on reconnect.
	transport := eventTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		return reconnectResponse(t, request, calls)
	})
	c := &Client{URL: "https://controller.test", Token: "fixture-token", HTTP: &http.Client{Transport: transport}}
	// Count only log events; operation snapshots must not masquerade as additional durable progress.
	err := c.WatchOperation(context.Background(), "job", "", func(event OperationEvent) error {
		// Operation snapshots are not durable log progress.
		if event.Event == "log" {
			logs++
		}
		return nil
	})
	// Successful completion requires the terminal snapshot from the second connection.
	if err != nil || calls != 2 || logs != 2 {
		t.Fatalf("reconnect failed: %v, requests=%d, logs=%d", err, calls, logs)
	}
}

// reconnectResponse verifies transport identity and the exact acknowledged resume position.
func reconnectResponse(t *testing.T, request *http.Request, call int) (*http.Response, error) {
	// Every reconnect is an authenticated read against the configured origin.
	if request.Method != "GET" || request.Header.Get("Authorization") != "Bearer fixture-token" {
		t.Fatal("unsafe stream request")
	}
	// The first connection has no resume cursor and ends before completing its second log frame.
	if call == 1 {
		// Initial observation must never invent a cursor before receiving progress.
		if request.Header.Get("Last-Event-ID") != "" {
			t.Fatal("unexpected initial cursor")
		}
		return eventResponse(200, statusEvent("running")+logEvent(1)+"event: log\nid: job:2\ndata: {"), nil
	}
	// Partial frames must not advance the resume token beyond the last successfully consumed log.
	if request.Header.Get("Last-Event-ID") != "job:1" {
		t.Fatal("incorrect reconnect cursor")
	}
	return eventResponse(200, logEvent(1)+logEvent(2)+statusEvent("succeeded")), nil
}

// TestWatchCancellationNeverCancelsServer checks cancellation between bounded stream connections.
func TestWatchCancellationNeverCancelsServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	// Return a single valid log; cancellation occurs only in its local consumer.
	c := &Client{URL: "https://controller.test", HTTP: &http.Client{Transport: eventTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		return eventResponse(200, logEvent(1)), nil
	})}}
	// Stop observing immediately after receiving progress without sending a mutation.
	err := c.WatchOperation(ctx, "job", "", func(OperationEvent) error { cancel(); return nil })
	// Context cancellation must be distinguishable from server operation failure.
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("unexpected cancellation: %v, requests=%d", err, calls)
	}
}

// eventErrorCode matches stable semantic errors without depending on potentially sensitive provider text.
func eventErrorCode(err error, code string) bool {
	var failure *model.Error
	return errors.As(err, &failure) && failure.Code == code
}

// TestOperationMultilineData checks valid SSE data folding before JSON schema validation.
func TestOperationMultilineData(t *testing.T) {
	count := 0
	// Multiline JSON is one event even when the server folds it across data fields.
	watch := &operationWatch{operationID: "job", consume: func(event OperationEvent) error {
		var object map[string]any
		err := json.Unmarshal(event.Data, &object)
		count++
		return err
	}}
	frame := "event: log\nid: job:1\ndata: {\"operationId\":\"job\",\ndata: \"sequence\":1}\n\n" + statusEvent("failed")
	// A failed operation is still a successfully consumed terminal snapshot at the client transport layer.
	if err := watch.readOperationFrames(strings.NewReader(frame)); err != nil || count != 2 {
		t.Fatal("multiline SSE data was not preserved")
	}
}
