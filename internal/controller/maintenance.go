package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"hostlier/internal/model"
)

// serveMaintenanceRead performs expensive inspection without holding controller
// state locks. It shares signed-device authentication with the command RPC.
func (c *Controller) serveMaintenanceRead(w http.ResponseWriter, r *http.Request, local bool) bool {
	// Only this narrow namespace bypasses request-wide state locking.
	if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/v1/controller/") {
		return false
	}
	actor, generation, err := c.maintenanceAuthority(r, local)
	// Never invoke the privileged provider before authorization succeeds.
	if err != nil {
		writeMaintenanceResult(w, nil, err)
		return true
	}
	// Release download/inspection work from the ordinary short response deadline.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Minute))
	value, err := c.maintenanceRead(r, actor, generation)
	writeMaintenanceResult(w, value, err)
	return true
}

// maintenanceAuthority captures immutable request identity under the controller lock.
func (c *Controller) maintenanceAuthority(r *http.Request, local bool) (string, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	actor, err := c.authenticate(r, local, false)
	// Authentication also verifies possession of the saved device key and live scope.
	if err != nil {
		return "", 0, err
	}
	// Fleet delegation cannot grant server-wide maintenance authority.
	if err := c.maintenanceAdministrator(actor); err != nil {
		return "", 0, err
	}
	// Update previews require a stable, idle source just like the apply boundary.
	if r.URL.Path == "/v1/controller/update/plan" {
		// Respect an already accepted detached update before admitting new work.
		if err := c.controllerMaintenanceAvailable(""); err != nil {
			return "", 0, err
		}
		// Require a settled source before reviewing or accepting a controller restart.
		if err := c.controllerUpdateIdle(); err != nil {
			return "", 0, err
		}
	}
	return actor, c.state.Generation, nil
}

// maintenanceAdministrator keeps deployment credentials and fleet actors out of root operations.
func (c *Controller) maintenanceAdministrator(actor string) error {
	// Agent listeners do not provide controller administration.
	if c.agentOnly {
		return model.Err("SCOPE_DENIED", "Controller maintenance is unavailable on a delegated agent")
	}
	// The protected local socket retains its established administration authority.
	if actor == "local" {
		return nil
	}
	device := c.state.Clients[actor]
	// An unknown delegated identity cannot borrow local root privileges.
	if device == nil {
		return model.Err("SCOPE_DENIED", "Controller administration requires an owner login")
	}
	// Legacy unscoped devices still require independent approval for mutations.
	if device.DeploymentGrant == "" {
		return nil
	}
	grant := c.state.Authorizations[device.DeploymentGrant]
	// Only a live all-access grant confers server-wide administration.
	if grant == nil || grant.Scope != "all" || !c.authorizationLive(grant) {
		return model.Err("SCOPE_DENIED", "Controller maintenance requires an all-access login")
	}
	return nil
}

// maintenanceRead exposes fixed operations, never a generic remote command or path.
func (c *Controller) maintenanceRead(r *http.Request, actor string, generation int64) (any, error) {
	// Capabilities let a newer CLI explain the one-time bootstrap requirement.
	if r.URL.Path == "/v1/controller/capabilities" {
		_, preparation := c.opts.Maintenance.(model.ControllerMigrationPreparation)
		return map[string]bool{"updates": c.opts.Maintenance != nil, "migrationPreview": c.opts.Maintenance != nil, "migrationPreparation": preparation}, nil
	}
	// Embedded/test controllers cannot accidentally gain privileged execution.
	if c.opts.Maintenance == nil {
		return nil, model.Err("MAINTENANCE_UNAVAILABLE", "This controller installation does not support remote maintenance")
	}
	// Dispatch only the fixed maintenance operations supported by this interface.
	switch r.URL.Path {
	// Prerequisite inspection never invokes local preparation apply or rollback.
	case "/v1/controller/migration/preparation":
		provider, ok := c.opts.Maintenance.(model.ControllerMigrationPreparation)
		// Older embedded providers cannot implicitly gain host remediation support.
		if !ok {
			return nil, model.Err("MAINTENANCE_UNAVAILABLE", "Upgrade the controller for migration preparation previews")
		}
		return provider.PreviewPreparation(r.Context(), r.URL.Query().Get("backupDir"))
	// Source paths and credentials come only from the installed server configuration.
	case "/v1/controller/migration/plan":
		// Reject runtime directions outside the supported Docker to k3s migration.
		if r.URL.Query().Get("to") != "k3s" {
			return nil, model.Err("INVALID_REQUEST", "Migration preview supports Docker to k3s")
		}
		return c.opts.Maintenance.PreviewMigration(r.Context(), r.URL.Query().Get("backupDir"))
	// Release selection is fixed by the server's verified official-release policy.
	case "/v1/controller/update/plan":
		return c.opts.Maintenance.PreviewUpdate(r.Context(), actor, generation)
	}
	// Job identifiers cannot contain path separators.
	prefix := "/v1/controller/update/jobs/"
	// Only the fixed job-receipt namespace may select a retained update identifier.
	if strings.HasPrefix(r.URL.Path, prefix) {
		return c.opts.Maintenance.UpdateStatus(r.Context(), actor, strings.TrimPrefix(r.URL.Path, prefix))
	}
	return endpointNotFound()
}

