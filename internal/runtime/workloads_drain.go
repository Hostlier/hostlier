package runtime

import (
	"context"
	"hostlier/internal/model"
)

// retiredGroup retains exact source member identities until a committed cutover finishes draining.
type retiredGroup struct {
	Anchor  string            `json:"anchor,omitempty"`
	Members map[string]string `json:"members,omitempty"`
	Spec    model.Project     `json:"spec"`
}

// sourceGroup distinguishes a fresh cutover from a replay of an already committed operation.
func (d *dockerDeployment) sourceGroup() *retiredGroup {
	// A replay must never mistake current sidecars for its retained source members.
	if d.old.OperationID == d.operation || d.old.PreviousRuntimeID != "" {
		return d.old.PreviousGroup
	}
	// Version one has no independently tracked group processes.
	if d.old.Spec.Version != 2 {
		return nil
	}
	return &retiredGroup{Anchor: d.old.Anchor, Members: d.old.Members, Spec: d.old.Spec}
}

// stopSourceGroup retires only the recorded source members after their primary has drained.
func (d *dockerDeployment) stopSourceGroup(ctx context.Context) error {
	group := d.sourceGroup()
	// Legacy sources have no additional processes to stop.
	if group == nil {
		return nil
	}
	app := appRecord{Deployment: model.Deployment{Name: d.spec.Name}, Members: group.Members, Spec: group.Spec}
	// Sidecars get their individual approved grace periods after the source primary stops.
	if err := d.runtime.memberAction(ctx, app, "stop"); err != nil {
		return err
	}
	// A missing anchor is valid only for a legacy retained record.
	if group.Anchor == "" {
		return nil
	}
	return d.runtime.groupContainerAction(ctx, d.spec.Name, group.Anchor, "stop", 1)
}
