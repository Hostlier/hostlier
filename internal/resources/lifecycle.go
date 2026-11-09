package resources

import (
	"context"
	"net"
	"net/url"

	"hostlier/internal/model"
)

// Status reports the observed state of the selected owned resource.
func (m *Manager) Status(ctx context.Context, name string) (model.Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.find(name)
	// Require an existing resource record before operating on engine state.
	if err != nil {
		return model.Resource{}, err
	}
	current, err := m.inspect(ctx, name)
	// Handle absent engine objects through the resource reconciliation path.
	if code(err, "HTTP_404") {
		item.Status = "deleted"
		return item.Resource, nil
	}
	// Require a verified owned container before changing or reporting its state.
	if err != nil {
		return model.Resource{}, err
	}
	item.RuntimeID = current.ID
	// Report running or stopped from the observed engine container state.
	if current.State.Running {
		item.Status = "running"
	} else {
		item.Status = "stopped"
	}
	return item.Resource, nil
}

// Connection returns a private engine connection after validating its credentials.
func (m *Manager) Connection(ctx context.Context, name string, c model.ResourceCredentials) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.find(name)
	// Require an existing resource record before operating on engine state.
	if err != nil {
		return nil, err
	}
	// Validate engine credentials before issuing authenticated commands.
	if err = validateCredentials(item.Type, c); err != nil {
		return nil, err
	}
	host := m.container(name)
	// Return the Bolt URI and separate Neo4j credentials expected by clients.
	if item.Type == "neo4j" {
		return map[string]string{"uri": "bolt://" + host + ":7687", "username": c.Username, "password": c.Password}, nil
	}
	scheme, port, path := "postgresql", "5432", "/"+c.Database
	// Build a Redis URL using its native port and default database index.
	if item.Type == "redis" {
		scheme = "redis"
		port = "6379"
		path = "/0"
	}
	address := url.URL{Scheme: scheme, User: url.UserPassword(c.Username, c.Password), Host: net.JoinHostPort(host, port), Path: path}
	return map[string]string{"url": address.String()}, nil
}

// Action performs a lifecycle action on the selected owned resource.
func (m *Manager) Action(ctx context.Context, name, action string, operation model.ResourceContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Reject this request: Unsupported resource action.
	if !validResourceAction(action) {
		return model.Err("RESOURCE_ACTION", "Unsupported resource action")
	}
	state, err := m.read()
	// Require valid persisted resource state before looking up or changing records.
	if err != nil {
		return err
	}
	item, ok := state[name]
	// Reject this request: Resource not found.
	if !ok {
		return model.Err("RESOURCE_NOT_FOUND", "Resource not found")
	}
	// Reject this request: Database is reserved by a recovery workflow.
	if item.RecoveryFence != "" && item.RecoveryFence != operation.OperationID {
		return model.Err("RECOVERY_FENCE", "Database is reserved by a recovery workflow")
	}
	current, err := m.inspect(ctx, name)
	// Require a verified owned container before changing or reporting its state.
	if err != nil && !(action == "delete" && code(err, "HTTP_404")) {
		return err
	}
	// Delete owned workload state only for the explicit delete action.
	if action == "delete" {
		return m.deleteResource(ctx, name, current, item, state, operation)
	}
	return m.changeResourceState(ctx, name, action, current, item, state, operation)
}

// Logs reads diagnostics from the selected resource workload.
func (m *Manager) Logs(ctx context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.inspect(ctx, name)
	// Require a verified owned container before changing or reporting its state.
	if err != nil {
		return "", err
	}
	data, err := m.api(ctx, "GET", "/containers/"+current.ID+"/logs?stdout=true&stderr=true&tail=100", nil, nil)
	// Require the owned Docker API request to complete before using its result.
	if err != nil {
		return "", err
	}
	stdout, stderr, err := demultiplex(data)
	// Reject malformed Docker stream framing before using engine logs.
	if err != nil {
		return "", err
	}
	text := string(stdout) + string(stderr)
	// Bound returned diagnostics to the final 64 KiB of logs.
	if len(text) > 65536 {
		text = text[len(text)-65536:]
	}
	return text, nil
}

