package snapshot

import (
	"strconv"
	"testing"
)

// TestSlowSubscriberCoalescesWithoutBlocking proves fanout is bounded by subscribers, not captures.
func TestSlowSubscriberCoalescesWithoutBlocking(t *testing.T) {
	hub := New()
	updates, stop := hub.Subscribe()
	defer stop()
	// A subscriber that never drains its notification still permits arbitrary future captures.
	for i := 0; i < 10000; i++ {
		hub.Publish(strconv.Itoa(i))
	}
	<-updates
	// Every pending notification refers to the latest complete replacement.
	if hub.Latest() != "9999" {
		t.Fatal("cache lost newest capture")
	}
	// Only one wakeup can remain queued for a slow consumer.
	select {
	// A second queued notification would violate coalescing.
	case <-updates:
		t.Fatal("unbounded queued notifications")
	// An empty queue confirms bounded notification memory.
	default:
	}
	stop()
	stop()
	hub.Publish("after unsubscribe")
}
