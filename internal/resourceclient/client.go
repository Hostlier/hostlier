// Package resourceclient brokers resource operations to optional installed packages.
package resourceclient

import (
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"hostlier/pkg/resourceprotocol"
	"os"
	"path/filepath"
	"sync"
)

// Options selects protected package installations and stable runtime ownership state.
type Options = resourceprotocol.Backend

// Manager routes operations without linking any engine implementation.
type Manager struct {
	options Options
	mu      sync.Mutex
}

// New creates a lazy broker; resource packages are required only when called.
func New(options Options) (*Manager, error) {
	// Restored staging metadata cannot silently change existing resource ownership namespaces.
	if err := model.ValidateRecoveryLocation(options.StateDir); err != nil {
		return nil, err
	}
	// Preserve the daemon defaults without opening any runtime.
	if options.Type == "" {
		options.Type = "docker"
	}
	// Preserve the historical ownership root.
	if options.StateDir == "" {
		options.StateDir = "/var/lib/hostlier"
	}
	return &Manager{options: options}, nil
}

// Name identifies the selected runtime backend.
func (m *Manager) Name() string { return m.options.Type }

// Plan pins the installed executable digest into reviewed resource intent.
func (m *Manager) Plan(ctx context.Context, s model.ResourceSpec) (model.ResourcePlan, error) {
	s.PackageDigest = ""
	v, digest, err := m.call(ctx, s.Type, "", resourceprotocol.Request{Operation: "plan", Spec: s})
	v.Plan.Spec.PackageDigest = digest
	return v.Plan, err
}

// Provision requires exactly the executable reviewed in the approved plan.
func (m *Manager) Provision(ctx context.Context, s model.ResourceSpec, c model.ResourceContext) (model.Resource, error) {
	// Missing approval metadata cannot select an arbitrary executable.
	if s.PackageDigest == "" {
		return model.Resource{}, model.Err("RESOURCE_PACKAGE_PREFLIGHT", "Resource requires a fresh package plan")
	}
	v, _, err := m.call(ctx, s.Type, s.PackageDigest, request("provision", s.Name, s, c))
	emit(c, v.Events)
	return v.Resource, err
}

// Action forwards approved lifecycle authority through the private pipe.
func (m *Manager) Action(ctx context.Context, name, action string, c model.ResourceContext) error {
	r := request("action", name, model.ResourceSpec{}, c)
	r.Action = action
	v, err := m.namedApproved(ctx, name, r, c.PackageDigest)
	emit(c, v.Events)
	return err
}

// Status observes a retained resource through its engine package.
func (m *Manager) Status(ctx context.Context, name string) (model.Resource, error) {
	v, err := m.named(ctx, name, resourceprotocol.Request{Operation: "status", Name: name})
	return v.Resource, err
}

// Connection forwards transient credentials without persistent plaintext state.
func (m *Manager) Connection(ctx context.Context, name string, c model.ResourceCredentials) (map[string]string, error) {
	v, err := m.named(ctx, name, resourceprotocol.Request{Operation: "connection", Name: name, Credentials: c})
	return v.Connection, err
}

// Logs returns bounded output for controller-side redaction.
func (m *Manager) Logs(ctx context.Context, name string) (string, error) {
	v, err := m.named(ctx, name, resourceprotocol.Request{Operation: "logs", Name: name})
	return v.Logs, err
}

// Backup returns a bounded native engine snapshot.
func (m *Manager) Backup(ctx context.Context, name string, c model.ResourceContext) ([]byte, error) {
	v, err := m.namedApproved(ctx, name, request("backup", name, model.ResourceSpec{}, c), c.PackageDigest)
	emit(c, v.Events)
	return v.Data, err
}

// Restore forwards the approved snapshot through the private input pipe.
func (m *Manager) Restore(ctx context.Context, name string, data []byte, c model.ResourceContext) error {
	r := request("restore", name, model.ResourceSpec{}, c)
	r.Data = data
	// Restore is a separately reviewed mutation and cannot silently select current executable bytes.
	if c.PackageDigest == "" {
		return model.Err("RESOURCE_PACKAGE_PREFLIGHT", "Resource restore requires a fresh package plan")
	}
	engine, err := m.engine(name)
	// Retained runtime metadata chooses the engine, never caller-controlled restore data.
	if err != nil {
		return err
	}
	v, _, err := m.call(ctx, engine, c.PackageDigest, r)
	emit(c, v.Events)
	return err
}

// request copies only serializable lifecycle authority.
func request(op, name string, s model.ResourceSpec, c model.ResourceContext) resourceprotocol.Request {
	return resourceprotocol.Request{Operation: op, Name: name, Spec: s, Credentials: c.Credentials, OperationID: c.OperationID, Purge: c.Purge}
}

// named resolves engine identity from existing runtime-owned metadata.
func (m *Manager) named(ctx context.Context, name string, r resourceprotocol.Request) (resourceprotocol.Response, error) {
	engine, err := m.engine(name)
	// Unknown resources never select a package from caller input.
	if err != nil {
		return resourceprotocol.Response{}, err
	}
	v, _, err := m.call(ctx, engine, "", r)
	return v, err
}

// engine reads only resource identities from the stable runtime state file.
func (m *Manager) engine(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(m.options.StateDir, "resources-"+m.options.Type+".json"))
	var records map[string]model.Resource
	// Corrupt or unavailable metadata cannot authorize an engine operation.
	if err != nil || json.Unmarshal(data, &records) != nil {
		return "", model.Err("RESOURCE_NOT_FOUND", "Resource metadata unavailable")
	}
	item, ok := records[name]
	// Only the retained engine may operate on this identity.
	if !ok {
		return "", model.Err("RESOURCE_NOT_FOUND", "Resource not found")
	}
	return item.Type, nil
}

// emit replays bounded engine progress into the controller operation log.
func emit(c model.ResourceContext, events []model.Event) {
	// Callbacks are owned by the controller and never serialized to the package.
	if c.OnEvent == nil {
		return
	}
	// Engine progress is replayed only after the private operation completes.
	for _, event := range events {
		c.OnEvent(event)
	}
}