// deleteResource removes the owned engine while retaining storage unless purge was approved.
func (m *Manager) deleteResource(ctx context.Context, name string, current container, item record, state map[string]record, operation model.ResourceContext) error {
	var err error

	// Delete a workload container only when an existing identity was observed.
	if current.ID != "" {
		// Require the owned Docker API request to complete before using its result.
		if _, err = m.api(ctx, "DELETE", "/containers/"+current.ID+"?force=true", nil, nil); err != nil {
			return err
		}
	}
	// Advance only after the required recovery invariant holds.
	if item.RecoveryFence != "" {
		// Propagate failure before performing the next recovery side effect.
		if err = m.deleteRecoveryNetwork(ctx, m.network(name)+"-recovery", name); err != nil {
			return err
		}
	}
	item.Status = "deleted"
	state[name] = item
	// Remove persistent data only for an explicitly requested purge.
	if operation.Purge {
		// Require explicit data purge to complete before removing retained metadata.
		if err = m.purgeResource(ctx, name, item); err != nil {
			return err
		}
		delete(state, name)
	}
	return m.save(state)
}

// purgeResource checks volume ownership before removing retained storage and its network.
func (m *Manager) purgeResource(ctx context.Context, name string, item record) error {
	var err error

	var volume struct{ Labels map[string]string }
	_, err = m.api(ctx, "GET", "/volumes/"+url.PathEscape(item.Volume), nil, &volume)
	// Require the owned Docker API request to complete before using its result.
	if err != nil && !code(err, "HTTP_404") {
		return err
	}
	// Check ownership and delete the data volume only when it still exists.
	if err == nil {
		// Reject this request: Refusing to purge unowned volume.
		if !m.owned(volume.Labels, name) {
			return model.Err("RESOURCE_OWNERSHIP", "Refusing to purge unowned volume")
		}
		// Require the owned Docker API request to complete before using its result.
		if _, err = m.api(ctx, "DELETE", "/volumes/"+url.PathEscape(item.Volume), nil, nil); err != nil {
			return err
		}
	}
	// Require the owned Docker API request to complete before using its result.
	if _, err = m.api(ctx, "DELETE", "/networks/"+url.PathEscape(item.Network), nil, nil); err != nil && !code(err, "HTTP_404") {
		return err
	}

	return nil
}

// validResourceAction restricts mutations to supported engine lifecycle operations.
func validResourceAction(action string) bool {
	return action == "start" || action == "stop" || action == "restart" || action == "delete"
}

// changeResourceState requires credentials for starting engines and checks authenticated readiness.
func (m *Manager) changeResourceState(ctx context.Context, name, action string, current container, item record, state map[string]record, operation model.ResourceContext) error {
	var err error

	// Start the engine unless the requested final state is stopped.
	if action != "stop" {
		// Validate engine credentials before issuing authenticated commands.
		if err = validateCredentials(item.Type, operation.Credentials); err != nil {
			return err
		}
	}
	suffix := ""
	// Stop the engine before restart or an explicit stop.
	if action != "start" {
		suffix = "?t=30"
	}
	// Require the owned Docker API request to complete before using its result.
	if _, err = m.api(ctx, "POST", "/containers/"+current.ID+"/"+action+suffix, nil, nil); err != nil {
		return err
	}
	item.Status = "stopped"
	// Start the engine unless the requested final state is stopped.
	if action != "stop" {
		// Require authenticated readiness before reporting the lifecycle operation complete.
		if err = m.ready(ctx, item.Spec, operation.Credentials); err != nil {
			return err
		}
		item.Status = "running"
	}
	state[name] = item
	return m.save(state)
}
