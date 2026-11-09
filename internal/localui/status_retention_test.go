package localui

import (
	"context"
	"encoding/json"
	"testing"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// retentionRemote records the browser's immutable observations across loss and authority replacement.
type retentionRemote struct {
	uiRemote
	server       *Server
	publications []string
}

// WatchStatus supplies a complete capture, a transient loss, an explicit reset and a replacement.
func (r *retentionRemote) WatchStatus(_ context.Context, consume func(client.StatusEvent) error) error {
	events := []client.StatusEvent{
		{Connected: true, Snapshot: &model.StatusSnapshot{ID: "a:1", Hostname: "first"}},
		{Connected: false},
		{Reset: true},
		{Connected: true, Snapshot: &model.StatusSnapshot{ID: "b:1", Hostname: "second"}},
	}
	// Capture each serialized publication before the next event replaces the shared cache.
	for _, event := range events {
		// A failed consumer cannot acknowledge or advance an observation.
		if err := consume(event); err != nil {
			return err
		}
		r.publications = append(r.publications, r.server.bus.Latest())
	}
	return nil
}

// TestObservationRetainsTransientDataButPublishesAuthorityReset protects cross-controller cache isolation.
func TestObservationRetainsTransientDataButPublishesAuthorityReset(t *testing.T) {
	remote := &retentionRemote{}
	server, err := New(remote, "http://127.0.0.1:7777")
	// The observer must have a real local session before accepting any upstream event.
	if err != nil {
		t.Fatal(err)
	}
	remote.server = server
	server.observe(context.Background())
	// Every upstream event produces one immutable local replacement.
	if len(remote.publications) != 4 {
		t.Fatal("missing observation transitions")
	}
	assertRetainedObservation(t, remote.publications[0], "first", false)
	assertRetainedObservation(t, remote.publications[1], "first", false)
	assertRetainedObservation(t, remote.publications[2], "", true)
	assertRetainedObservation(t, remote.publications[3], "second", false)
}

// assertRetainedObservation compares public authority and cache state without private transport material.
func assertRetainedObservation(t *testing.T, data, hostname string, reset bool) {
	t.Helper()
	var event client.StatusEvent
	// The browser must receive a complete JSON event rather than a partial replacement.
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		t.Fatal(err)
	}
	observed := ""
	// An explicit reset is the only transition with no retained hostname.
	if event.Snapshot != nil {
		observed = event.Snapshot.Hostname
	}
	// Reset provenance remains visible even when the replacement capture has not arrived yet.
	if observed != hostname || event.Reset != reset {
		t.Fatalf("hostname=%q reset=%v, want hostname=%q reset=%v", observed, event.Reset, hostname, reset)
	}
}
