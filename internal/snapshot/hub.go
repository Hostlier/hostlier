// Package snapshot distributes replaceable snapshots without coupling consumers to capture.
package snapshot

import "sync"

// Bus is the controller's internal observation delivery boundary.
// Embedded JetStream preserves these latest-state and bounded subscriber semantics.
type Bus interface {
	// Latest returns immutable JSON; an empty string means no completed capture yet.
	Latest() string
	// Publish replaces cached state and wakes subscribers without waiting for them.
	Publish(string)
	// Subscribe signals that callers should read Latest; notifications may coalesce.
	Subscribe() (<-chan struct{}, func())
}

// Hub stores one immutable snapshot and one pending notification per subscriber.
type Hub struct {
	mu     sync.Mutex
	latest string
	subs   map[chan struct{}]struct{}
}

// New constructs an empty in-process bus.
func New() *Hub { return &Hub{subs: make(map[chan struct{}]struct{})} }

// Latest reads immutable serialized state under a short independent lock.
func (h *Hub) Latest() string { h.mu.Lock(); defer h.mu.Unlock(); return h.latest }

// Publish coalesces updates so a slow viewer cannot block capture or grow a queue.
func (h *Hub) Publish(value string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latest = value
	// Notifications convey replacement availability, never an unbounded event history.
	for sub := range h.subs {
		// Keep at most one pending wakeup for each consumer.
		select {
		// A ready consumer will fetch the newest snapshot.
		case sub <- struct{}{}:
		// An existing notification already guarantees a latest-state refresh.
		default:
		}
	}
}

// Subscribe registers before consumers read Latest, preventing a missed update at attachment.
func (h *Hub) Subscribe() (<-chan struct{}, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sub := make(chan struct{}, 1)
	h.subs[sub] = struct{}{}
	// Unsubscription is idempotent and never closes a channel beneath a publisher.
	return sub, func() { h.mu.Lock(); delete(h.subs, sub); h.mu.Unlock() }
}
