// Package resourcepackage hosts the engine adapters outside the controller process.
package resourcepackage

import (
	"context"
	"encoding/json"
	"errors"
	"hostlier/internal/model"
	"hostlier/internal/resources"
	"hostlier/pkg/resourceprotocol"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// backupManager describes engine snapshot operations.
type backupManager interface {
	Backup(context.Context, string, model.ResourceContext) ([]byte, error)
	Restore(context.Context, string, []byte, model.ResourceContext) error
}

// Serve handles exactly one private resource request for a fixed engine.
func Serve(engine string, input io.Reader, output io.Writer) error {
	req, err := decode(input)
	// Invalid requests never construct a runtime adapter.
	if err != nil {
		return err
	}
	response := handle(engine, req)
	response.Version = 1
	return json.NewEncoder(output).Encode(response)
}

// decode rejects oversized, trailing, or unsupported requests.
func decode(input io.Reader) (resourceprotocol.Request, error) {
	var req resourceprotocol.Request
	decoder := json.NewDecoder(io.LimitReader(input, resourceprotocol.MaxWireBytes+1))
	decoder.DisallowUnknownFields()
	// Exactly one versioned request is accepted.
	if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF || req.Version != 1 || len(req.Data) > resourceprotocol.MaxBackupBytes {
		return req, errors.New("invalid resource request")
	}
	return req, nil
}

// handle holds the shared metadata lock throughout the engine operation.
func handle(engine string, req resourceprotocol.Request) resourceprotocol.Response {
	file, err := lock(req.Backend.StateDir)
	// State must be exclusively held across package processes.
	if err != nil {
		return failure(err)
	}
	defer file.Close()
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return execute(engine, req)
}

// lock protects existing shared engine metadata against concurrent package writers.
func lock(directory string) (*os.File, error) {
	// Runtime state is always selected by the controller.
	if directory == "" {
		return nil, errors.New("missing resource state directory")
	}
	// Create only metadata directories before planning.
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, ".resource-package.lock"), os.O_CREATE|os.O_RDWR, 0600)
	// Require an open lock file before acquiring its advisory lock.
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
	// Failed lock acquisition must not retain a descriptor.
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// execute validates engine authority before constructing its adapter.
func execute(engine string, req resourceprotocol.Request) resourceprotocol.Response {
	// Provision and plan must target the executable's fixed engine.
	if req.Spec.Type != "" && req.Spec.Type != engine {
		return failure(model.Err("RESOURCE_ENGINE", "Resource package engine mismatch"))
	}
	manager, err := resources.New(resources.Options{Type: req.Backend.Type, StateDir: req.Backend.StateDir, SocketPath: req.Backend.SocketPath, Kubeconfig: req.Backend.Kubeconfig})
	// Invalid backend configuration cannot reach an engine.
	if err != nil {
		return failure(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	return dispatch(ctx, manager, engine, req)
}

// dispatch preserves engine ownership checks and strips broker-only approval metadata.
func dispatch(ctx context.Context, m model.ResourceManager, engine string, r resourceprotocol.Request) resourceprotocol.Response {
	r.Spec.PackageDigest = ""
	// Existing instance operations cannot cross executable engine boundaries.
	if r.Operation != "plan" && r.Operation != "provision" {
		item, err := m.Status(ctx, r.Name)
		// Refuse operations when owned engine state cannot be observed.
		if err != nil {
			return failure(err)
		}
		// Reject requests dispatched to a different fixed engine.
		if item.Type != engine {
			return failure(model.Err("RESOURCE_ENGINE", "Resource package engine mismatch"))
		}
	}
	result, err := operation(ctx, m, r)
	// Only safe structured failures cross the private protocol boundary.
	if err != nil {
		return failure(err)
	}
	return result
}

// operation dispatches read and lifecycle resource requests.
func operation(ctx context.Context, m model.ResourceManager, r resourceprotocol.Request) (v resourceprotocol.Response, err error) {
	// Retain safe engine progress for replay by the controller.
	record := func(event model.Event) { v.Events = append(v.Events, event) }
	c := model.ResourceContext{Credentials: r.Credentials, OperationID: r.OperationID, Purge: r.Purge, OnEvent: record}
	// Every operation has an explicit fixed adapter method.
	switch r.Operation {
	// Dispatch only this explicitly supported operation.
	case "plan":
		v.Plan, err = m.Plan(ctx, r.Spec)
	// Dispatch only this explicitly supported operation.
	case "provision":
		v.Resource, err = m.Provision(ctx, r.Spec, c)
	// Dispatch only this explicitly supported operation.
	case "action":
		err = m.Action(ctx, r.Name, r.Action, c)
	// Dispatch only this explicitly supported operation.
	default:
		return observation(ctx, m, r, c)
	}
	return
}

// observation dispatches bounded observation and snapshot operations.
func observation(ctx context.Context, m model.ResourceManager, r resourceprotocol.Request, c model.ResourceContext) (v resourceprotocol.Response, err error) {
	// Snapshot capability is implemented by both supported backends.
	switch r.Operation {
	// Dispatch only this explicitly supported operation.
	case "evidence", "fence":
		return recoveryObservation(ctx, m, r, c)
	// Handle only this supported recovery operation or retained state.
	case "status":
		v.Resource, err = m.Status(ctx, r.Name)
	// Dispatch only this explicitly supported operation.
	case "connection":
		v.Connection, err = m.Connection(ctx, r.Name, r.Credentials)
	// Dispatch only this explicitly supported operation.
	case "logs":
		v.Logs, err = m.Logs(ctx, r.Name)
	// Dispatch only this explicitly supported operation.
	case "backup":
		v.Data, err = m.(backupManager).Backup(ctx, r.Name, c)
	// Dispatch only this explicitly supported operation.
	case "plan-resize", "resize":
		return allocationObservation(ctx, m, r, c)
	// Dispatch only this explicitly supported operation.
	case "restore":
		err = m.(backupManager).Restore(ctx, r.Name, r.Data, c)
	// Dispatch only this explicitly supported operation.
	default:
		err = model.Err("RESOURCE_OPERATION", "Unsupported resource operation")
	}
	return
}

// failure exposes only structured engine failures, never filesystem diagnostics.
func failure(err error) resourceprotocol.Response {
	var safe *model.Error
	// Unknown errors may contain credentials or private runtime output.
	if !errors.As(err, &safe) {
		safe = &model.Error{Code: "RESOURCE_PACKAGE", Message: "Resource package operation failed"}
	}
	return resourceprotocol.Response{Error: safe}
}

// recoveryObservation carries native verification and exclusive-writer fencing through the package boundary.
func recoveryObservation(ctx context.Context, m model.ResourceManager, r resourceprotocol.Request, c model.ResourceContext) (v resourceprotocol.Response, err error) {
	capability, ok := m.(model.DatabaseRecoveryManager)
	// Reject this request: Native recovery verification is unavailable.
	if !ok {
		return v, model.Err("RECOVERY_UNSUPPORTED", "Native recovery verification is unavailable")
	}
	// Advance only after the required recovery invariant holds.
	if r.Operation == "evidence" {
		v.Evidence, err = capability.Evidence(ctx, r.Name, c)
	} else {
		err = capability.Fence(ctx, r.Name, r.Fenced, c)
	}
	return
}

// allocationObservation keeps optional allocation capability checks separate from native recovery operations.
func allocationObservation(ctx context.Context, m model.ResourceManager, r resourceprotocol.Request, c model.ResourceContext) (v resourceprotocol.Response, err error) {
	capability, ok := m.(model.ResourceResizeManager)
	// Reject this request: Resource backend does not support allocation maintenance.
	if !ok {
		return v, model.Err("UNSUPPORTED_OPERATION", "Resource backend does not support allocation maintenance")
	}
	// Advance only after the required recovery invariant holds.
	if r.Operation == "plan-resize" {
		v.Plan, err = capability.PlanResize(ctx, r.Spec)
	} else {
		v.Resource, err = capability.Resize(ctx, r.Spec, c)
	}
	return
}
