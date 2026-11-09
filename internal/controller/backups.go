package controller

import (
	"context"
	"encoding/base64"
	"net/http"

	"hostlier/internal/model"
	"hostlier/internal/recovery"
	"time"
)

// BackupRecord identifies an encrypted backup and its immutable source metadata.
type BackupRecord struct {
	Spec      *model.ResourceSpec     `json:"spec,omitempty"`
	Evidence  *model.DatabaseEvidence `json:"evidence,omitempty"`
	ID        string                  `json:"id"`
	Name      string                  `json:"name"`
	Type      string                  `json:"type"`
	CreatedAt int64                   `json:"createdAt"`
	Size      int                     `json:"size"`
}

// backupManager defines the optional runtime backup and restore capability.
type backupManager interface {
	// Backup returns a bounded native payload while honoring engine maintenance rules.
	Backup(context.Context, string, model.ResourceContext) ([]byte, error)
	// Restore applies a native payload to an approved independent target.
	Restore(context.Context, string, []byte, model.ResourceContext) error
}

// createBackup releases the state lock during long backup work while retaining operation tracking.
func (c *Controller) createBackup(r *http.Request, resource *ResourceRecord) (any, error) {
	// Reject unexpected payload fields on this bodyless action.
	if err := emptyBody(r); err != nil {
		return nil, err
	}
	// Reject this path: Offline Neo4j backup requires detaching active application dependencies first.
	if resource.Type == "neo4j" && len(c.resourceDependents(resource.Name)) > 0 {
		return nil, model.Err("RESOURCE_IN_USE", "Offline Neo4j backup requires detaching active application dependencies first")
	}
	manager, ok := c.opts.ResourceManager.(backupManager)
	// Reject this path: Resource backup unavailable.
	if !ok {
		return nil, model.Err("UNSUPPORTED_OPERATION", "Resource backup unavailable")
	}
	// Reject this path: Resource has active operation.
	if c.resourceBusy(resource.Name) {
		return nil, model.Err("RESOURCE_BUSY", "Resource has active operation")
	}
	credentials, err := c.resourceCredentials(resource.Name, resource.Spec)
	// Require stable protected resource credentials before invoking runtime work.
	if err != nil {
		return nil, err
	}
	id := random()
	op := &Operation{ID: id, Kind: "resource-backup", Name: resource.Name, Status: "queued"}
	c.state.Operations[id] = op
	// Require durable state persistence before reporting success or scheduling side effects.
	if err = c.store.Save(c.state); err != nil {
		delete(c.state.Operations, id)
		return nil, err
	}
	c.wg.Add(1)
	defer c.wg.Done()
	c.mu.Unlock()
	c.execMu.Lock()
	c.mu.Lock()
	op.Status = "running"
	err = c.store.Save(c.state)
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, c.opts.LongOperationTimeout)
	stop := context.AfterFunc(r.Context(), cancel)
	var data []byte
	// Invoke the resource manager only after running state was persisted.
	if err == nil {
		data, err = manager.Backup(ctx, resource.Name, model.ResourceContext{Credentials: credentials, OperationID: id})
	}
	stop()
	cancel()
	c.mu.Lock()
	c.execMu.Unlock()
	// The request handler's state lock is held again on every return path.
	if err != nil {
		op.Status = "failed"
		op.Error = &model.Error{Code: "BACKUP_FAILED", Message: "Resource backup failed"}
		c.store.Save(c.state)
		return nil, op.Error
	}
	return c.commitBackup(resource, op, data)
}

// backupBytes loads and decrypts an existing backup for authorized export or restore.
func (c *Controller) backupBytes(id string) ([]byte, error) {
	// Reject this path: Backup not found.
	if _, exists := c.state.Backups[id]; !exists {
		return nil, model.Err("NOT_FOUND", "Backup not found")
	}
	encrypted, err := c.store.ReadBlob(id + ".backup")
	// Require the persisted artifact bytes before decoding or exposing their contents.
	if err != nil {
		return nil, err
	}
	plain, err := c.store.Decrypt(string(encrypted))
	return []byte(plain), err
}

