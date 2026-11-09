package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// Store owns an independently locked, private, quota-limited telemetry database.
type Store struct {
	mu                   sync.Mutex
	db                   *sql.DB
	lock                 *os.File
	now                  func() time.Time
	maxRecords, maxBytes int
}

// Open creates or recovers sensitive telemetry history, separate from controller state.
func Open(dir string) (*Store, error) {
	dir, err := filepath.Abs(dir)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return nil, err
	}
	// Stop on failed validation or I/O before exposing a successful observation.
	if err = privateDirectory(dir); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, "history.lock")
	// Refuse an existing lock linked to another file or owned by another user.
	if err = privateFile(lockPath); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return nil, err
	}
	// Stop on failed validation or I/O before exposing a successful observation.
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, err
	}
	s := &Store{lock: lock, now: time.Now, maxRecords: MaxHistoryRecords, maxBytes: MaxHistoryBytes}
	// Stop on failed validation or I/O before exposing a successful observation.
	if err = s.openDB(dir); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// privateDirectory refuses redirected paths and restricts telemetry to its owner.
func privateDirectory(dir string) error {
	current := string(filepath.Separator)
	// Validate each bounded entry before carrying it into the next stage.
	for _, part := range strings.Split(strings.TrimPrefix(dir, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		// Stop on failed validation or I/O before exposing a successful observation.
		if current == "/var" || current == "/tmp" {
			resolved, err := filepath.EvalSymlinks(current)
			// Stop on failed validation or I/O before exposing a successful observation.
			if err != nil {
				return err
			}
			current = resolved
		}
		info, err := os.Lstat(current)
		// Stop on failed validation or I/O before exposing a successful observation.
		if os.IsNotExist(err) {
			// Skip absent values without inventing observations.
			if err = os.Mkdir(current, 0700); err != nil {
				return err
			}
			continue
		}
		// Stop on failed validation or I/O before exposing a successful observation.
		if err != nil {
			return err
		}
		// Stop on failed validation or I/O before exposing a successful observation.
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("telemetry path must contain only directories")
		}
	}
	return restrictDirectory(dir)
}

// openDB verifies database sidecars before enabling bounded SQLite storage.
func (s *Store) openDB(dir string) error {
	path := filepath.Join(dir, "history.sqlite")
	// Validate each bounded entry before carrying it into the next stage.
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		// Existing database files must belong exclusively to this process owner.
		if err := privateFile(path + suffix); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return err
	}
	// Stop on failed validation or I/O before exposing a successful observation.
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	file.Close()
	s.db, err = sql.Open("sqlite", path)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return err
	}
	s.db.SetMaxOpenConns(1)
	_, err = s.db.Exec(`PRAGMA journal_mode=DELETE; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=1000; PRAGMA max_page_count=65536;
 CREATE TABLE IF NOT EXISTS records(id INTEGER PRIMARY KEY AUTOINCREMENT, signal TEXT NOT NULL, application TEXT NOT NULL, revision TEXT NOT NULL, component TEXT NOT NULL, received INTEGER NOT NULL, observed INTEGER NOT NULL DEFAULT 0, payload BLOB NOT NULL);
 CREATE INDEX IF NOT EXISTS records_workload ON records(application,signal,id);`)
	if err != nil {
		return err
	}
	return s.migrateObservedTime()
}

