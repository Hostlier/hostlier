package packageexec

import (
	"context"
	"errors"
	"hostlier/pkg/workflow"
	"time"
)

// Inspect observes an installed package when no earlier executable approval exists.
func (r Runner) Inspect(ctx context.Context, req workflow.Request) (workflow.Inspection, error) {
	return r.InspectPinned(ctx, req, "")
}

// InspectPinned permits recovery only through the executable that performed the approved work.
func (r Runner) InspectPinned(ctx context.Context, req workflow.Request, digest string) (workflow.Inspection, error) {
	req.Action = "inspect"
	response, _, err := r.invoke(ctx, req, digest, 30*time.Second)
	// Recovery cannot trust malformed output or a changed executable.
	if err != nil || response.Inspection == nil || response.Result != nil || response.Prepared != nil {
		return workflow.Inspection{}, errors.New("package inspection unavailable")
	}
	// Only independently verified completed results may restore private bindings.
	if !response.Inspection.Valid() {
		return workflow.Inspection{}, errors.New("invalid package inspection")
	}
	return *response.Inspection, nil
}
