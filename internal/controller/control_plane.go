package controller

import (
	"context"

	"hostlier/internal/model"
)

// ControlPlaneMode identifies a controller that delegates all workload execution.
const ControlPlaneMode = "control-plane"

// NewControlPlane constructs a fleet controller without a local execution backend.
func NewControlPlane(o Options) (*Controller, error) {
	o.Runtime = controlPlaneRuntime{}
	return New(o)
}

type controlPlaneRuntime struct{}

func (controlPlaneRuntime) Name() string { return ControlPlaneMode }

func (controlPlaneRuntime) InspectImage(context.Context, string) (model.ImageInfo, error) {
	return model.ImageInfo{}, controlPlaneWorkloadError()
}
func (controlPlaneRuntime) Deploy(context.Context, model.Project, model.DeployContext) (model.Deployment, error) {
	return model.Deployment{}, controlPlaneWorkloadError()
}
func (controlPlaneRuntime) Action(context.Context, string, string, model.ActionContext) error {
	return controlPlaneWorkloadError()
}
func (controlPlaneRuntime) Logs(context.Context, string) (string, error) {
	return "", controlPlaneWorkloadError()
}
func (controlPlaneRuntime) Doctor(context.Context) (any, error) {
	return map[string]string{"runtime": ControlPlaneMode}, nil
}

func (c *Controller) controlPlaneOnly() bool { return c.opts.Runtime.Name() == ControlPlaneMode }

func (c *Controller) publicOrigin() string {
	if c.opts.PublicOrigin != "" {
		return c.opts.PublicOrigin
	}
	return "https://" + c.state.Hostname
}

func controlPlaneWorkloadError() error {
	return model.Err("CONTROLLER_MODE", "Select an enrolled worker with --on HOST to run workloads")
}

// checkControlPlaneRoute keeps local operations restricted to fleet and controller administration.
func (c *Controller) checkControlPlaneRoute(resource string) error {
	if !c.controlPlaneOnly() {
		return nil
	}
	switch resource {
	case "access", "approvals", "authorizations", "clients", "doctor", "domains", "email",
		"fleet-joins", "hosts", "human", "logs", "pairing", "status", "swarms":
		return nil
	default:
		return controlPlaneWorkloadError()
	}
}
