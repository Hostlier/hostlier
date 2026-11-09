package observation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"hostlier/internal/model"
)

// Publish queues one sanitized replacement without waiting for disk, broker acknowledgements or viewers.
func (b *Embedded) Publish(value string) {
	// Closing a bus must not retain new pending data or wake a stopped publisher.
	if b.ctx.Err() != nil {
		return
	}
	value, ok := publicSnapshot(value)
	// Invalid or oversized internal payloads cannot contaminate retained telemetry.
	if !ok {
		return
	}
	b.pending.Publish(value)
}

// publicSnapshot projects retained payloads through the public DTO, dropping unknown private fields.
func publicSnapshot(value string) (string, bool) {
	// Reject oversized input before JSON parsing can allocate its application inventory.
	if len(value) > maxSnapshotBytes {
		return "", false
	}
	var snapshot model.StatusSnapshot
	// Snapshots carry explicit identity and bounded inventories; arbitrary events are unsupported.
	if json.Unmarshal([]byte(value), &snapshot) != nil || snapshot.ID == "" || len(snapshot.ID) > 200 {
		return "", false
	}
	// Public stream limits must match the CLI's replacement snapshot contract.
	if len(snapshot.Applications) > 128 || len(snapshot.Operations) > 128 {
		return "", false
	}
	data, err := json.Marshal(snapshot)
	return string(data), err == nil && len(data) <= maxSnapshotBytes
}

// publishLoop serializes durable publication without blocking capture workers or ordinary controller requests.
func (b *Embedded) publishLoop(updates <-chan struct{}) {
	// One coalescing wakeup represents any number of superseded pending captures.
	for {
		// Shutdown cancels broker requests instead of waiting for a viewer to consume data.
		select {
		// No accepted controller command depends on draining this ephemeral pending queue.
		case <-b.ctx.Done():
			return
		// Publication takes place only after a capture produced a complete public DTO.
		case <-updates:
			b.publishPending()
		}
	}
}

// publishPending retries the newest replacement with stable deduplication identity after ambiguous acks.
func (b *Embedded) publishPending() {
	delay := 100 * time.Millisecond
	// Retry is bounded by controller lifetime, individual request deadlines and capped backoff.
	for b.ctx.Err() == nil {
		value := b.pending.Latest()
		var identity struct {
			ID string `json:"id"`
		} // The full DTO was validated before entering the queue.
		json.Unmarshal([]byte(value), &identity)
		ctx, cancel := context.WithTimeout(b.ctx, time.Second)
		_, err := b.js.Publish(ctx, statusSubject, []byte(value), jetstream.WithMsgID(identity.ID))
		cancel()
		// A durable ack completes this attempt; cache visibility still comes through the consumer.
		if err == nil {
			return
		}
		// Newer captures replace retry intent; old telemetry need not delay fresh observations.
		if !observationPause(b.ctx, delay) {
			return
		}
		delay = min(delay*2, time.Second)
	}
}

// consumeLoop maintains one acknowledged JetStream reader regardless of CLI or browser viewer count.
func (b *Embedded) consumeLoop() {
	// Pulling one message bounds both retained in-flight data and acknowledgement state.
	for b.ctx.Err() == nil {
		ctx, cancel := context.WithTimeout(b.ctx, time.Second)
		message, err := b.consumer.Next(jetstream.FetchContext(ctx))
		cancel()
		// Idle pull timeouts and transient broker errors do not change controller execution authority.
		if err != nil {
			// Backoff bounds disconnected-broker errors while cancellation releases the worker promptly.
			if !observationPause(b.ctx, 100*time.Millisecond) {
				return
			}
			continue
		} // Backoff also bounds unexpected fast failures.
		b.consumeSnapshot(message)
	}
}

// consumeSnapshot updates immutable fanout before acknowledging; redelivery cannot regress cached state.
func (b *Embedded) consumeSnapshot(message jetstream.Msg) {
	metadata, err := message.Metadata()
	// Malformed internal envelopes cannot acquire a committed observation identity.
	if err != nil {
		return
	}
	b.acceptSnapshot(metadata.Sequence.Stream, string(message.Data()))
	ctx, cancel := context.WithTimeout(b.ctx, time.Second)
	defer cancel()
	// Ack loss permits idempotent redelivery; a successful cache update never acknowledges a browser read.
	message.DoubleAck(ctx)
}

// acceptSnapshot suppresses duplicate and older stream deliveries after restart or ack loss.
func (b *Embedded) acceptSnapshot(sequence uint64, value string) {
	// The single consumer owns this high watermark; initialization completes before it starts.
	if sequence <= b.lastSequence {
		return
	}
	value, ok := publicSnapshot(value)
	// Invalid retained data is skipped rather than exposed through local fanout.
	if !ok {
		return
	}
	b.lastSequence = sequence
	b.latest.Publish(value)
}

// observationPause backs off worker retries while allowing immediate controller shutdown.
func observationPause(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	// No retry loop spins or uses a blocking sleep during shutdown.
	select {
	// The bus no longer has any controller-owned work to perform.
	case <-ctx.Done():
		return false
	// The next bounded retry may proceed.
	case <-timer.C:
		return true
	}
}
