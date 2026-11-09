package cli

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"

	"hostlier/internal/model"
)

// resource selects the requested private resource command.
func (s *session) resource() error {
	handlers := map[string]func() error{"upgrade": s.resourceUpgrade, "upgrade-status": s.resourceUpgrade, "cutover": s.resourceUpgrade, "abort-upgrade": s.resourceUpgrade, "scale": s.resourceScale, "create": s.resourceCreate, "restore": s.resourceCreate, "backup": s.resourceBackup, "export": s.resourceBackup, "list": s.resourceList, "": s.resourceList, "status": s.resourceStatus, "logs": s.resourceLogs, "start": s.resourceAction, "stop": s.resourceAction, "restart": s.resourceAction, "delete": s.resourceAction}
	// Reject this path: Unsupported resource command; use create, list, status, logs, start, stop, restart, or delete.
	if handler := handlers[s.sub]; handler != nil {
		return handler()
	}
	return model.Err("UNSUPPORTED_COMMAND", "Unsupported resource command; use create, list, status, logs, start, stop, restart, or delete")
}

// resourceCreate constructs provisioning or restore intent for exact-plan approval.
func (s *session) resourceCreate() error {
	// Reject this path: Usage: resource create|restore postgres|redis|neo4j NAME --storage SIZE [--backup ID].
	if len(s.parsed.args) != 4 || s.flags["storage"] == "" {
		return model.Err("ARGUMENTS", "Usage: resource create|restore postgres|redis|neo4j NAME --storage SIZE [--backup ID]")
	}
	// Reject this path: resource restore requires --backup BACKUP_ID.
	if s.sub == "restore" && s.flags["backup"] == "" {
		return model.Err("ARGUMENTS", "resource restore requires --backup BACKUP_ID")
	}
	spec := model.ResourceSpec{Name: s.position(3), Type: s.target, Storage: s.flags["storage"]}
	body := map[string]any{"spec": spec}
	// Carry the immutable backup identity into the restore approval plan.
	if s.sub == "restore" {
		body["restoreBackupId"] = s.flags["backup"]
	}
	return s.reviewPlan(planFlow{path: "/v1/resource-plans", body: body, prompt: "Approve this exact resource provisioning plan? [y/N] ", staleMessage: "Server state changed. Review and approve the updated resource plan."})
}

// resourceBackup creates a backup or exports an identified backup to an explicit file.
func (s *session) resourceBackup() error {
	var e error

	// Reject this path: Resource name or backup ID required.
	if s.target == "" {
		return model.Err("ARGUMENTS", "Resource name or backup ID required")
	}
	backupID := s.target
	var created map[string]any
	// Create a new backup before exporting when the backup command was used.
	if s.sub == "backup" {
		created, e = s.createResourceBackup()
		// Do not export an artifact from a failed backup operation.
		if e != nil {
			return e
		}
		backupID, _ = created["backupId"].(string)
		// Reject this path: Controller returned no backup ID.
		if backupID == "" {
			return model.Err("BACKUP_RESPONSE", "Controller returned no backup ID")
		}
		// Print backup metadata when no export path was requested.
		if s.flags["output"] == "" {
			return s.print(created)
		}
	}
	return s.exportResourceBackup(backupID)
}

// resourceList prints visible private resources without exposing their credentials.
func (s *session) resourceList() error {
	var e error

	var result any
	// Require the requested controller data before displaying or acting on it.
	if e = s.request("GET", "/v1/resources", nil, &result); e != nil {
		return e
	}
	return s.print(result)
}

// resourceStatus prints the requested private resource state.
func (s *session) resourceStatus() error {
	var e error

	// Reject this path: Resource name required.
	if s.target == "" {
		return model.Err("ARGUMENTS", "Resource name required")
	}
	var result any
	// Require the requested controller data before displaying or acting on it.
	if e = s.request("GET", "/v1/resources/"+url.PathEscape(s.target), nil, &result); e != nil {
		return e
	}
	return s.print(result)
}

// resourceLogs follows redacted logs for a named private resource.
func (s *session) resourceLogs() error {
	// Reject this path: Resource name required.
	if s.target == "" {
		return model.Err("ARGUMENTS", "Resource name required")
	}
	return s.followLogs("/v1/resources/" + url.PathEscape(s.target) + "/logs")
}

// resourceAction validates private resource identity before the shared lifecycle workflow.
func (s *session) resourceAction() error {
	// Reject this path: Resource name required.
	if s.target == "" {
		return model.Err("ARGUMENTS", "Resource name required")
	}
	return s.performAction("/v1/resources/"+url.PathEscape(s.target)+"/action", s.sub, "Permanently purge this resource's owned data? [y/N] ", "--purge requires resource delete")
}

