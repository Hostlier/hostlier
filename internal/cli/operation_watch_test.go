package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// eventRequester supplies explicit stream capability without requiring a live controller or changing legacy fixtures.
type eventRequester struct {
	approval approvalSimulation
	events   []client.OperationEvent
	failure  error
	cursor   string
	watches  int
	requests []string
}

// Request records durable acceptance and compatibility reads while rejecting accidental server cancellation requests.
func (r *eventRequester) Request(_ context.Context, method, path string, body any, result any) error {
	handled, call, err := r.approval.respond(method, path, body, result)
	// Select the fixture behavior required to exercise this boundary.
	if handled {
		return err
	}
	method, path = call.method, call.path
	r.requests = append(r.requests, method+" "+path)
	// Existing lifecycle commands receive their durable receipt before streaming begins.
	if method == "POST" {
		return json.Unmarshal([]byte(`{"operationId":"job"}`), result)
	}
	return json.Unmarshal([]byte(`{"id":"job","status":"succeeded"}`), result)
}

// WatchOperation captures explicit resume state and acknowledges each fixture event through the real CLI consumer.
func (r *eventRequester) WatchOperation(ctx context.Context, _ string, cursor string, consume func(client.OperationEvent) error) error {
	r.watches++
	r.cursor = cursor
	// Local cancellation must remain distinct from a durable operation failing on the server.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Replay only the supplied sanitized controller events.
	for _, event := range r.events {
		// A failed progress writer prevents event acknowledgement and ends this observation.
		if err := consume(event); err != nil {
			return err
		}
	}
	return r.failure
}

// cliOperationEvents supplies in-flight status, redacted durable progress and one authoritative terminal result.
func cliOperationEvents(status string) []client.OperationEvent {
	return []client.OperationEvent{
		{Event: "operation", Data: json.RawMessage(`{"id":"job","status":"running"}`)},
		{Event: "log", ID: "job:7", Data: json.RawMessage(`{"operationId":"job","sequence":7,"message":"password [REDACTED]"}`)},
		{Event: "operation", Data: json.RawMessage(`{"id":"job","status":"` + status + `"}`)},
	}
}

// runEventCLI executes the normal command entry point with isolated profiles and distinct machine/progress outputs.
func runEventCLI(t *testing.T, ctx context.Context, requester *eventRequester, args []string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := RunWithOptions(ctx, args, strings.NewReader(""), &stdout, &stderr, Options{Client: requester, Home: t.TempDir()})
	return stdout.String(), stderr.String(), err
}

// TestLifecycleSSESeparatesProgressFromReceiptAndResult preserves automation-friendly stdout during normal operation waits.
func TestLifecycleSSESeparatesProgressFromReceiptAndResult(t *testing.T) {
	requester := &eventRequester{events: cliOperationEvents("succeeded")}
	stdout, stderr, err := runEventCLI(t, context.Background(), requester, []string{"restart", "app"})
	// Exactly the receipt and final operation belong on stdout, with no intermediate running or log payload.
	if err != nil || len(strings.Split(strings.TrimSpace(stdout), "\n")) != 2 || strings.Contains(stdout, "REDACTED") || strings.Contains(stdout, "running") {
		t.Fatal("progress polluted receipt/final JSON output")
	}
	// The redacted log and durable cursor belong on stderr so an interrupted user can resume precisely.
	if !strings.Contains(stderr, "job:7") || !strings.Contains(stderr, "[REDACTED]") || requester.watches != 1 || len(requester.requests) != 1 {
		t.Fatal("stream progress or cursor missing")
	}
}

// TestOperationWatchResumesWithoutMutation exposes an existing operation's stream without creating new server work.
func TestOperationWatchResumesWithoutMutation(t *testing.T) {
	requester := &eventRequester{events: cliOperationEvents("succeeded")}
	stdout, _, err := runEventCLI(t, context.Background(), requester, []string{"operation", "job", "--watch", "--cursor", "job:6"})
	// Explicit observation forwards the supplied cursor and prints only the terminal operation snapshot.
	if err != nil || requester.cursor != "job:6" || len(requester.requests) != 0 || strings.Contains(stdout, "operationId") {
		t.Fatal("resume created work or lost cursor")
	}
}

