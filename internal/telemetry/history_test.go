package telemetry

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestTimeSearchPaginationAndLateArrival(t *testing.T) {
	s := testStore(t)
	now := s.now()
	ctx := context.Background()
	// Arrival order differs from event time; a timestamp tie requires the ID cursor too.
	for _, offset := range []time.Duration{-time.Hour, -2 * time.Hour, -time.Hour, 0} {
		if _, err := s.Ingest(ctx, "logs", logBody(now.Add(offset)), Scope{"c", "k3s"}, resolveFixture); err != nil {
			t.Fatal(err)
		}
	}
	query := Query{Signal: "logs", Application: "verified-app", From: now.Add(-90 * time.Minute), To: now, Search: "sensitive", Level: "info", Limit: 1}
	first, err := s.Query(ctx, query)
	if err != nil || len(first.Records) != 1 || first.Records[0].ID != 3 || first.NextBefore == 0 {
		t.Fatal(first, err)
	}
	query.Before = first.NextBefore
	query.BeforeTime, _ = time.Parse(time.RFC3339Nano, first.NextBeforeTime)
	second, err := s.Query(ctx, query)
	if err != nil || len(second.Records) != 1 || second.Records[0].ID != 1 || second.NextBefore != 0 {
		t.Fatal(second, err)
	}
	query.Search = "%"
	none, err := s.Query(ctx, query)
	if err != nil || len(none.Records) != 0 {
		t.Fatal("search treated as wildcard", none, err)
	}
	query.Search = "sensitive"
	query.Level = "error"
	none, err = s.Query(ctx, query)
	if err != nil || len(none.Records) != 0 {
		t.Fatal(none, err)
	}
}

func TestOldTelemetrySchemaMigratesObservedTime(t *testing.T) {
	s := testStore(t)
	// Recreate the old table to verify upgrade of an existing payload, not just fresh databases.
	_, err := s.db.Exec("DROP TABLE records; CREATE TABLE records(id INTEGER PRIMARY KEY AUTOINCREMENT,signal TEXT,application TEXT,revision TEXT,component TEXT,received INTEGER,payload BLOB)")
	if err != nil {
		t.Fatal(err)
	}
	stamp := s.now().Add(-time.Hour)
	payload, _ := json.Marshal(Record{Signal: "logs", ObservedAt: stamp, ReceivedAt: s.now()})
	if _, err = s.db.Exec("INSERT INTO records(signal,application,revision,component,received,payload) VALUES('logs','','','',?,?)", s.now().UnixNano(), payload); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateObservedTime(); err != nil {
		t.Fatal(err)
	}
	var observed int64
	if err = s.db.QueryRow("SELECT observed FROM records").Scan(&observed); err != nil || observed != stamp.UnixNano() {
		t.Fatal(observed, err)
	}
}
