package telemetry

import (
	"encoding/json"
)

// migrateObservedTime preserves original event timestamps in pre-existing history.
func (s *Store) migrateObservedTime() error {
	rows, err := s.db.Query("PRAGMA table_info(records)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var id, notnull, pk int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&id, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "observed" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		if _, err = s.db.Exec("ALTER TABLE records ADD COLUMN observed INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	rows, err = s.db.Query("SELECT id,payload FROM records WHERE observed=0")
	if err != nil {
		return err
	}
	type stamp struct{ id, observed int64 }
	var pending []stamp
	for rows.Next() {
		var id int64
		var payload []byte
		if err = rows.Scan(&id, &payload); err != nil {
			rows.Close()
			return err
		}
		var record Record
		if err = json.Unmarshal(payload, &record); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, stamp{id, record.ObservedAt.UnixNano()})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, row := range pending {
		if _, err = tx.Exec("UPDATE records SET observed=? WHERE id=?", row.observed, row.id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS records_time ON records(signal,observed,id); CREATE INDEX IF NOT EXISTS records_app_time ON records(application,signal,observed,id)"); err != nil {
		return err
	}
	return tx.Commit()
}
