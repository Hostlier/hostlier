package resources

import (
	"context"
	"hostlier/internal/model"
	"strings"
)

// VerifyWorkflow observes owned native state and authenticates approved credentials without provisioning.
func (m *Manager) VerifyWorkflow(ctx context.Context, name string, c model.ResourceCredentials) error {
	item, err := m.Status(ctx, name)
	// Missing, stopped, or unowned workloads cannot establish completion.
	if err != nil {
		return err
	}
	// Only live engines can prove their protected credentials.
	if item.Status != "running" {
		return model.Err("RESOURCE_READINESS", "Resource is not running")
	}
	out, err := m.execute(ctx, name, readinessCommand(item.Type, c), authEnv(item.Type, c))
	return verifiedProtocol(item.Type, out, err)
}

// VerifyWorkflow authenticates the owned Kubernetes engine without applying manifests.
func (m *K3sManager) VerifyWorkflow(ctx context.Context, name string, c model.ResourceCredentials) error {
	_, err := m.Status(ctx, name)
	// Ownership must be established before running authenticated health checks.
	if err != nil {
		return err
	}
	item, err := m.find(name)
	// Persistent metadata must agree with the owned observed workload.
	if err != nil {
		return err
	}
	out, err := m.protocol(ctx, item, c)
	return verifiedProtocol(item.Type, out, err)
}

// verifiedProtocol permits publication after one current authenticated observation, without readiness polling.
func verifiedProtocol(engine string, out []byte, err error) error {
	// Failed authentication or unavailable engines cannot establish completed ownership.
	if err != nil {
		return err
	}
	// Redis must actually acknowledge the authenticated health command.
	if engine == "redis" && strings.TrimSpace(string(out)) != "PONG" {
		return model.Err("RESOURCE_READINESS", "Resource did not authenticate")
	}
	return nil
}
