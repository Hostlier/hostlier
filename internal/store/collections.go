package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Indexed collections are projections of the authoritative snapshot. The list is
// fixed here so callers cannot choose a JSON path or SQL identifier.
var indexedCollections = []string{
	"applications", "operations", "tunnels", "telemetryCollectors", "clients",
	"secrets", "domains", "backups", "authorizations", "hosts", "resources",
	"backupPolicies", "recoveryPoints", "recoveryJobs", "requests",
	"fusedConnections", "workflowRecords", "events", "planningJobs",
}

type CollectionItem struct {
	Key     string
	Payload json.RawMessage
}

type CollectionQuery struct {
	ExcludeStatus string
	Owner         string
	Operation     string
	Policy        string
	Destination   string
	Verified      bool
	NewestFirst   bool
	KeyPrefix     string
	Limit         int
	ActiveFirst   bool
	Tail          bool
}

func collectionSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS collection_items (
		collection TEXT NOT NULL, item_key TEXT NOT NULL, status TEXT NOT NULL,
		owner TEXT NOT NULL, operation TEXT NOT NULL, policy TEXT NOT NULL,
		destination TEXT NOT NULL, verified INTEGER NOT NULL, created_at INTEGER NOT NULL,
		payload BLOB NOT NULL,
		PRIMARY KEY (collection, item_key)
	);
	CREATE INDEX IF NOT EXISTS collection_status_key ON collection_items(collection,status,item_key);
	CREATE INDEX IF NOT EXISTS collection_owner_key ON collection_items(collection,owner,item_key);
	CREATE INDEX IF NOT EXISTS collection_operation_key ON collection_items(collection,operation,item_key);
	CREATE INDEX IF NOT EXISTS collection_recovery_order ON collection_items(collection,policy,destination,verified,created_at DESC);`)
	return err
}

// ensureCollectionIndex also backfills installations created before the index existed.
func (s *Store) ensureCollectionIndex(payload []byte) error {
	if err := collectionSchema(s.db); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := indexCollections(tx, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// indexCollections updates every collection in the same transaction as Save.
func indexCollections(tx *sql.Tx, payload []byte) error {
	if _, err := tx.Exec("DELETE FROM collection_items"); err != nil {
		return err
	}
	const insert = `INSERT INTO collection_items(collection,item_key,status,owner,operation,policy,destination,verified,created_at,payload)
		SELECT ?, CAST(key AS TEXT),
		CASE WHEN type='object' THEN COALESCE(json_extract(value,'$.status'),'') ELSE '' END,
		CASE WHEN type='object' THEN COALESCE(json_extract(value,'$.owner'),'') ELSE '' END,
		CASE WHEN type='object' THEN COALESCE(json_extract(value,'$.operationId'),'') ELSE '' END,
		CASE WHEN type='object' THEN COALESCE(json_extract(value,'$.policy'),'') ELSE '' END,
		CASE WHEN type='object' THEN COALESCE(json_extract(value,'$.destinationFingerprint'),'') ELSE '' END,
		CASE WHEN type='object' THEN COALESCE(json_extract(value,'$.verified'),0) ELSE 0 END,
		CASE WHEN type='object' THEN COALESCE(json_extract(value,'$.createdAt'),0) ELSE 0 END,
		CASE WHEN type='text' THEN json_quote(value) ELSE value END
		FROM json_each(?, ?) WHERE type <> 'null'`
	for _, collection := range indexedCollections {
		if _, err := tx.Exec(insert, collection, payload, "$."+collection); err != nil {
			return fmt.Errorf("index %s: %w", collection, err)
		}
	}
	return nil
}

// Collection applies filters, ordering, and bounds in SQLite before fetching rows.
func (s *Store) Collection(ctx context.Context, collection string, q CollectionQuery) ([]CollectionItem, error) {
	if s.db == nil {
		return nil, errors.New("controller store is closed")
	}
	allowed := false
	for _, name := range indexedCollections {
		if name == collection {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, errors.New("unknown collection")
	}
	clauses := []string{"collection = ?"}
	args := []any{collection}
	if q.ExcludeStatus != "" {
		clauses = append(clauses, "status <> ?")
		args = append(args, q.ExcludeStatus)
	}
	if q.Owner != "" {
		clauses = append(clauses, "owner = ?")
		args = append(args, q.Owner)
	}
	if q.Operation != "" {
		clauses = append(clauses, "operation = ?")
		args = append(args, q.Operation)
	}
	if q.Policy != "" {
		clauses = append(clauses, "policy = ?")
		args = append(args, q.Policy)
	}
	if q.Destination != "" {
		clauses = append(clauses, "destination = ?")
		args = append(args, q.Destination)
	}
	if q.Verified {
		clauses = append(clauses, "verified = 1")
	}
	if q.KeyPrefix != "" {
		// The prefix is data, never interpolated into SQL or a LIKE pattern.
		clauses = append(clauses, "substr(item_key,1,length(?)) = ?")
		args = append(args, q.KeyPrefix, q.KeyPrefix)
	}
	order := "item_key ASC"
	if collection == "domains" || collection == "events" {
		order = "CAST(item_key AS INTEGER) ASC"
	}
	if q.ActiveFirst {
		order = "CASE WHEN status IN ('queued','running','interrupted') THEN 0 ELSE 1 END, item_key ASC"
	}
	if q.NewestFirst {
		order = "created_at DESC, item_key ASC"
	}
	if q.Tail {
		// Select the newest bounded events, then emit them in chronological order.
		order = "CAST(item_key AS INTEGER) DESC"
	}
	query := "SELECT item_key,payload FROM collection_items WHERE " + strings.Join(clauses, " AND ") + " ORDER BY " + order
	if q.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, q.Limit)
	}
	if q.Tail {
		query = "SELECT item_key,payload FROM (" + query + ") ORDER BY CAST(item_key AS INTEGER) ASC"
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CollectionItem{}
	for rows.Next() {
		var item CollectionItem
		var payload string
		if err := rows.Scan(&item.Key, &payload); err != nil {
			return nil, err
		}
		item.Payload = json.RawMessage(payload)
		items = append(items, item)
	}
	return items, rows.Err()
}
