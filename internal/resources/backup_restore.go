package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"hostlier/internal/model"
)

// redisConfig renders an authenticated Redis configuration with the requested AOF mode.
func redisConfig(c model.ResourceCredentials, aof bool) string {
	enabled := "no"
	// Enable append-only durability only when the requested mode requires it.
	if aof {
		enabled = "yes"
	}
	return fmt.Sprintf("bind 0.0.0.0\nprotected-mode yes\nport 6379\ndir /data\nappendonly %s\nappendfsync everysec\nuser default off\nuser %s on >%s ~* +@all\n", enabled, c.Username, c.Password)
}

// restoreEngine routes a restore to the selected engine.
func (m *Manager) restoreEngine(ctx context.Context, item record, id string, running bool, data []byte, c model.ResourceCredentials) (err error) {
	// Select the native behavior for the requested engine or lifecycle operation.
	switch item.Type {
	// Use PostgreSQL native commands and its authenticated database configuration.
	case "postgres":
		file := "hostlier-restore.dump"
		// Upload the native PostgreSQL dump before invoking pg_restore.
		if err = m.upload(ctx, id, "/tmp", file, data, 0); err != nil {
			return err
		}
		defer m.execute(context.Background(), item.Name, []string{"rm", "-f", "/tmp/" + file}, nil)
		_, err = m.execute(ctx, item.Name, []string{"pg_restore", "-h", "127.0.0.1", "-U", c.Username, "-d", c.Database, "--clean", "--if-exists", "--no-owner", "--no-acl", "--exit-on-error", "/tmp/" + file}, authEnv(item.Type, c))
	// Stop engines that require offline replacement of their data files.
	case "redis", "neo4j":
		return m.restoreOffline(ctx, item, id, running, data, c)
	// Reject unsupported values rather than guessing an engine behavior.
	default:
		return model.Err("RESTORE_UNSUPPORTED", "Unsupported restore engine")
	}
	return err
}

// restoreOffline replaces data while the selected engine is stopped.
func (m *Manager) restoreOffline(ctx context.Context, item record, id string, running bool, data []byte, c model.ResourceCredentials) (err error) {
	// Stop a running engine before replacing its data files.
	if running {
		// Require the owned Docker API request to complete before using its result.
		if _, err = m.api(ctx, "POST", "/containers/"+id+"/stop?t=30", nil, nil); err != nil {
			return err
		}
	}
	// Load the Neo4j dump through an offline helper mounted to this resource volume.
	if item.Type == "neo4j" {
		err = m.restoreNeo4j(ctx, item, data)
	} else {
		_, err = m.helper(ctx, item, []string{"/bin/sh", "-c"}, []string{"rm -rf /data/appendonlydir /data/dump.rdb && cp /tmp/restore.rdb /data/dump.rdb && chown 999:999 /data/dump.rdb"}, data, "restore.rdb", 0, "")
		// Temporarily disable AOF only after the replacement RDB file is installed.
		if err == nil {
			err = m.upload(ctx, id, "/tmp", "hostlier-redis.conf", []byte(redisConfig(c, false)), 999)
		}
	}
	// Return native load or Redis configuration failures before restarting the engine.
	if err != nil {
		return err
	}
	// Require the owned Docker API request to complete before using its result.
	if _, err = m.api(ctx, "POST", "/containers/"+id+"/start", nil, nil); err != nil {
		return err
	}
	// Require authenticated readiness before reporting the lifecycle operation complete.
	if err = m.ready(ctx, item.Spec, c); err != nil {
		return err
	}
	// Re-enable and persist Redis AOF after replacing its RDB data.
	if item.Type == "redis" {
		return m.restoreRedisPersistence(ctx, item, id, c)
	}
	return nil
}

// restoreRedisPersistence enables AOF and waits for the rewritten durable snapshot.
func (m *Manager) restoreRedisPersistence(ctx context.Context, item record, id string, c model.ResourceCredentials) (err error) {
	output, runErr := m.execute(ctx, item.Name, []string{"redis-cli", "--user", c.Username, "CONFIG", "SET", "appendonly", "yes"}, authEnv(item.Type, c))
	// Reject this request: Cannot enable Redis AOF after restore.
	if runErr != nil || strings.TrimSpace(string(output)) != "OK" {
		return model.Err("RESTORE_PERSISTENCE", "Cannot enable Redis AOF after restore")
	}
	deadline := time.Now().Add(m.options.HealthTimeout)
	// Poll Redis persistence until the rewrite completes or the operation deadline expires.
	for {
		output, runErr = m.execute(ctx, item.Name, []string{"redis-cli", "--user", c.Username, "INFO", "persistence"}, authEnv(item.Type, c))
		// Return native command failures instead of accepting incomplete engine output.
		if runErr != nil {
			return runErr
		}
		text := string(output)
		// Finish polling only after Redis reports a completed and successful AOF rewrite.
		if strings.Contains(text, "aof_rewrite_in_progress:0") && strings.Contains(text, "aof_last_bgrewrite_status:ok") {
			break
		}
		// Reject this request: Redis AOF rewrite did not complete.
		if time.Now().After(deadline) {
			return model.Err("RESTORE_PERSISTENCE", "Redis AOF rewrite did not complete")
		}
		// Stop polling immediately when the operation context is cancelled.
		if err = wait(ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
	// Persist the AOF-enabled Redis configuration after its background rewrite completes.
	if err = m.upload(ctx, id, "/tmp", "hostlier-redis.conf", []byte(redisConfig(c, true)), 999); err != nil {
		return err
	}
	return nil
}

// restoreNeo4j loads the native archive and explicitly migrates the pinned calendar-version target.
func (m *Manager) restoreNeo4j(ctx context.Context, item record, data []byte) (err error) {
	_, err = m.helper(ctx, item, []string{"neo4j-admin"}, []string{"database", "load", "neo4j", "--from-path=/tmp", "--overwrite-destination=true"}, data, "neo4j.dump", 7474, "")

	// Propagate failure before performing the next recovery side effect.
	if err == nil && model.ApprovedDatabaseImage("neo4j", item.Spec.Image) {
		_, err = m.helper(ctx, item, []string{"neo4j-admin"}, []string{"database", "migrate", "neo4j"}, nil, "", 7474, "")
	}
	return err
}