// createResourceBackup requires a maintenance-window approval for offline Neo4j backups.
func (s *session) createResourceBackup() (map[string]any, error) {
	var e error
	var created map[string]any

	var status resourceTypeResponse
	// Require the requested controller data before displaying or acting on it.
	if e = s.request("GET", "/v1/resources/"+url.PathEscape(s.target), nil, &status); e != nil {
		return nil, e
	}
	// Require approval for the offline Neo4j maintenance window.
	if status.Type == "neo4j" {
		s.message("Neo4j Community backup requires stopping this resource, then restarting it after the dump.")
		// Require explicit interactive approval before performing the reviewed mutation.
		if e = s.approve("Approve this offline backup maintenance window? [y/N] "); e != nil {
			return nil, e
		}
	}
	// Require a successful backup operation before reporting its artifact identity.
	if e = s.request("POST", "/v1/resources/"+url.PathEscape(s.target)+"/backups", map[string]any{}, &created); e != nil {
		return nil, e
	}
	return created, nil
}

// exportResourceBackup decodes protected backup data and writes it to a new owner-only file.
func (s *session) exportResourceBackup(backupID string) error {
	var e error
	outputPath := s.flags["output"]
	// Reject this path: resource export requires --output FILE.
	if outputPath == "" {
		return model.Err("ARGUMENTS", "resource export requires --output FILE")
	}
	var exported backupExportResponse
	// Fetch backup bytes only through the authenticated export endpoint.
	if e = s.request("GET", "/v1/backups/"+url.PathEscape(backupID)+"/export", nil, &exported); e != nil {
		return e
	}
	data, decodeErr := base64.StdEncoding.DecodeString(exported.Data)
	// Reject this path: Invalid backup export.
	if decodeErr != nil || len(data) == 0 {
		return model.Err("BACKUP_RESPONSE", "Invalid backup export")
	}
	// Resolve relative export paths from the invocation working directory.
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(s.cwd, outputPath)
	}
	// Do not report exported backup bytes until their new private file is durable.
	if err := writeBackupOutput(outputPath, data); err != nil {
		return err
	}

	return s.print(map[string]any{"backupId": backupID, "name": exported.Name, "type": exported.Type, "output": outputPath, "bytes": len(data)})
}

// writeBackupOutput creates backup output exclusively and syncs both file and directory.
func writeBackupOutput(outputPath string, data []byte) error {
	file, writeErr := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	// Reject this path: Cannot create backup file; choose a new writable path.
	if writeErr != nil {
		return model.Err("BACKUP_OUTPUT", "Cannot create backup file; choose a new writable path")
	}
	_, writeErr = file.Write(data)
	// Sync bytes only after the complete write succeeds.
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	// Reject this path: Cannot write backup output file.
	if writeErr != nil || closeErr != nil {
		os.Remove(outputPath)
		return model.Err("BACKUP_OUTPUT", "Cannot write backup output file")
	}
	directory, syncErr := os.Open(filepath.Dir(outputPath))
	// Sync the containing directory after opening it successfully.
	if syncErr == nil {
		syncErr = directory.Sync()
		directory.Close()
	}
	// Reject this path: Backup file written but its directory could not be synced; verify the output file.
	if syncErr != nil {
		return model.Err("BACKUP_OUTPUT", "Backup file written but its directory could not be synced; verify the output file")
	}
	return nil
}

// resourceTypeResponse decodes only the response fields needed by this CLI workflow.
type resourceTypeResponse struct {
	Type string `json:"type"`
}

// backupExportResponse decodes only the response fields needed by this CLI workflow.
type backupExportResponse struct {
	Data string `json:"data"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// resourceScale requests an exact allocation plan and its explicit maintenance approval.
func (s *session) resourceScale() error {
	// Require both quantities to avoid implicit backend defaults during maintenance.
	if s.target == "" || len(s.parsed.args) != 3 || s.flags["cpu"] == "" || s.flags["memory"] == "" {
		return model.Err("ARGUMENTS", "Usage: resource scale NAME --cpu CORES --memory SIZE")
	}
	body := map[string]any{"name": s.target, "cpu": s.flags["cpu"], "memory": s.flags["memory"]}
	return s.reviewPlan(planFlow{path: "/v1/resource-plans/resize", body: body, prompt: "Approve this exact database allocation and maintenance window? [y/N] ", staleMessage: "Server state changed. Review and approve the updated resource resize plan."})
}
