package resources

import (
	"context"

	"hostlier/internal/model"
)

// Backup exports an engine-native snapshot. Neo4j Community dump is offline:
// it stops this instance and restores its previous running state afterwards.
// The current transport intentionally bounds a single backup to 64 MiB.
func (m *Manager) Backup(ctx context.Context, name string, operation model.ResourceContext) (data []byte, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.find(name)
	// Require an existing resource record before operating on engine state.
	if err != nil {
		return nil, err
	}
	c := operation.Credentials
	// Validate engine credentials before issuing authenticated commands.
	if err = validateCredentials(item.Type, c); err != nil {
		return nil, err
	}
	data, err = m.backupEngine(ctx, item, c)
	// Return native export failures before accepting snapshot bytes.
	if err != nil {
		return nil, err
	}
	// Reject this request: Backup must contain data and be no larger than 64 MiB.
	if len(data) == 0 || len(data) > MaxBackupBytes {
		return nil, model.Err("BACKUP_LIMIT", "Backup must contain data and be no larger than 64 MiB")
	}
	return data, nil
}

// restartAfterMaintenance restarts the original engine with an independent cleanup deadline.
func (m *Manager) restartAfterMaintenance(name string, spec model.ResourceSpec, c model.ResourceCredentials) error {
	ctx, cancel := context.WithTimeout(context.Background(), m.options.HealthTimeout)
	defer cancel()
	current, err := m.inspect(ctx, name)
	// Require a verified owned container before changing or reporting its state.
	if err != nil {
		return err
	}
	// Require the owned Docker API request to complete before using its result.
	if _, err = m.api(ctx, "POST", "/containers/"+current.ID+"/start", nil, nil); err != nil {
		return err
	}
	return m.ready(ctx, spec, c)
}

// Restore overwrites the selected instance through an approved operation. The
// controller must reject active dependents and durably retain approval first.
func (m *Manager) Restore(ctx context.Context, name string, data []byte, operation model.ResourceContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Reject this request: Restore requires an accepted approved operation.
	if operation.OperationID == "" {
		return model.Err("APPROVAL_REQUIRED", "Restore requires an accepted approved operation")
	}
	// Reject this request: Backup must contain data and be no larger than 64 MiB.
	if len(data) == 0 || len(data) > MaxBackupBytes {
		return model.Err("BACKUP_LIMIT", "Backup must contain data and be no larger than 64 MiB")
	}
	item, err := m.find(name)
	// Require an existing resource record before operating on engine state.
	if err != nil {
		return err
	}
	c := operation.Credentials
	// Validate engine credentials before issuing authenticated commands.
	if err = validateCredentials(item.Type, c); err != nil {
		return err
	}
	current, err := m.inspect(ctx, name)
	// Require a verified owned container before changing or reporting its state.
	if err != nil {
		return err
	}
	err = m.restoreEngine(ctx, item, current.ID, current.State.Running, data, c)
	// Return engine restore failures before reporting resource readiness.
	if err != nil {
		return err
	}
	return m.ready(ctx, item.Spec, c)
}

// backupEngine exports the selected engine using its native format.
func (m *Manager) backupEngine(ctx context.Context, item record, c model.ResourceCredentials) (data []byte, err error) {
	// Select the native behavior for the requested engine or lifecycle operation.
	switch item.Type {
	// Use PostgreSQL native commands and its authenticated database configuration.
	case "postgres":
		data, err = m.execute(ctx, item.Name, []string{"pg_dump", "-h", "127.0.0.1", "-U", c.Username, "-d", c.Database, "--format=custom", "--no-owner", "--no-acl"}, authEnv(item.Type, c))
	// Use Redis native commands and its authenticated persistence configuration.
	case "redis":
		file := "/tmp/hostlier-backup.rdb"
		// Read snapshot bytes only after Redis finishes generating its native RDB dump.
		if _, err = m.execute(ctx, item.Name, []string{"redis-cli", "--user", c.Username, "--rdb", file}, authEnv(item.Type, c)); err == nil {
			data, err = m.execute(ctx, item.Name, []string{"cat", file}, nil)
		}
		m.execute(ctx, item.Name, []string{"rm", "-f", file}, nil)
	// Use Neo4j native commands and its authenticated database configuration.
	case "neo4j":
		current, inspectErr := m.inspect(ctx, item.Name)
		// Require a verified owned container before changing or reporting its state.
		if inspectErr != nil {
			return nil, inspectErr
		}
		// Preserve the engine running state across offline maintenance.
		if current.State.Running {
			// Require the owned Docker API request to complete before using its result.
			if _, err = m.api(ctx, "POST", "/containers/"+current.ID+"/stop?t=30", nil, nil); err != nil {
				return nil, err
			}
			// Restart the original Neo4j container when offline backup maintenance finishes.
			defer func() {
				restartErr := m.restartAfterMaintenance(item.Name, item.Spec, c)
				// Surface restart failure only when it will not mask the original maintenance error.
				if err == nil && restartErr != nil {
					err = restartErr
				}
			}()
		}
		data, err = m.helper(ctx, item, []string{"neo4j-admin"}, []string{"database", "dump", "neo4j", "--to-path=/tmp"}, nil, "", 7474, "neo4j.dump")
	// Reject unsupported values rather than guessing an engine behavior.
	default:
		return nil, model.Err("BACKUP_UNSUPPORTED", "Unsupported resource backup engine")
	}
	return data, err
}
