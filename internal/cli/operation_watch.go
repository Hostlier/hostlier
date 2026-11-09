package cli

import (
	"context"
	"encoding/json"
	"net/url"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// operationWatcher subscribes to the specified controller or enrolled host.
type operationWatcher interface {
	WatchOperationOn(context.Context, string, string, string, func(client.OperationEvent) error) error
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
	watcher, ok := s.remote.(operationWatcher)
	if !ok {
		return model.Err("EVENTS_UNSUPPORTED", "Controller does not support gRPC operation subscriptions")
	}
	return observer.result(watcher.WatchOperationOn(s.ctx, s.flags["on"], operationID, s.flags["cursor"], observer.consume))
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