// append commits accepted samples together, evicting expired/old records inside the transaction.
func (s *Store) append(ctx context.Context, records []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Stop on failed validation or I/O before exposing a successful observation.
	if s.db == nil {
		return errors.New("telemetry store closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Validate each bounded entry before carrying it into the next stage.
	for _, r := range records {
		payload, err := json.Marshal(r)
		// Stop on failed validation or I/O before exposing a successful observation.
		if err != nil {
			return err
		}
		// Reject record storage size before accepting untrusted telemetry.
		if len(payload) > MaxRecordBytes {
			return invalid("record storage size")
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO records(signal,application,revision,component,received,observed,payload) VALUES(?,?,?,?,?,?,?)", r.Signal, r.Identity.Application, r.Identity.Revision, r.Identity.Component, r.ReceivedAt.UnixNano(), r.ObservedAt.UnixNano(), payload)
		// Stop on failed validation or I/O before exposing a successful observation.
		if err != nil {
			return err
		}
	}
	// Stop on failed validation or I/O before exposing a successful observation.
	if err = s.trim(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// trim enforces age, row and logical-byte quotas without touching deployment state.
func (s *Store) trim(ctx context.Context, tx *sql.Tx) error {
	// Stop on failed validation or I/O before exposing a successful observation.
	if _, err := tx.ExecContext(ctx, "DELETE FROM records WHERE received < ?", s.now().Add(-Retention).UnixNano()); err != nil {
		return err
	}
	// Stop on failed validation or I/O before exposing a successful observation.
	if _, err := tx.ExecContext(ctx, "DELETE FROM records WHERE id NOT IN (SELECT id FROM records ORDER BY id DESC LIMIT ?)", s.maxRecords); err != nil {
		return err
	}
	var bytes int
	// Stop on failed validation or I/O before exposing a successful observation.
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(length(payload)),0) FROM records").Scan(&bytes); err != nil {
		return err
	}
	// Evict oldest batches until retained payload bytes fit the quota.
	for bytes > s.maxBytes {
		// Stop on failed validation or I/O before exposing a successful observation.
		if _, err := tx.ExecContext(ctx, "DELETE FROM records WHERE id IN (SELECT id FROM records ORDER BY id LIMIT 64)"); err != nil {
			return err
		}
		// Stop on failed validation or I/O before exposing a successful observation.
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(length(payload)),0) FROM records").Scan(&bytes); err != nil {
			return err
		}
	}
	return nil
}

// Query returns bounded sensitive history without writing or changing telemetry authority.
func (s *Store) Query(ctx context.Context, q Query) (Page, error) {
	// Apply the bounded special case before continuing record processing.
	if q.Limit == 0 {
		q.Limit = 100
	}
	// Stop on failed validation or I/O before exposing a successful observation.
	if err := validQuery(q); err != nil {
		return Page{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Stop on failed validation or I/O before exposing a successful observation.
	if s.db == nil {
		return Page{}, errors.New("telemetry store closed")
	}
	clauses := []string{"received >= ?"}
	args := []any{s.now().Add(-Retention).UnixNano()}
	// Validate each bounded entry before carrying it into the next stage.
	for column, value := range map[string]string{"signal": q.Signal, "application": q.Application, "revision": q.Revision, "component": q.Component} {
		// Reject query identity before accepting untrusted telemetry.
		if len(value) > 256 {
			return Page{}, invalid("query identity")
		}
		// Apply the bounded special case before continuing record processing.
		if value != "" {
			clauses = append(clauses, column+" = ?")
			args = append(args, value)
		}
	}
	if !q.From.IsZero() {
		clauses = append(clauses, "observed >= ?")
		args = append(args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "observed < ?")
		args = append(args, q.To.UnixNano())
	}
	if q.Search != "" {
		clauses = append(clauses, "instr(lower(CAST(json_extract(payload,'$.body') AS TEXT)),lower(?)) > 0")
		args = append(args, q.Search)
	}
	if q.Level != "" {
		clauses = append(clauses, `lower(json_extract(payload,'$.attributes."otel.severity_text".stringValue')) IN (?,?)`)
		alias := q.Level
		if alias == "warn" {
			alias = "warning"
		}
		args = append(args, q.Level, alias)
	}
	if q.Before > 0 {
		stamp := q.BeforeTime.UnixNano()
		if q.BeforeTime.IsZero() {
			if err := s.db.QueryRowContext(ctx, "SELECT observed FROM records WHERE id=?", q.Before).Scan(&stamp); err != nil {
				return Page{Records: []Record{}, RetentionDays: 30}, err
			}
		}
		clauses = append(clauses, "(observed < ? OR (observed = ? AND id < ?))")
		args = append(args, stamp, stamp, q.Before)
	}
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, "SELECT MIN(observed) FROM records WHERE received >= ?", s.now().Add(-Retention).UnixNano()).Scan(&oldest); err != nil {
		return Page{}, err
	}
	args = append(args, q.Limit+1)
	rows, err := s.db.QueryContext(ctx, "SELECT id,payload FROM records WHERE "+strings.Join(clauses, " AND ")+" ORDER BY observed DESC,id DESC LIMIT ?", args...)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page, err := readPage(rows, q.Limit)
	page.RetentionDays = 30
	if oldest.Valid {
		page.RetainedFrom = time.Unix(0, oldest.Int64).UTC().Format(time.RFC3339Nano)
	}
	return page, err
}

// readPage decodes only the bounded selected records and exposes an exclusive cursor.
func readPage(rows *sql.Rows, limit int) (Page, error) {
	page := Page{Records: []Record{}}
	payloadBytes := 0
	// Read only the bounded page selected by the history query.
	for rows.Next() {
		var id int64
		var payload []byte
		// Stop on failed validation or I/O before exposing a successful observation.
		if err := rows.Scan(&id, &payload); err != nil {
			return Page{}, err
		}
		// Reject corrupt or externally modified rows before allocating decoded payloads.
		if len(payload) > MaxRecordBytes {
			return Page{}, errors.New("corrupt telemetry record size")
		}
		// Continue on the next page when either output quota is exhausted.
		if len(page.Records) == limit || payloadBytes+len(payload) > MaxQueryBytes {
			page.NextBefore = page.Records[len(page.Records)-1].ID
			page.NextBeforeTime = page.Records[len(page.Records)-1].ObservedAt.UTC().Format(time.RFC3339Nano)
			break
		}
		var r Record
		// Stop on failed validation or I/O before exposing a successful observation.
		if err := json.Unmarshal(payload, &r); err != nil {
			return Page{}, fmt.Errorf("corrupt telemetry record")
		}
		r.ID = id
		payloadBytes += len(payload)
		page.Records = append(page.Records, r)
	}
	return page, rows.Err()
}

// Close releases database and writer ownership, safely tolerating repeated calls.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	// Apply the bounded special case before continuing record processing.
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	// Apply the bounded special case before continuing record processing.
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		e := s.lock.Close()
		s.lock = nil
		// Apply the bounded special case before continuing record processing.
		if err == nil {
			err = e
		}
	}
	return err
}

