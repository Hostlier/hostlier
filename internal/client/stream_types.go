package client

import (
	"encoding/json"
	"errors"
)

// MaxOperationFrameBytes bounds a complete streamed operation or log payload before JSON decoding.
const MaxOperationFrameBytes = 4 * 1024 * 1024

// ErrEventsUnsupported permits polling only after an existing operation's server lacks the events endpoint.
var ErrEventsUnsupported = errors.New("controller does not support operation event streams")

// OperationEvent contains one sanitized server event and its optional durable log cursor.
type OperationEvent struct {
	Event string          `json:"event"`
	ID    string          `json:"id,omitempty"`
	Data  json.RawMessage `json:"data"`
}

// operationFrame accumulates bounded SSE fields until an explicit frame boundary.
type operationFrame struct {
	event, id string
	data      []string
	size      int
	hasID     bool
}

// operationWatch keeps only the last successfully consumed log cursor across reconnects.
type operationWatch struct {
	client              *Client
	operationID, cursor string
	sequence            uint64
	consume             func(OperationEvent) error
	terminal            bool
}

// operationLog validates cursor identity without exposing or interpreting the sanitized message text.
type operationLog struct {
	OperationID string `json:"operationId"`
	Sequence    uint64 `json:"sequence"`
}

// operationSnapshot validates operation identity and lifecycle independently of the full retained payload.
type operationSnapshot struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// operationReset identifies the bounded-history replay notification.
type operationReset struct {
	Reason string `json:"reason"`
}

// eventConsumerFailure distinguishes failed local delivery from an identically named transport EOF.
type eventConsumerFailure struct{ cause error }

// Error preserves the caller's local delivery diagnostic without exposing server payloads.
func (e *eventConsumerFailure) Error() string { return e.cause.Error() }

// Unwrap allows callers to retain the original writer or consumer error identity.
func (e *eventConsumerFailure) Unwrap() error { return e.cause }
