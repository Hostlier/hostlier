package store

import (
	"context"
	"fmt"
	"hostlier/internal/model"
	"testing"
	"time"
)

func TestActivityHistorySurvivesRingAndRestart(t *testing.T) {
	dir := t.TempDir()
	var state struct {
		Events []Activity `json:"events"`
	}
	s, err := Open(dir, &state)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	// Saving separate bounded snapshots retains events removed from the replay ring.
	for batch := 0; batch < 2; batch++ {
		state.Events = nil
		for i := 1; i <= 600; i++ {
			n := batch*600 + i
			state.Events = append(state.Events, Activity{Sequence: int64(n), Timestamp: now.Add(-time.Duration(1200-n) * time.Second).Format(time.RFC3339Nano), Application: "sample", Level: "info", Message: fmt.Sprintf("event %d", n)})
		}
		if err = s.Save(state); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(dir, &state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	filter := model.HistoryFilter{From: now.Add(-time.Hour), To: now.Add(time.Second), Limit: 200, Search: "event"}
	count := 0
	seen := map[int64]bool{}
	for {
		page, err := s.Activity(context.Background(), filter, "sample", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range page.Entries {
			if seen[entry.Sequence] {
				t.Fatal("duplicate", entry.Sequence)
			}
			seen[entry.Sequence] = true
			count++
		}
		if page.NextBefore == 0 {
			break
		}
		filter.Before = page.NextBefore
		filter.BeforeTime, _ = time.Parse(time.RFC3339Nano, page.NextBeforeTime)
	}
	if count != 1200 {
		t.Fatal("lost archived events", count)
	}
	filter.Before = 0
	filter.BeforeTime = time.Time{}
	filter.Search = "event 1200"
	filter.From = now
	page, err := s.Activity(context.Background(), filter, "sample", "")
	if err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	filter.To = now
	filter.From = now.Add(-time.Second)
	page, err = s.Activity(context.Background(), filter, "sample", "")
	if err != nil || len(page.Entries) != 0 {
		t.Fatal("end must be exclusive", page, err)
	}
	filter.To = now.Add(time.Second)
	filter.From = now.Add(-time.Hour)
	page, err = s.Activity(context.Background(), filter, "other", "")
	if err != nil || len(page.Entries) != 0 {
		t.Fatal("cross-app leak", page, err)
	}
}

func TestActivityRetentionAndSnapshotAtomicity(t *testing.T) {
	state := map[string]any{}
	s, err := Open(t.TempDir(), &state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state["events"] = []Activity{{Sequence: 1, Timestamp: time.Now().Add(-31 * 24 * time.Hour).UTC().Format(time.RFC3339Nano), Message: "expired"}, {Sequence: 2, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Message: "retained"}}
	if err = s.Save(state); err != nil {
		t.Fatal(err)
	}
	page, err := s.Activity(context.Background(), model.HistoryFilter{Limit: 100}, "", "")
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Message != "retained" {
		t.Fatal(page, err)
	}
	before, _ := s.Snapshot()
	s.db.Exec("CREATE TRIGGER reject_activity BEFORE INSERT ON activity_history BEGIN SELECT RAISE(ABORT,'test'); END")
	state["events"] = []Activity{{Sequence: 3, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Message: "uncommitted"}}
	if err = s.Save(state); err == nil {
		t.Fatal("failed archive committed snapshot")
	}
	after, _ := s.Snapshot()
	if string(before) != string(after) {
		t.Fatal("snapshot not atomic")
	}
}
