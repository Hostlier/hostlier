package controller

import (
	"hostlier/internal/store"
	"net/http"
)

// apiBackups routes authenticated requests for encrypted backup export.
func (c *Controller) apiBackups(r *http.Request, p []string, actor string) (any, error) {

	// Monitoring may list receipt metadata without granting backup-content export authority.
	if r.Method == "GET" && len(p) == 2 {
		backups, err := collectionMap[BackupRecord](r.Context(), c.store, "backups", store.CollectionQuery{})
		return map[string]any{"backups": backups}, err
	}
	return c.backupExport(r, p)
}
