package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"hostlier/internal/model"
)

// engineExec runs a script in the engine deployment using protected stdin.
func (m *K3sManager) engineExec(ctx context.Context, item record, script string, input []byte) ([]byte, error) {
	return m.run(ctx, []string{"exec", "-i", "deployment/engine", "-n", item.Network, "--", "sh", "-c", script}, input)
}

// maintenance runs offline maintenance with a temporary pod and restores prior running state.
func (m *K3sManager) maintenance(ctx context.Context, item record, c model.ResourceCredentials, operation func(string) ([]byte, error)) (data []byte, err error) {
	observed, e := m.Status(ctx, item.Name)
	// Observe the original resource state before taking it offline.
	if e != nil {
		return nil, e
	}
	wasRunning := observed.Status == "running"
	// Require the explicitly scoped kubectl operation to complete successfully.
	if _, e = m.run(ctx, []string{"scale", "deployment/engine", "-n", item.Network, "--replicas=0"}, nil); e != nil {
		return nil, e
	}
	// Restart and health-check the engine only if it was running before maintenance.
	defer func() {
		// Preserve the engine running state across offline maintenance.
		if wasRunning {
			cleanup, cancel := context.WithTimeout(context.Background(), m.options.HealthTimeout)
			defer cancel()
			_, restartErr := m.run(cleanup, []string{"scale", "deployment/engine", "-n", item.Network, "--replicas=1"}, nil)
			// Verify authenticated health only after the engine restart succeeds.
			if restartErr == nil {
				restartErr = m.ready(cleanup, item, c)
			}
			// Surface restart failure only when it will not mask the original maintenance error.
			if err == nil {
				err = restartErr
			}
		}
	}()
	// Require the explicitly scoped kubectl operation to complete successfully.
	if _, e = m.run(ctx, []string{"wait", "--for=delete", "pods", "-n", item.Network, "-l", "io.hostlier.resource=" + item.Name, "--timeout=120s"}, nil); e != nil {
		return nil, e
	}
	sum := sha256.Sum256([]byte(item.Name + time.Now().String()))
	helper := "maintenance-" + hex.EncodeToString(sum[:])[:16]
	uid := int64(0)
	// Run Neo4j maintenance as its image user so restored data retains the expected ownership.
	if item.Type == "neo4j" {
		uid = 7474
	}
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": m.meta(item.Name, helper), "spec": map[string]any{"restartPolicy": "Never", "hostname": "localhost", "automountServiceAccountToken": false, "securityContext": map[string]any{"runAsUser": uid, "fsGroup": 999}, "containers": []any{map[string]any{"name": "maintenance", "image": item.Spec.Image, "command": []string{"sh", "-c", "sleep 600"}, "volumeMounts": []any{map[string]string{"name": "data", "mountPath": "/data"}}}}, "volumes": []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]string{"claimName": "data"}}}}}
	// Require the owned Kubernetes manifest to apply successfully.
	if e = m.apply(ctx, pod, item.Name); e != nil {
		return nil, e
	}
	// Remove the temporary maintenance pod with an independent cleanup deadline.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = m.remove(cleanup, "pod", helper, item.Network, item.Name)
	}()
	// Require the explicitly scoped kubectl operation to complete successfully.
	if _, e = m.run(ctx, []string{"wait", "--for=condition=Ready", "pod/" + helper, "-n", item.Network, "--timeout=120s"}, nil); e != nil {
		return nil, e
	}
	return operation(helper)
}

// helperExec runs a script in the isolated maintenance pod.
func (m *K3sManager) helperExec(ctx context.Context, item record, pod, script string, input []byte) ([]byte, error) {
	return m.run(ctx, []string{"exec", "-i", "pod/" + pod, "-n", item.Network, "--", "sh", "-c", script}, input)
}

// Backup exports a bounded native snapshot of the selected resource.
func (m *K3sManager) Backup(ctx context.Context, name string, operation model.ResourceContext) (data []byte, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, e := m.find(name)
	// Require an existing resource record before operating on engine state.
	if e != nil {
		return nil, e
	}
	c := operation.Credentials
	// Validate engine credentials before issuing authenticated commands.
	if e = validateCredentials(item.Type, c); e != nil {
		return nil, e
	}
	data, err = m.backupEngine(ctx, item, c)
	// Return native export failures before accepting snapshot bytes.
	if err != nil {
		return nil, err
	}
	// Reject this request: Backup must be nonempty and at most64 MiB.
	if len(data) == 0 || len(data) > MaxBackupBytes {
		return nil, model.Err("BACKUP_LIMIT", "Backup must be nonempty and at most64 MiB")
	}
	return data, nil
}

