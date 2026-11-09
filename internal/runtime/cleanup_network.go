package runtime

import (
	"context"
	"hostlier/internal/model"
	"net/url"
)

// cleanGroupNetwork removes only an empty, owned failed application's network after every member is gone.
func (r *Runtime) cleanGroupNetwork(ctx context.Context, job failedGroupCleanup, protected map[string]bool) error {
	// Running and retained applications keep their stable networking.
	if job.Network == "" || protected["network:"+job.App] {
		return nil
	}
	// A journal never authorizes a shared provider or arbitrary host network.
	if job.Network != r.applicationNetwork(job.App) {
		return model.Err("OWNERSHIP", "Cleanup network identity mismatch")
	}
	var network struct {
		Labels     map[string]string
		Containers map[string]any
	}
	path := "/networks/" + url.PathEscape(job.Network)
	err := r.api(ctx, "GET", path, nil, &network, nil)
	// Network removal is idempotent after successful container cleanup.
	if hasCode(err, "HTTP_404") {
		return nil
	}
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil {
		return err
	}
	// Never disconnect consumers or delete networks with unknown ownership.
	if !r.owned(network.Labels, job.App) || len(network.Containers) > 0 {
		return model.Err("CLEANUP_PENDING", "Owned network still referenced or unavailable")
	}
	return r.api(ctx, "DELETE", path, nil, nil, nil)
}