// validQuery prevents arbitrary signal names and unbounded page allocations.
func validQuery(q Query) error {
	if len(q.Search) > 256 || (!q.From.IsZero() && !q.To.IsZero() && (!q.From.Before(q.To) || q.To.Sub(q.From) > Retention)) {
		return invalid("history filters")
	}
	// Reject query bounds before accepting untrusted telemetry.
	if q.Limit < 1 || q.Limit > MaxQueryRecords || q.Before < 0 {
		return invalid("query bounds")
	}
	// Reject query signal before accepting untrusted telemetry.
	if q.Signal != "" && q.Signal != "logs" && q.Signal != "metrics" {
		return invalid("query signal")
	}
	return nil
}

// privateFile refuses redirected, shared or foreign files before SQLite can modify them.
func privateFile(path string) error {
	info, err := os.Lstat(path)
	// New files are created later with owner-only permissions and O_NOFOLLOW.
	if os.IsNotExist(err) {
		return nil
	}
	// Preserve operating-system errors for retryable controller responses.
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	// Hard links would let database initialization modify an unrelated file.
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("telemetry file must be regular, private and owner controlled")
	}
	return os.Chmod(path, 0600)
}

// restrictDirectory sets permissions only on a directory owned by the current user.
func restrictDirectory(dir string) error {
	info, err := os.Stat(dir)
	// Do not change permissions on a directory owned by a different account.
	if err != nil {
		return err
	}
	// The private leaf is the authority boundary for subsequent SQLite path operations.
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("telemetry directory owner mismatch")
	}
	return os.Chmod(dir, 0700)
}