// redisPersistence enables Redis AOF and waits for its durable rewrite.
func (m *K3sManager) redisPersistence(ctx context.Context, item record, c model.ResourceCredentials) error {
	out, e := m.engineExec(ctx, item, `IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; exec redis-cli --user hostlier CONFIG SET appendonly yes`, []byte(c.Password+"\n"))
	// Reject this request: Cannot enable Redis AOF after restore.
	if e != nil || strings.TrimSpace(string(out)) != "OK" {
		return model.Err("RESTORE_PERSISTENCE", "Cannot enable Redis AOF after restore")
	}
	deadline := time.Now().Add(m.options.HealthTimeout)
	// Poll Redis persistence until the rewrite completes or the operation deadline expires.
	for time.Now().Before(deadline) {
		out, e = m.engineExec(ctx, item, `IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; exec redis-cli --user hostlier INFO persistence`, []byte(c.Password+"\n"))
		// Require the authenticated engine command to complete successfully.
		if e != nil {
			return e
		}
		text := string(out)
		// Finish only after Redis reports a completed and successful AOF rewrite.
		if strings.Contains(text, "aof_rewrite_in_progress:0") && strings.Contains(text, "aof_last_bgrewrite_status:ok") {
			return nil
		}
		// Stop polling immediately when the operation context is cancelled.
		if e = wait(ctx, 250*time.Millisecond); e != nil {
			return e
		}
	}
	return model.Err("RESTORE_PERSISTENCE", "Redis AOF rewrite did not complete")
}

// redisSecret updates the Redis configuration Secret for the requested AOF mode.
func (m *K3sManager) redisSecret(ctx context.Context, item record, c model.ResourceCredentials, aof bool) error {
	// Find the generated credentials Secret so only its Redis configuration is updated.
	for _, manifest := range m.manifests(item.Spec, c) {
		// Restrict credential configuration to the Kubernetes Secret payload.
		if manifest["kind"] == "Secret" {
			data := manifest["stringData"].(map[string]string)
			data["redis.conf"] = redisConfig(c, aof)
			return m.apply(ctx, manifest, item.Name)
		}
	}
	return model.Err("RESOURCE_SECRET", "Redis configuration secret missing")
}

// Restore restores a bounded snapshot only with an accepted operation identifier.
func (m *K3sManager) Restore(ctx context.Context, name string, data []byte, operation model.ResourceContext) (err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Reject this request: Restore requires an accepted approved operation.
	if operation.OperationID == "" {
		return model.Err("APPROVAL_REQUIRED", "Restore requires an accepted approved operation")
	}
	// Reject this request: Backup must be nonempty and at most64 MiB.
	if len(data) == 0 || len(data) > MaxBackupBytes {
		return model.Err("BACKUP_LIMIT", "Backup must be nonempty and at most64 MiB")
	}
	item, e := m.find(name)
	// Require an existing resource record before operating on engine state.
	if e != nil {
		return e
	}
	c := operation.Credentials
	// Validate engine credentials before issuing authenticated commands.
	if e = validateCredentials(item.Type, c); e != nil {
		return e
	}
	// Select the native behavior for the requested engine or lifecycle operation.
	switch item.Type {
	// Use PostgreSQL native commands and its authenticated database configuration.
	case "postgres":
		input := append([]byte(c.Password+"\n"), data...)
		_, err = m.engineExec(ctx, item, `IFS= read -r PGPASSWORD; export PGPASSWORD; exec pg_restore -h 127.0.0.1 -U hostlier -d app --clean --if-exists --no-owner --no-acl --exit-on-error`, input)
	// Use Neo4j native commands and its authenticated database configuration.
	case "neo4j":
		err = m.restoreNeo4j(ctx, item, data, c)
	// Use Redis native commands and its authenticated persistence configuration.
	case "redis":
		err = m.restoreRedis(ctx, item, data, c)
	// Reject unsupported values rather than guessing an engine behavior.
	default:
		return model.Err("RESTORE_UNSUPPORTED", "Unsupported resource restore")
	}
	// Return the selected engine restore failure before checking authenticated readiness.
	if err != nil {
		return err
	}
	return m.ready(ctx, item, c)
}

