package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// operationWatcher is an optional capability so existing injected request-only clients retain polling compatibility.
type operationWatcher interface {
	// WatchOperation delivers acknowledged resumable progress until terminal completion or observation cancellation.
	WatchOperation(context.Context, string, string, func(client.OperationEvent) error) error
}

// operationObserver owns terminal outcome tracking and output boundaries for one watched operation.
type operationObserver struct {
	session     *session
	operationID string
	terminal    bool
	failure     error
}

// operation exposes explicit durable operation inspection and resumable progress observation.
func (s *session) operation() error {
	// Require exactly one durable identity rather than accidentally watching a different positional value.
	if len(s.parsed.args) != 2 || s.sub == "" {
		return model.Err("ARGUMENTS", "Usage: operation ID [--watch] [--cursor CURSOR]")
	}
	// A resume cursor changes stream position and is meaningless for a one-shot status read.
	if s.flags["cursor"] != "" && s.flags["watch"] == "" {
		return model.Err("ARGUMENTS", "--cursor requires operation --watch")
	}
	// Watching an existing job never requires a new deployment approval or mutation.
	if s.flags["watch"] != "" {
		return s.waitForOperation(s.sub)
	}
	var result map[string]any
	// A one-shot read uses the same concise outcome format as streaming.
	if err := s.request("GET", "/v1/operations/"+url.PathEscape(s.sub), nil, &result); err != nil {
		return err
	}
	return s.printOperation(result)
}

// waitForOperation streams resumable progress when supported and preserves durable work on local cancellation.
func (s *session) waitForOperation(operationID string) error {
	// Reject an incomplete receipt before trying to observe an ambiguous server operation.
	if operationID == "" {
		return model.Err("OPERATION_RESPONSE", "Controller returned no operation ID")
	}
	// Detached commands retain the existing durable receipt-only behavior.
	if s.flags["detach"] != "" {
		return nil
	}
	observer := &operationObserver{session: s, operationID: operationID}
	// Use the production streaming capability without breaking existing request-only injected test clients.
	if watcher, ok := s.remote.(operationWatcher); ok && s.flags["on"] == "" {
		err := watcher.WatchOperation(s.ctx, operationID, s.flags["cursor"], observer.consume)
		// Poll only when the client proved an existing operation is hosted by an older server without SSE.
		if !errors.Is(err, client.ErrEventsUnsupported) {
			return observer.result(err)
		}
	}
	// A requested cursor cannot be honored by a legacy polling transport or older server.
	if s.flags["cursor"] != "" {
		return model.Err("EVENTS_UNSUPPORTED", "Controller does not support resumable operation events")
	}
	return observer.result(observer.poll())
}

// consume prints sanitized log/reset progress to stderr and terminal operation JSON only to stdout.
func (o *operationObserver) consume(event client.OperationEvent) error {
	// Status snapshots contain the full operation; only terminal snapshots are machine-readable command results.
	if event.Event == "operation" {
		var operation map[string]any
		// Preserve the output boundary even for a malformed injected stream implementation.
		if json.Unmarshal(event.Data, &operation) != nil {
			return model.Err("EVENT_PROTOCOL", "Invalid operation status response")
		}
		return o.snapshot(operation)
	}
	return o.session.progress(event)
}

// snapshot records a terminal outcome only after its JSON was successfully written to stdout.
func (o *operationObserver) snapshot(operation map[string]any) error {
	status, _ := operation["status"].(string)
	// In-flight status changes do not pollute the receipt/final-result stdout stream.
	if !client.OperationTerminal(status) {
		return nil
	}
	// A failed output write must not be mistaken for acknowledged terminal delivery.
	if err := o.session.printOperation(operation); err != nil {
		return err
	}
	o.terminal = true
	// Failed, interrupted and recovery-required jobs remain command failures despite successful observation.
	if !client.OperationSucceeded(status) {
		o.failure = model.Err("OPERATION_FAILED", "Operation "+status+"; inspect "+o.operationID)
	}
	return nil
}

// result distinguishes local observation cancellation from durable server completion or semantic stream failures.
func (o *operationObserver) result(err error) error {
	// A completely delivered terminal result wins over cancellation racing after its acknowledgement.
	if err == nil && o.terminal {
		return o.failure
	}
	// Ctrl-C stops waiting without cancelling, rolling back or losing the accepted server operation.
	if o.session.ctx.Err() != nil {
		return model.Err("OPERATION_PENDING", "Stopped waiting; operation continues on server; inspect "+o.operationID)
	}
	// Preserve authentication, cursor, framing and consumer errors instead of falling back silently.
	if err != nil {
		return err
	}
	// A successful streaming implementation must have delivered an authoritative terminal snapshot.
	if !o.terminal {
		return model.Err("EVENT_PROTOCOL", "Operation event stream ended before terminal status")
	}
	return o.failure
}

// poll provides unbounded context-cancellable observation for older servers and request-only injected clients.
func (o *operationObserver) poll() error {
	// Polling has no overall job deadline because server work remains durable independently of this invocation.
	for {
		var operation map[string]any
		// API semantic failures remain visible and never cancel the accepted operation.
		if err := o.session.request("GET", "/v1/operations/"+url.PathEscape(o.operationID), nil, &operation); err != nil {
			return err
		}
		// Reuse exactly the same terminal-result encoding and failure semantics as the stream path.
		if err := o.snapshot(operation); err != nil {
			return err
		}
		// Terminal success and failure both end observation after their final JSON was written.
		if o.terminal {
			return nil
		}
		// Honor local cancellation between compatibility requests.
		if err := o.session.pause(); err != nil {
			return err
		}
	}
}
