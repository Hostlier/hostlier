package resources

import (
	"context"
	"hostlier/internal/model"
	"net/url"
)

// Fence persists workflow ownership and disables restart before moving a database onto its own isolated network.
func (m *Manager) Fence(ctx context.Context, name string, fenced bool, c model.ResourceContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.read()
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	item, ok := state[name]
	// Reject this request: Resource fence belongs to another workflow.
	if !ok || c.OperationID == "" || (item.RecoveryFence != "" && item.RecoveryFence != c.OperationID) {
		return model.Err("RECOVERY_FENCE", "Resource fence belongs to another workflow")
	}
	current, err := m.inspect(ctx, name)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = m.checkRecoveryPorts(current); err != nil {
		return err
	}
	// Advance only after the required recovery invariant holds.
	if !fenced {
		return m.releaseFence(ctx, item, current, c, state)
	}
	return m.applyRecoveryFence(ctx, item, current, c, state)
}

// applyRecoveryFence persists ownership and disables automatic restarts before detaching client networks.
func (m *Manager) applyRecoveryFence(ctx context.Context, item record, current container, c model.ResourceContext, state map[string]record) error {
	name := item.Name
	var err error
	item.RecoveryFence = c.OperationID
	state[name] = item
	// Propagate failure before performing the next recovery side effect.
	if err = m.save(state); err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if _, err = m.api(ctx, "POST", "/containers/"+current.ID+"/update", map[string]any{"RestartPolicy": map[string]string{"Name": "no"}}, nil); err != nil {
		return err
	}
	return m.moveToRecoveryNetwork(ctx, item, current)
}

// moveToRecoveryNetwork permits native maintenance on a network containing only the selected database.
func (m *Manager) moveToRecoveryNetwork(ctx context.Context, item record, current container) error {
	name := item.Name
	var err error
	quarantine := m.network(name) + "-recovery"
	// Propagate failure before performing the next recovery side effect.
	if err = m.ensure(ctx, "networks", name, quarantine, map[string]any{"Name": quarantine, "Driver": "bridge", "Internal": true, "Labels": m.labels(name)}); err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if err = m.checkRecoveryNetwork(ctx, quarantine, current.ID); err != nil {
		return err
	}
	// Advance only after the required recovery invariant holds.
	if _, ok := current.NetworkSettings.Networks[quarantine]; !ok {
		// Propagate failure before performing the next recovery side effect.
		if err = m.attachRecoveryNetwork(ctx, quarantine, current.ID, nil); err != nil {
			return err
		}
	}
	// Propagate failure before performing the next recovery side effect.
	if err = m.detachClientNetworks(ctx, current, quarantine); err != nil {
		return err
	}
	checked, err := m.inspect(ctx, name)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	// Reject this request: Database network isolation could not be verified.
	if _, ok := checked.NetworkSettings.Networks[quarantine]; !ok || len(checked.NetworkSettings.Networks) != 1 {
		return model.Err("RECOVERY_FENCE", "Database network isolation could not be verified")
	}
	return m.checkRecoveryNetwork(ctx, quarantine, current.ID)
}

// checkRecoveryNetwork rejects a shared or externally routed quarantine network.
func (m *Manager) checkRecoveryNetwork(ctx context.Context, name, id string) error {
	var network struct {
		Internal   bool
		Containers map[string]any
	}
	// Propagate failure before performing the next recovery side effect.
	if _, err := m.api(ctx, "GET", "/networks/"+url.PathEscape(name), nil, &network); err != nil {
		return err
	}
	// Reject this request: Recovery network is not isolated.
	if !network.Internal {
		return model.Err("RECOVERY_FENCE", "Recovery network is not isolated")
	}
	// Inspect each retained item before advancing recovery state.
	for container := range network.Containers {
		// Reject this request: Recovery network has another attached container.
		if container != id {
			return model.Err("RECOVERY_FENCE", "Recovery network has another attached container")
		}
	}
	return nil
}

// attachRecoveryNetwork connects the selected owned container without exposing a host port.
func (m *Manager) attachRecoveryNetwork(ctx context.Context, network, id string, aliases []string) error {
	body := map[string]any{"Container": id, "EndpointConfig": map[string]any{"Aliases": aliases}}
	_, err := m.api(ctx, "POST", "/networks/"+url.PathEscape(network)+"/connect", body, nil)
	return err
}

