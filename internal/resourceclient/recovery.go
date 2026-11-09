package resourceclient

import (
	"context"
	"hostlier/internal/model"
	"hostlier/pkg/resourceprotocol"
)

// Evidence obtains native restore verification through the isolated engine package.
func (m *Manager) Evidence(ctx context.Context, name string, c model.ResourceContext) (model.DatabaseEvidence, error) {
	v, err := m.namedApproved(ctx, name, request("evidence", name, model.ResourceSpec{}, c), c.PackageDigest)
	return v.Evidence, err
}

// Fence carries the retained workflow identity through the private package channel.
func (m *Manager) Fence(ctx context.Context, name string, fenced bool, c model.ResourceContext) error {
	r := request("fence", name, model.ResourceSpec{}, c)
	r.Fenced = fenced
	_, err := m.namedApproved(ctx, name, r, c.PackageDigest)
	return err
}

// namedApproved preserves reviewed executable identity for recovery and lifecycle mutations.
func (m *Manager) namedApproved(ctx context.Context, name string, r resourceprotocol.Request, pin string) (resourceprotocol.Response, error) {
	engine, err := m.engine(name)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return resourceprotocol.Response{}, err
	}
	value, _, err := m.call(ctx, engine, pin, r)
	return value, err
}