// writeMaintenanceResult uses the API's sanitized error envelope on HTTP and gRPC.
func writeMaintenanceResult(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	// Raw provider errors may contain privileged paths or subprocess diagnostics.
	if err != nil {
		public := &model.Error{Code: "MAINTENANCE_FAILED", Message: "Controller maintenance failed; inspect the retained job"}
		var typed *model.Error
		// Expose recognized public errors while withholding raw provider diagnostics.
		if errors.As(err, &typed) {
			public = typed
		}
		w.WriteHeader(errorStatus(public.Code))
		_ = json.NewEncoder(w).Encode(map[string]any{"error": public})
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

// apiControllerMaintenance is reachable through the normal independent approval boundary.
func (c *Controller) apiControllerMaintenance(r *http.Request, p []string, actor string) (any, error) {
	// No migration execution or arbitrary process launch is exposed remotely.
	if r.Method != "POST" || r.URL.Path != "/v1/controller/update/apply" {
		return endpointNotFound()
	}
	// Server-wide maintenance requires owner administration authority.
	if err := c.maintenanceAdministrator(actor); err != nil {
		return nil, err
	}
	// Embedded or older controllers cannot implicitly execute privileged maintenance.
	if c.opts.Maintenance == nil {
		return nil, model.Err("MAINTENANCE_UNAVAILABLE", "Controller updates require an installed maintenance-capable release")
	}
	// Require a settled source before reviewing or accepting a controller restart.
	if err := c.controllerUpdateIdle(); err != nil {
		return nil, err
	}
	var request model.ControllerUpdateRequest
	// Reject malformed or unexpected apply fields before using retained review identifiers.
	if err := decode(r, &request); err != nil {
		return nil, err
	}
	// Respect an already accepted detached update before admitting new work.
	if err := c.controllerMaintenanceAvailable(request.ID); err != nil {
		return nil, err
	}
	job, err := c.opts.Maintenance.StartUpdate(r.Context(), actor, c.state.Generation, request)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return nil, err
	}
	c.state.ControllerUpdateID = job.ID
	return job, nil
}

// controllerUpdateIdle rejects accepted or uncertain workload changes before restart.
func (c *Controller) controllerUpdateIdle() error {
	// Reject workload operations whose external effects are not settled.
	for _, op := range c.state.Operations {
		// Unfinished or interrupted operations require resolution before an API restart.
		if op.Status == "queued" || op.Status == "running" || op.Status == "interrupted" {
			return model.Err("CONTROLLER_BUSY", "Resolve active or interrupted operations before updating the controller")
		}
	}
	// Wait for every accepted planning job before changing the controller.
	for _, job := range c.state.PlanningJobs {
		// Active or uncertain jobs retain ownership of their workload effects.
		if job.Status == "queued" || job.Status == "running" {
			return model.Err("CONTROLLER_BUSY", "Wait for planning to finish before updating the controller")
		}
	}
	return c.controllerRecoveryIdle()
}

// controllerRecoveryIdle includes uncertain jobs that may already have changed data.
func (c *Controller) controllerRecoveryIdle() error {
	// Recovery reservations include uncertain jobs that may have changed data.
	for _, job := range c.state.RecoveryJobs {
		// Active or uncertain jobs retain ownership of their workload effects.
		if job.Status == "queued" || job.Status == "running" || job.Status == "uncertain" {
			return model.Err("CONTROLLER_BUSY", "Resolve active or uncertain recovery jobs before updating the controller")
		}
	}
	return nil
}

// controllerMaintenanceAvailable keeps a detached restart from racing accepted mutations.
// The caller holds the controller lock; terminal receipts release the reservation.
func (c *Controller) controllerMaintenanceAvailable(sameID string) error {
	id := c.state.ControllerUpdateID
	// Ordinary work proceeds when no update holds the maintenance reservation.
	if id == "" {
		return nil
	}
	// Embedded or older controllers cannot implicitly execute privileged maintenance.
	if c.opts.Maintenance == nil {
		return model.Err("CONTROLLER_BUSY", "Controller maintenance reservation requires server inspection")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	job, err := c.opts.Maintenance.UpdateStatus(ctx, "local", id)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return model.Err("CONTROLLER_BUSY", "Cannot verify the retained controller update; inspect it before proceeding")
	}
	// Replaying the original update only returns its existing receipt.
	if job.Status == "queued" || job.Status == "running" {
		// An identical retained request may retrieve its existing acceptance receipt.
		if sameID == id {
			return nil
		}
		return model.Err("CONTROLLER_BUSY", "Controller update is in progress; wait for its retained job to finish")
	}
	c.state.ControllerUpdateID = ""
	return nil
}

// maintenanceMutation reserves privileged changes and new planning work during updates.
func maintenanceMutation(r *http.Request) bool {
	// Read traffic remains available while maintenance reserves mutation admission.
	if r.Method == "GET" || r.Method == "HEAD" {
		return false
	}
	// The update route applies its own exact-request and reservation validation.
	if r.URL.Path == "/v1/controller/update/apply" {
		return false
	}
	return model.RequiresHumanApproval(r.Method, r.URL.Path) || maintenancePreparation(r.URL.Path)
}

// maintenancePreparation prevents accepting new planning work during an installation reservation.
func maintenancePreparation(path string) bool {
	return path == "/v1/plans" || path == "/v1/resource-plans" || strings.HasPrefix(path, "/v1/planning")
}

// controllerUpdateReview includes the server-retained release and installation changes.
func (c *Controller) controllerUpdateReview(request humanRequest, actor string, review map[string]any) (json.RawMessage, error) {
	// Server-wide maintenance requires owner administration authority.
	if err := c.maintenanceAdministrator(actor); err != nil {
		return nil, err
	}
	// Embedded or older controllers cannot implicitly execute privileged maintenance.
	if c.opts.Maintenance == nil {
		return nil, model.Err("MAINTENANCE_UNAVAILABLE", "Controller update support is unavailable")
	}
	var input model.ControllerUpdateRequest
	// Reject malformed retained or target data before treating it as executable authority.
	if err := json.Unmarshal(request.Body, &input); err != nil {
		return nil, model.Err("INVALID_REQUEST", "Invalid controller update request")
	}
	plan, err := c.opts.Maintenance.ReviewUpdate(c.ctx, actor, c.state.Generation, input)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return nil, err
	}
	review["controllerUpdate"] = plan
	return json.Marshal(review)
}

// checkMaintenanceMutation applies the update reservation only to new protected work.
func (c *Controller) checkMaintenanceMutation(r *http.Request) error {
	// Only state-changing or newly accepted planning work needs this reservation.
	if maintenanceMutation(r) {
		return c.controllerMaintenanceAvailable("")
	}
	return nil
}

// serveUnserializedRead keeps observations and long inspections outside normal aggregate dispatch.
func (c *Controller) serveUnserializedRead(w http.ResponseWriter, r *http.Request, local bool) bool {
	// Root maintenance reads retain authentication while releasing locks during inspection.
	if c.serveMaintenanceRead(w, r, local) {
		return true
	}
	// Collector writes and history queries never persist or lock deployment state during I/O.
	if c.serveActivity(w, r, local) {
		return true
	}
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if c.serveTelemetry(w, r, local) {
		return true
	}
	// Shared observations use an independent cache and never persist read traffic.
	if c.serveStatusObservation(w, r, local) {
		return true
	}
	// Stream progress outside the ordinary request-wide state lock.
	if operationEventsRoute(r) {
		c.serveOperationEvents(w, r, local)
		return true
	}
	return false
}
