package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testStore supplies deterministic timestamps and always releases storage ownership.
func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil {
		t.Fatal(err)
	}
	// Keep this fixture callback isolated from production authority and clocks.
	s.now = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	// Keep this fixture callback isolated from production authority and clocks.
	t.Cleanup(func() { s.Close() })
	return s
}

// resolveFixture demonstrates controller attribution overriding untrusted resource claims.
func resolveFixture(r Resource) (Identity, error) {
	return Identity{Application: "verified-app", Revision: "rev1", Component: "cache", Node: "node1"}, nil
}

// metricBody generates a valid OTLP gauge request at the supplied sample timestamp.
func metricBody(stamp time.Time) []byte {
	return []byte(fmt.Sprintf(`{"resourceMetrics":[{"resource":{"attributes":[{"key":"io.hostlier.app","value":{"stringValue":"forged"}}]},"scopeMetrics":[{"metrics":[{"name":"container.memory.usage","unit":"By","gauge":{"dataPoints":[{"timeUnixNano":"%d","asInt":"123"}]}}]}]}]}`, stamp.UnixNano()))
}

// logBody generates standard OTLP log envelopes without leaking message bodies to errors.
func logBody(stamp time.Time) []byte {
	return []byte(fmt.Sprintf(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"timeUnixNano":"%d","severityText":"INFO","body":{"stringValue":"sensitive message"}}]}]}]}`, stamp.UnixNano()))
}

// TestIngestQueryRestart proves actual OTLP records persist with trusted identity attribution.
func TestIngestQueryRestart(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	result, err := s.Ingest(ctx, "metrics", metricBody(s.now()), Scope{"collector", "docker"}, resolveFixture)
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil || result.Accepted != 1 {
		t.Fatal(result, err)
	}
	result, err = s.Ingest(ctx, "logs", logBody(s.now()), Scope{"collector", "docker"}, resolveFixture)
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil || result.Accepted != 1 {
		t.Fatal(result, err)
	}
	page, err := s.Query(ctx, Query{Application: "verified-app"})
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil || len(page.Records) != 2 {
		t.Fatal(page, err)
	}
	// Fail immediately when the observed result violates this fixture contract.
	if page.Records[0].Signal != "logs" || page.Records[1].Value == nil || *page.Records[1].Value != 123 {
		t.Fatal(page)
	}
	verifyReopen(t, s)
}

// TestMalformedAndOwnershipAreAtomic prevents malformed or forged data entering history.
func TestMalformedAndOwnershipAreAtomic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scope := Scope{"c", "k3s"}
	samples := [][]byte{[]byte(`{}`), []byte(`null`), []byte(`{"resourceLogs":[]}`), []byte(strings.Repeat("x", MaxBodyBytes+1)), []byte(`{"resourceMetrics":{},"resourceLogs":[]}`)}
	// Exercise each independent fixture case with the same bounded-store contract.
	for _, sample := range samples {
		// Fail immediately when the observed result violates this fixture contract.
		if _, err := s.Ingest(ctx, "metrics", sample, scope, resolveFixture); !errors.Is(err, ErrInvalid) {
			t.Fatal("expected invalid", err)
		}
	}
	// Keep this fixture callback isolated from production authority and clocks.
	deny := func(Resource) (Identity, error) { return Identity{}, errors.New("ownership denied") }
	// Fail immediately when the observed result violates this fixture contract.
	if result, err := s.Ingest(ctx, "metrics", metricBody(s.now()), scope, deny); err != nil || result.Rejected != 1 || result.Accepted != 0 {
		t.Fatal("unexpected ownership result", result, err)
	}
	page, err := s.Query(ctx, Query{})
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil || len(page.Records) != 0 {
		t.Fatal(page, err)
	}
}

// TestPartialSuccessReportsUnsupportedAndStale counts omitted observations honestly.
func TestPartialSuccessReportsUnsupportedAndStale(t *testing.T) {
	s := testStore(t)
	scope := Scope{"c", "k3s"}
	unsupported := []byte(`{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"latency","histogram":{"dataPoints":[{"count":"1"}]}}]}]}]}`)
	// Exercise each independent fixture case with the same bounded-store contract.
	for _, sample := range [][]byte{unsupported, metricBody(s.now().Add(-31 * 24 * time.Hour)), metricBody(s.now().Add(time.Hour))} {
		result, err := s.Ingest(context.Background(), "metrics", sample, scope, resolveFixture)
		// Fail immediately when the observed result violates this fixture contract.
		if err != nil || result.Accepted != 0 || result.Rejected != 1 || result.Message == "" {
			t.Fatal(result, err)
		}
	}
}

// TestQuotaPaginationAndExpiry proves retained history and read allocation stay bounded.
func TestQuotaPaginationAndExpiry(t *testing.T) {
	s := testStore(t)
	s.maxRecords = 3
	ctx := context.Background()
	// Exercise each independent fixture case with the same bounded-store contract.
	for i := 0; i < 5; i++ {
		// Fail immediately when the observed result violates this fixture contract.
		if _, err := s.Ingest(ctx, "logs", logBody(s.now()), Scope{"c", "docker"}, resolveFixture); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Query(ctx, Query{Limit: 2})
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil || len(page.Records) != 2 || page.NextBefore == 0 {
		t.Fatal(page, err)
	}
	older, err := s.Query(ctx, Query{Limit: 2, Before: page.NextBefore})
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil || len(older.Records) != 1 || older.Records[0].ID >= page.NextBefore {
		t.Fatal(older, err)
	}
	verifyExpiry(t, s)
}

// TestByteQuotaAndWriterLock prevents storage growth and concurrent writer ownership.
func TestByteQuotaAndWriterLock(t *testing.T) {
	s := testStore(t)
	s.maxBytes = 200
	// Fail immediately when the observed result violates this fixture contract.
	if other, err := Open(filepath.Dir(s.lock.Name())); err == nil {
		other.Close()
		t.Fatal("accepted duplicate writer")
	}
	// Fail immediately when the observed result violates this fixture contract.
	if _, err := s.Ingest(context.Background(), "logs", logBody(s.now()), Scope{"c", "docker"}, resolveFixture); err != nil {
		t.Fatal(err)
	}
	page, err := s.Query(context.Background(), Query{})
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil || len(page.Records) != 0 {
		t.Fatal(page, err)
	}
}

// TestSymlinkRejection refuses redirected telemetry roots and database paths.
func TestSymlinkRejection(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	// Fail immediately when the observed result violates this fixture contract.
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	// Fail immediately when the observed result violates this fixture contract.
	if s, err := Open(filepath.Join(root, "link")); err == nil {
		s.Close()
		t.Fatal("accepted directory symlink")
	}
	file := filepath.Join(target, "foreign")
	_ = os.WriteFile(file, []byte("private"), 0600)
	dir := filepath.Join(root, "safe")
	_ = os.Mkdir(dir, 0700)
	_ = os.Symlink(file, filepath.Join(dir, "history.sqlite"))
	// Fail immediately when the observed result violates this fixture contract.
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatal("accepted DB symlink")
	}
}

// TestAnyValueBounds rejects malformed attribute types and deeply nested bodies.
func TestAnyValueBounds(t *testing.T) {
	// Exercise each independent fixture case with the same bounded-store contract.
	for _, raw := range []string{`{"intValue":"nan"}`, `{"doubleValue":"NaN"}`, `{"stringValue":"x","boolValue":true}`, `{"unknown":1}`} {
		// Fail immediately when the observed result violates this fixture contract.
		if !errors.Is(anyValue(json.RawMessage(raw), 0), ErrInvalid) {
			t.Fatal(raw)
		}
	}
	// Fail immediately when the observed result violates this fixture contract.
	if !errors.Is(anyValue(json.RawMessage(`{"stringValue":"x"}`), 5), ErrInvalid) {
		t.Fatal("accepted depth")
	}
}

// verifyReopen confirms private persisted history survives collector restarts.
func verifyReopen(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	var err error
	dir := filepath.Dir(s.lock.Name())
	clock := s.now
	// Fail immediately when the observed result violates this fixture contract.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = clock
	page, err := reopened.Query(ctx, Query{Signal: "metrics"})
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil || len(page.Records) != 1 || page.Records[0].Identity.Application != "verified-app" {
		t.Fatal(page, err)
	}
	info, err := os.Stat(filepath.Join(dir, "history.sqlite"))
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}

// verifyExpiry hides aged history without mutating on a read request.
func verifyExpiry(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	original := s.now()
	// Keep this fixture callback isolated from production authority and clocks.
	s.now = func() time.Time { return original.Add(31 * 24 * time.Hour) }
	page, err := s.Query(ctx, Query{})
	// Fail immediately when the observed result violates this fixture contract.
	if err != nil || len(page.Records) != 0 {
		t.Fatal(page, err)
	}
	// Fail immediately when the observed result violates this fixture contract.
	if _, err = s.Query(ctx, Query{Limit: MaxQueryRecords + 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

// TestQueryByteBudget preserves resumable pages while bounding decoded response memory.
func TestQueryByteBudget(t *testing.T) {
	s := testStore(t)
	records := []Record{}
	// Fill enough valid rows to exhaust the byte quota before the row quota.
	for i := 0; i < 100; i++ {
		records = append(records, Record{Signal: "logs", Identity: Identity{Application: "app"}, ReceivedAt: s.now(), Body: json.RawMessage(`{"stringValue":"` + strings.Repeat("x", 30000) + `"}`)})
	}
	// Persist the batch through the same atomic quota path used by ingress.
	if err := s.append(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	page, err := s.Query(context.Background(), Query{Limit: 200})
	// The first page must stop before all rows and expose an exclusive continuation.
	if err != nil || len(page.Records) == 0 || len(page.Records) >= 100 || page.NextBefore == 0 {
		t.Fatal(len(page.Records), page.NextBefore, err)
	}
	next, err := s.Query(context.Background(), Query{Limit: 200, Before: page.NextBefore})
	// Continuation must preserve all remaining records without duplication.
	if err != nil || len(page.Records)+len(next.Records) != 100 {
		t.Fatal(len(next.Records), err)
	}
	records[0].Body = json.RawMessage(`{"stringValue":"` + strings.Repeat("x", MaxRecordBytes) + `"}`)
	// Oversized records fail atomically with a client-validation classification.
	if err = s.append(context.Background(), records[:1]); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

// TestHistoryRefusesHardLinks protects unrelated files from SQLite initialization.
func TestHistoryRefusesHardLinks(t *testing.T) {
	root := t.TempDir()
	unrelated := filepath.Join(root, "unrelated")
	// Seed recognizable bytes to prove failed initialization never changes the target.
	if err := os.WriteFile(unrelated, []byte("preserve-me"), 0600); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(root, "history")
	// Give the candidate store its own private directory.
	if err := os.Mkdir(history, 0700); err != nil {
		t.Fatal(err)
	}
	// A hard link must be rejected just like a symbolic redirect.
	if err := os.Link(unrelated, filepath.Join(history, "history.sqlite")); err != nil {
		t.Fatal(err)
	}
	// A failed open must never leave a live database handle.
	if s, err := Open(history); err == nil {
		s.Close()
		t.Fatal("accepted linked database")
	}
	body, err := os.ReadFile(unrelated)
	// Preserve the unrelated file byte for byte.
	if err != nil || string(body) != "preserve-me" {
		t.Fatal(string(body), err)
	}
}