// backupExport exposes backup bytes only through the authenticated export route.
func (c *Controller) backupExport(r *http.Request, p []string) (any, error) {
	// Reject this path: Endpoint not found.
	if r.Method != "GET" || len(p) != 4 || p[3] != "export" {
		return nil, model.Err("NOT_FOUND", "Endpoint not found")
	}
	record, exists := c.state.Backups[p[2]]
	// Reject this path: Backup not found.
	if !exists {
		return nil, model.Err("NOT_FOUND", "Backup not found")
	}
	data, err := c.backupBytes(record.ID)
	// Reject this path: Backup could not be read.
	if err != nil {
		return nil, model.Err("BACKUP_UNAVAILABLE", "Backup could not be read")
	}
	return map[string]any{"data": base64.StdEncoding.EncodeToString(data), "name": record.Name, "type": record.Type, "backupId": record.ID}, nil
}

// executeRestore restores an approved backup into the separately provisioned target.
func (c *Controller) executeRestore(ctx context.Context, op *Operation, onEvent func(model.Event)) error {
	manager, ok := c.opts.ResourceManager.(backupManager)
	// Reject this path: Resource restore unavailable.
	if !ok {
		return model.Err("UNSUPPORTED_OPERATION", "Resource restore unavailable")
	}
	c.mu.Lock()
	data, err := c.backupBytes(op.RestoreBackupID)
	var credentials model.ResourceCredentials
	// Load stable target credentials only after the backup was decrypted.
	if err == nil {
		credentials, err = c.resourceCredentials(op.Name, *op.ResourceSpec)
	}
	c.mu.Unlock()
	// Require stable protected resource credentials before invoking runtime work.
	if err != nil {
		return err
	}
	operation := model.ResourceContext{Credentials: credentials, OperationID: op.ID, OnEvent: onEvent, PackageDigest: op.ResourceSpec.PackageDigest}
	// Propagate failure before performing the next recovery side effect.
	if err = manager.Restore(ctx, op.Name, data, operation); err != nil {
		return err
	}
	c.mu.Lock()
	backup := c.state.Backups[op.RestoreBackupID]
	c.mu.Unlock()
	// Advance only after the required recovery invariant holds.
	if backup.Evidence != nil {
		verifier, ok := c.opts.ResourceManager.(model.DatabaseRecoveryManager)
		// Reject this request: Native restore verification unavailable.
		if !ok {
			return model.Err("RECOVERY_VERIFY", "Native restore verification unavailable")
		}
		observed, err := verifier.Evidence(ctx, op.Name, operation)
		// Propagate failure before performing the next recovery side effect.
		if err != nil {
			return err
		}
		return recovery.Match(*backup.Evidence, observed, time.Now().UnixMilli())
	}
	return nil
}

// commitBackup encrypts backup bytes and durably records their metadata.
func (c *Controller) commitBackup(resource *ResourceRecord, op *Operation, data []byte) (any, error) {
	id := op.ID
	op.Status = "failed"
	op.Error = &model.Error{Code: "BACKUP_FAILED", Message: "Backup could not be durably committed"}
	// Persist the final backup operation status on every exit path.
	defer func() { c.store.Save(c.state) }()
	// Reject this path: Backup must contain 1 byte to 256 MiB.
	if len(data) == 0 || len(data) > 256*1024*1024 {
		return nil, model.Err("BACKUP_SIZE", "Backup must contain 1 byte to 256 MiB")
	}
	encrypted, err := c.store.Encrypt(string(data))
	// Never persist private data when encryption fails.
	if err != nil {
		return nil, err
	}
	// Stop if durable storage rejects the encrypted backup artifact.
	if err = c.store.WriteBlob(id+".backup", []byte(encrypted)); err != nil {
		return nil, err
	}
	record := BackupRecord{ID: id, Name: resource.Name, Type: resource.Type, CreatedAt: c.opts.Now().UnixMilli(), Size: len(data)}
	c.state.Backups[id] = record
	op.Status = "succeeded"
	op.Error = nil
	c.log("Resource backup committed", id, "info")
	return map[string]any{"backupId": id, "name": record.Name, "type": record.Type, "createdAt": record.CreatedAt, "size": record.Size}, nil
}