// TestStreamTerminalFailureKeepsFinalJSON returns a command failure after successfully observing server failure.
func TestStreamTerminalFailureKeepsFinalJSON(t *testing.T) {
	requester := &eventRequester{events: cliOperationEvents("failed")}
	stdout, _, err := runEventCLI(t, context.Background(), requester, []string{"operation", "job", "--watch"})
	// Failed server work remains visible as final JSON and a stable non-success command error.
	if !operationCommandError(err, "OPERATION_FAILED") || !strings.Contains(stdout, `"status":"failed"`) {
		t.Fatal("terminal failure was reported as success")
	}
}

// TestDetachedLifecycleDoesNotOpenStream keeps existing durable-receipt background behavior unchanged.
func TestDetachedLifecycleDoesNotOpenStream(t *testing.T) {
	requester := &eventRequester{events: cliOperationEvents("succeeded")}
	stdout, stderr, err := runEventCLI(t, context.Background(), requester, []string{"restart", "app", "--detach"})
	// Detachment returns after acceptance and does not subscribe or print progress.
	if err != nil || requester.watches != 0 || stderr != "" || !strings.Contains(stdout, `"operationId":"job"`) {
		t.Fatal("detach subscribed to progress")
	}
}

// TestWatchCancellationPreservesDurableReceipt reports pending work without issuing a cancellation mutation.
func TestWatchCancellationPreservesDurableReceipt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	requester := &eventRequester{}
	stdout, _, err := runEventCLI(t, ctx, requester, []string{"restart", "app"})
	// The accepted job remains identifiable even when the local observer is already cancelled.
	if !operationCommandError(err, "OPERATION_PENDING") || !strings.Contains(stdout, "job") || len(requester.requests) != 1 {
		t.Fatal("cancellation lost receipt or mutated server work")
	}
}

// TestStreamFallbackAndSemanticFailures separates an explicit capability gap from authentication or framing errors.
func TestStreamFallbackAndSemanticFailures(t *testing.T) {
	requester := &eventRequester{failure: client.ErrEventsUnsupported}
	stdout, _, err := runEventCLI(t, context.Background(), requester, []string{"operation", "job", "--watch"})
	// An older server still completes via the established authenticated operation status endpoint.
	if err != nil || !strings.Contains(stdout, "succeeded") || len(requester.requests) != 1 {
		t.Fatal("verified old-server fallback failed")
	}
	requester = &eventRequester{failure: model.Err("UNAUTHORIZED", "Unauthorized")}
	_, _, err = runEventCLI(t, context.Background(), requester, []string{"operation", "job", "--watch"})
	// Authentication and semantic failures never fall back into polling.
	if !operationCommandError(err, "UNAUTHORIZED") || len(requester.requests) != 0 {
		t.Fatal("authentication failure entered polling fallback")
	}
}

// TestProgressWriterFailureStopsObservation preserves output delivery errors without switching to polling.
func TestProgressWriterFailureStopsObservation(t *testing.T) {
	requester := &eventRequester{events: cliOperationEvents("succeeded")}
	err := RunWithOptions(context.Background(), []string{"operation", "job", "--watch"}, strings.NewReader(""), io.Discard, failedProgressWriter{}, Options{Client: requester, Home: t.TempDir()})
	// Broken stderr is a consumer failure, not a successful log acknowledgement or missing-SSE capability.
	if !errors.Is(err, io.ErrClosedPipe) || len(requester.requests) != 0 {
		t.Fatal("progress writer failure was hidden")
	}
}

// failedProgressWriter models a closed progress output independently of machine-readable stdout.
type failedProgressWriter struct{}

// Write rejects every progress event to verify acknowledgement depends on successful delivery.
func (failedProgressWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// operationCommandError compares stable CLI outcomes without depending on human-facing wording.
func operationCommandError(err error, code string) bool {
	var failure *model.Error
	return errors.As(err, &failure) && failure.Code == code
}