// backupEngine selects the engine-native Kubernetes export.
func (m *K3sManager) backupEngine(ctx context.Context, item record, c model.ResourceCredentials) (data []byte, err error) {
	// Select the native behavior for the requested engine or lifecycle operation.
	switch item.Type {
	// Use PostgreSQL native commands and its authenticated database configuration.
	case "postgres":
		data, err = m.engineExec(ctx, item, `IFS= read -r PGPASSWORD; export PGPASSWORD; exec pg_dump -h 127.0.0.1 -U hostlier -d app --format=custom --no-owner --no-acl`, []byte(c.Password+"\n"))
	// Use Redis native commands and its authenticated persistence configuration.
	case "redis":
		_, err = m.engineExec(ctx, item, `IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; exec redis-cli --user hostlier --rdb /tmp/hostlier-backup.rdb`, []byte(c.Password+"\n"))
		// Read snapshot bytes only after Redis finishes generating its native RDB dump.
		if err == nil {
			data, err = m.engineExec(ctx, item, `cat /tmp/hostlier-backup.rdb`, nil)
		}
	// Use Neo4j native commands and its authenticated database configuration.
	case "neo4j":
		// Dump the selected Neo4j database and read its native dump from the helper pod.
		data, err = m.maintenance(ctx, item, c, func(helper string) ([]byte, error) {
			// Require the offline maintenance command to complete successfully.
			if _, e := m.helperExec(ctx, item, helper, `neo4j-admin database dump neo4j --to-path=/tmp`, nil); e != nil {
				return nil, e
			}
			return m.helperExec(ctx, item, helper, `cat /tmp/neo4j.dump`, nil)
		})
	// Reject unsupported values rather than guessing an engine behavior.
	default:
		return nil, model.Err("BACKUP_UNSUPPORTED", "Unsupported engine backup")
	}
	return data, err
}

// restoreRedis temporarily disables AOF and restores its durable configuration.
func (m *K3sManager) restoreRedis(ctx context.Context, item record, data []byte, c model.ResourceCredentials) (err error) {
	var e error
	// Require the Redis credential configuration update before continuing restore.
	if e = m.redisSecret(ctx, item, c, false); e != nil {
		return e
	}
	// Restore Redis AOF configuration when the restore scope exits with a failure.
	defer func() {
		// Restore the AOF-enabled Secret on failure without masking the original restore error.
		if err != nil {
			_ = m.redisSecret(context.Background(), item, c, true)
		}
	}()
	// Validate and install the supplied RDB into the selected resource data volume.
	_, err = m.maintenance(ctx, item, c, func(helper string) ([]byte, error) {
		// Require the offline maintenance command to complete successfully.
		if _, e := m.helperExec(ctx, item, helper, `cat > /tmp/restore.rdb`, data); e != nil {
			return nil, e
		}
		return m.helperExec(ctx, item, helper, `redis-check-rdb /tmp/restore.rdb >/dev/null && rm -rf /data/appendonlydir /data/dump.rdb && cp /tmp/restore.rdb /data/dump.rdb && chown 999:999 /data/dump.rdb`, nil)
	})
	// Enable AOF only after the offline RDB restore succeeds.
	if err == nil {
		// Publish the AOF-enabled configuration only after its durable rewrite completes.
		if err = m.redisPersistence(ctx, item, c); err == nil {
			err = m.redisSecret(ctx, item, c, true)
		}
	}
	return err
}

// restoreNeo4j loads the native dump through an isolated maintenance pod.
func (m *K3sManager) restoreNeo4j(ctx context.Context, item record, data []byte, c model.ResourceCredentials) error {
	// Upload and load the supplied Neo4j dump in the selected maintenance pod.
	_, err := m.maintenance(ctx, item, c, func(helper string) ([]byte, error) {
		// Require the offline maintenance command to complete successfully.
		if _, e := m.helperExec(ctx, item, helper, `cat > /tmp/neo4j.dump`, data); e != nil {
			return nil, e
		}
		return m.helperExec(ctx, item, helper, `neo4j-admin database load neo4j --from-path=/tmp --overwrite-destination=true`, nil)
	})
	return err
}
