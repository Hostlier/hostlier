package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// existingState prevents replacing keys for either supported persistence format.
func existingState(dir string) bool {
	// Either durable format binds the directory to its original key.
	for _, name := range []string{"state.json", "state.db"} {
		// Validate the precondition before accepting protected state.
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// openDatabase validates key ownership and migrates one complete snapshot atomically.
// Schema version 1 stores the controller aggregate as JSON in one row;
// each Save also commits query indexes and its event cursor in the same transaction.
// Runtime actions remain outside SQLite transactions.
func (s *Store) openDatabase(state any) error {
	path := filepath.Join(s.dir, "state.db")
	// Refuse database links before SQLite can follow them.
	if err := regularOrMissing(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	s.db = db
	db.SetMaxOpenConns(1)
	// FULL synchronization makes a returned commit durable across process crashes.
	if _, err = db.Exec("PRAGMA synchronous=FULL; PRAGMA journal_mode=DELETE; CREATE TABLE IF NOT EXISTS controller_snapshot (id INTEGER PRIMARY KEY CHECK(id=1), payload BLOB NOT NULL, key_check TEXT NOT NULL, schema_version INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS activity_history(sequence INTEGER PRIMARY KEY,stamp INTEGER NOT NULL,application TEXT NOT NULL,operation TEXT NOT NULL,level TEXT NOT NULL,message TEXT NOT NULL,payload BLOB NOT NULL); CREATE INDEX IF NOT EXISTS activity_time ON activity_history(stamp,sequence); CREATE INDEX IF NOT EXISTS activity_app_time ON activity_history(application,stamp,sequence)"); err != nil {
		return err
	}
	// Propagate failures before publishing recovery state.
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	var payload []byte
	var check string
	var version int
	err = db.QueryRow("SELECT payload, key_check,schema_version FROM controller_snapshot WHERE id=1").Scan(&payload, &check, &version)
	// An absent row is an uninitialized database, including a failed previous migration.
	if errors.Is(err, sql.ErrNoRows) {
		return s.initializeSnapshot(state)
	}
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	// Validate the precondition before accepting protected state.
	// A future schema requires an explicit compatible upgrade rather than silent adoption.
	if version != 1 {
		return fmt.Errorf("unsupported controller SQLite schema version %d", version)
	}
	// Validate the precondition before accepting protected state.
	if check != s.Authenticate("controller-key-check-v1") {
		return errors.New("incorrect secret encryption key")
	}
	if err := json.Unmarshal(payload, state); err != nil {
		return err
	}
	return s.ensureCollectionIndex(payload)
}

// initializeSnapshot verifies the legacy document before committing its migration.
func (s *Store) initializeSnapshot(state any) error {
	// Legacy migration must not follow redirected controller documents.
	if err := regularOrMissing(filepath.Join(s.dir, "state.json")); err != nil {
		return err
	}
	payload, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	// A new installation starts with the caller's empty state.
	if errors.Is(err, os.ErrNotExist) {
		payload, err = json.Marshal(state)
	}
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	// Propagate failures before publishing recovery state.
	if err = json.Unmarshal(payload, state); err != nil {
		return err
	}
	// Authenticate every legacy encrypted value before recording migration success.
	if err = s.validateLegacySecrets(payload); err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO controller_snapshot(id,payload,key_check,schema_version) VALUES(1,?,?,1)", payload, s.Authenticate("controller-key-check-v1"))
	// Persist the first SQLite directory entry only after its migration transaction commits.
	if err != nil {
		return err
	}
	if err = s.ensureCollectionIndex(payload); err != nil {
		return err
	}
	return syncDirectory(s.dir)
}

// validateLegacySecrets checks encrypted secret fields using the original key.
func (s *Store) validateLegacySecrets(payload []byte) error {
	var document map[string]json.RawMessage
	// Propagate failures before publishing recovery state.
	if err := json.Unmarshal(payload, &document); err != nil {
		return err
	}
	// Validate each private payload collection using its documented legacy shape.
	for _, key := range []string{"resourceCredentials", "workflowData"} {
		var values map[string]string
		// Validate the precondition before accepting protected state.
		if raw := document[key]; len(raw) > 0 {
			// Propagate failures before publishing recovery state.
			if err := json.Unmarshal(raw, &values); err != nil {
				return err
			}
		}
		// Propagate failures before publishing recovery state.
		if err := s.validateCiphertexts(values); err != nil {
			return err
		}
	}
	var secrets map[string]struct {
		Revisions map[string]string `json:"revisions"`
	}
	// Validate the precondition before accepting protected state.
	if raw := document["secrets"]; len(raw) > 0 {
		// Propagate failures before publishing recovery state.
		if err := json.Unmarshal(raw, &secrets); err != nil {
			return err
		}
	}
	// Process each protected record with the same validation rules.
	for _, secret := range secrets {
		// Propagate failures before publishing recovery state.
		if err := s.validateCiphertexts(secret.Revisions); err != nil {
			return err
		}
	}

	return s.validateLegacyWorkflowRecords(document["workflowRecords"])
}

// regularOrMissing refuses links and special files in protected persistence paths.
func regularOrMissing(path string) error {
	info, err := os.Lstat(path)
	// Propagate failures before publishing recovery state.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// Propagate failures before publishing recovery state.
	if err != nil {
		return err
	}
	// Validate the precondition before accepting protected state.
	if !info.Mode().IsRegular() {
		return fmt.Errorf("protected path must be a regular file: %s", path)
	}
	return nil
}

// validateCiphertexts authenticates a collection without exposing plaintext.
func (s *Store) validateCiphertexts(values map[string]string) error {
	// Process each protected record with the same validation rules.
	for _, value := range values {
		// Validate the precondition before accepting protected state.
		if _, err := s.Decrypt(value); err != nil {
			return fmt.Errorf("legacy secret authentication failed: %w", err)
		}
	}
	return nil
}

// Snapshot reads the committed authoritative controller document in a SQLite read transaction.
func (s *Store) Snapshot() ([]byte, error) {
	// Reject reads after ownership has been released.
	if s.db == nil {
		return nil, errors.New("controller store is closed")
	}
	var payload []byte
	err := s.db.QueryRow("SELECT payload FROM controller_snapshot WHERE id=1").Scan(&payload)
	return payload, err
}

// validateLegacyWorkflowRecords authenticates private persisted workflow result payloads.
func (s *Store) validateLegacyWorkflowRecords(raw json.RawMessage) error {
	// Older snapshots may not contain workflow records.
	if len(raw) == 0 {
		return nil
	}
	var records map[string]struct {
		Result string `json:"result"`
	}
	// Require the known record shape before recording migration completion.
	if err := json.Unmarshal(raw, &records); err != nil {
		return err
	}
	// Results are encrypted only after a step has produced a private output.
	for _, record := range records {
		// Validate the precondition before accepting protected state.
		if record.Result != "" {
			// Validate the precondition before accepting protected state.
			if _, err := s.Decrypt(record.Result); err != nil {
				return fmt.Errorf("legacy workflow result authentication failed: %w", err)
			}
		}
	}
	return nil
}
