package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
)

// Snapshot reads an existing committed snapshot without taking writer ownership,
// creating files, migrating schemas, or accessing plaintext secrets.
func Snapshot(dir string, state any) error {
	path := filepath.Join(dir, "state.db")
	// Preserve failure before advancing to the next external-effect boundary.
	if err := safeStateDirectory(dir); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := regularOrMissing(path); err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	defer db.Close()
	var payload []byte
	var version int
	// Preserve failure before advancing to the next external-effect boundary.
	if err = db.QueryRow("SELECT payload,schema_version FROM controller_snapshot WHERE id=1").Scan(&payload, &version); err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if version != 1 {
		return fmt.Errorf("unsupported controller SQLite schema version %d", version)
	}
	return json.Unmarshal(payload, state)
}