// disconnectRecoveryNetwork tears down existing sessions on the old interface.
func (m *Manager) disconnectRecoveryNetwork(ctx context.Context, network, id string) error {
	_, err := m.api(ctx, "POST", "/networks/"+url.PathEscape(network)+"/disconnect", map[string]any{"Container": id, "Force": true}, nil)
	return err
}

// releaseFence reconnects only the owned network after a separate human cutover or abort decision.
func (m *Manager) releaseFence(ctx context.Context, item record, current container, c model.ResourceContext, state map[string]record) error {
	// Advance only after the required recovery invariant holds.
	if recoveryAlreadyReleased(item, current) {
		return nil
	}
	// Reject this request: No matching recovery fence.
	if item.RecoveryFence != c.OperationID {
		return model.Err("RECOVERY_FENCE", "No matching recovery fence")
	}
	// Advance only after the required recovery invariant holds.
	if _, ok := current.NetworkSettings.Networks[item.Network]; !ok {
		// Propagate failure before performing the next recovery side effect.
		if err := m.attachRecoveryNetwork(ctx, item.Network, current.ID, []string{m.container(item.Name)}); err != nil {
			return err
		}
	}
	quarantine := m.network(item.Name) + "-recovery"
	// Advance only after the required recovery invariant holds.
	if _, ok := current.NetworkSettings.Networks[quarantine]; ok {
		// Propagate failure before performing the next recovery side effect.
		if err := m.disconnectRecoveryNetwork(ctx, quarantine, current.ID); err != nil {
			return err
		}
	}
	// Propagate failure before performing the next recovery side effect.
	if err := m.deleteRecoveryNetwork(ctx, quarantine, item.Name); err != nil {
		return err
	}
	// Propagate failure before performing the next recovery side effect.
	if _, err := m.api(ctx, "POST", "/containers/"+current.ID+"/update", map[string]any{"RestartPolicy": map[string]string{"Name": "unless-stopped"}}, nil); err != nil {
		return err
	}
	item.RecoveryFence = ""
	state[item.Name] = item
	return m.save(state)
}

// deleteRecoveryNetwork removes only the empty, explicitly owned recovery network.
func (m *Manager) deleteRecoveryNetwork(ctx context.Context, name, resource string) error {
	var network struct {
		Labels     map[string]string
		Containers map[string]any
		Internal   bool
	}
	_, err := m.api(ctx, "GET", "/networks/"+url.PathEscape(name), nil, &network)
	// Propagate failure before performing the next recovery side effect.
	if code(err, "HTTP_404") {
		return nil
	}
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return err
	}
	// Reject this request: Recovery network is not empty and exclusively owned.
	if !network.Internal || !m.owned(network.Labels, resource) || len(network.Containers) != 0 {
		return model.Err("RECOVERY_FENCE", "Recovery network is not empty and exclusively owned")
	}
	_, err = m.api(ctx, "DELETE", "/networks/"+url.PathEscape(name), nil, nil)
	return err
}

// checkRecoveryPorts rejects published ports that would bypass private-network fencing.
func (m *Manager) checkRecoveryPorts(current container) error {
	// Inspect each retained item before advancing recovery state.
	for _, bindings := range current.NetworkSettings.Ports {
		// Reject this request: Published database ports prevent safe fencing.
		if bindings != nil {
			return model.Err("RECOVERY_FENCE", "Published database ports prevent safe fencing")
		}
	}
	return nil
}

// detachClientNetworks closes every original client interface while preserving only isolated maintenance access.
func (m *Manager) detachClientNetworks(ctx context.Context, current container, quarantine string) error {
	var err error
	// Inspect each retained item before advancing recovery state.
	for network := range current.NetworkSettings.Networks {
		// Advance only after the required recovery invariant holds.
		if network != quarantine {
			// Propagate failure before performing the next recovery side effect.
			if err = m.disconnectRecoveryNetwork(ctx, network, current.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// recoveryAlreadyReleased makes interrupted finalization retryable only on the exact original network.
func recoveryAlreadyReleased(item record, current container) bool {
	_, ok := current.NetworkSettings.Networks[item.Network]
	return item.RecoveryFence == "" && ok && len(current.NetworkSettings.Networks) == 1
}
