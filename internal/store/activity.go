package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"hostlier/internal/model"
)

const activityMaxRecords = 100000
const activityMaxBytes = 64 << 20

// Activity is public controller event metadata, never request bodies or credentials.
type Activity struct {
	Sequence    int64  `json:"sequence"`
	Timestamp   string `json:"timestamp"`
	Level       string `json:"level"`
	Message     string `json:"message"`
	OperationID string `json:"operationId,omitempty"`
	Application string `json:"application,omitempty"`
	Actor       string `json:"actor,omitempty"`
}
type ActivityPage struct {
	Entries        []Activity `json:"entries"`
	NextBefore     int64      `json:"nextBefore,omitempty"`
	NextBeforeTime string     `json:"nextBeforeTime,omitempty"`
	RetentionDays  int        `json:"retentionDays"`
	RetainedFrom   string     `json:"retainedFrom,omitempty"`
}

// archiveActivity participates in the snapshot transaction so failed commits expose neither.
func archiveActivity(tx *sql.Tx, data []byte) error {
	var snapshot struct {
		Events     []Activity `json:"events"`
		Operations map[string]struct {
			Name  string `json:"name"`
			Kind  string `json:"kind"`
			Actor string `json:"actor"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	var latest int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(sequence),0) FROM activity_history").Scan(&latest); err != nil {
		return err
	}
	cutoff := time.Now().Add(-model.HistoryRetention).UnixNano()
	added := false
	for _, entry := range snapshot.Events {
		if entry.Sequence <= latest {
			continue
		}
		stamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
		if err != nil {
			continue
		}
		if stamp.UnixNano() < cutoff {
			continue
		}
		if op, ok := snapshot.Operations[entry.OperationID]; ok {
			if entry.Actor == "" {
				entry.Actor = op.Actor
			}
			if entry.Application == "" && (op.Kind == "deploy" || op.Kind == "action") {
				entry.Application = op.Name
			}
		}
		payload, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO activity_history(sequence,stamp,application,operation,level,message,payload) VALUES(?,?,?,?,?,?,?)", entry.Sequence, stamp.UnixNano(), entry.Application, entry.OperationID, entry.Level, entry.Message, payload); err != nil {
			return err
		}
		added = true
	}
	if !added {
		return nil
	}
	if _, err := tx.Exec("DELETE FROM activity_history WHERE stamp < ?", cutoff); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM activity_history WHERE sequence NOT IN (SELECT sequence FROM activity_history ORDER BY sequence DESC LIMIT ?)", activityMaxRecords); err != nil {
		return err
	}
	var size int
	if err := tx.QueryRow("SELECT COALESCE(SUM(length(payload)),0) FROM activity_history").Scan(&size); err != nil {
		return err
	}
	for size > activityMaxBytes {
		if _, err := tx.Exec("DELETE FROM activity_history WHERE sequence IN (SELECT sequence FROM activity_history ORDER BY sequence LIMIT 256)"); err != nil {
			return err
		}
		if err := tx.QueryRow("SELECT COALESCE(SUM(length(payload)),0) FROM activity_history").Scan(&size); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Activity(ctx context.Context, f model.HistoryFilter, app, operation string) (ActivityPage, error) {
	page := ActivityPage{Entries: []Activity{}, RetentionDays: 30}
	clauses := []string{"stamp >= ?"}
	args := []any{time.Now().Add(-model.HistoryRetention).UnixNano()}
	if !f.From.IsZero() {
		clauses = append(clauses, "stamp >= ?")
		args = append(args, f.From.UnixNano())
	}
	if !f.To.IsZero() {
		clauses = append(clauses, "stamp < ?")
		args = append(args, f.To.UnixNano())
	}
	for column, value := range map[string]string{"application": app, "operation": operation, "level": f.Level} {
		if value != "" {
			clauses = append(clauses, column+" = ?")
			args = append(args, value)
		}
	}
	if f.Search != "" {
		clauses = append(clauses, "instr(lower(message),lower(?)) > 0")
		args = append(args, f.Search)
	}
	if f.Before > 0 {
		stamp := f.BeforeTime.UnixNano()
		if f.BeforeTime.IsZero() {
			if err := s.db.QueryRowContext(ctx, "SELECT stamp FROM activity_history WHERE sequence=?", f.Before).Scan(&stamp); err != nil {
				return page, err
			}
		}
		clauses = append(clauses, "(stamp < ? OR (stamp = ? AND sequence < ?))")
		args = append(args, stamp, stamp, f.Before)
	}
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, "SELECT MIN(stamp) FROM activity_history WHERE stamp >= ?", time.Now().Add(-model.HistoryRetention).UnixNano()).Scan(&oldest); err != nil {
		return page, err
	}
	if oldest.Valid {
		page.RetainedFrom = time.Unix(0, oldest.Int64).UTC().Format(time.RFC3339Nano)
	}
	args = append(args, f.Limit+1)
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM activity_history WHERE "+strings.Join(clauses, " AND ")+" ORDER BY stamp DESC,sequence DESC LIMIT ?", args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return page, err
		}
		if len(page.Entries) == f.Limit || (size+len(raw) > 2<<20 && len(page.Entries) > 0) {
			last := page.Entries[len(page.Entries)-1]
			page.NextBefore = last.Sequence
			page.NextBeforeTime = last.Timestamp
			break
		}
		var entry Activity
		if err = json.Unmarshal(raw, &entry); err != nil {
			return page, err
		}
		page.Entries = append(page.Entries, entry)
		size += len(raw)
	}
	return page, rows.Err()
}
