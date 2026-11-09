package model

import "context"

// ImageInfo records the immutable digest resolved during planning and any runtime warnings.
type ImageInfo struct {
	Image    string   `json:"image"`
	Digest   string   `json:"digest"`
	Warnings []string `json:"warnings"`
}

// Event reports runtime progress for controller-side sanitization.
type Event struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Step    string `json:"step,omitempty"`
}

// Deployment is the runtime-neutral result retained after application activation.
type Deployment struct {
	Endpoints   map[string]string `json:"endpoints,omitempty"`
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	RuntimeID   string            `json:"runtimeId"`
	ImageDigest string            `json:"imageDigest,omitempty"`
	Status      string            `json:"status"`
}

// DeployContext carries approved ephemeral secrets, networks and recovery inputs to a runtime.
type DeployContext struct {
	OperationID        string
	Secrets            map[string]string
	DependencyNetworks []string
	OnEvent            func(Event)
	Previous           *Deployment
}

// ActionContext carries explicit lifecycle and data-retention authority to a runtime.
type ActionContext struct {
	Purge       bool
	OnEvent     func(Event)
	Application *Deployment
}

// Runtime defines the application deployment boundary implemented by Docker and k3s.
type Runtime interface {
	// Name identifies the selected execution backend.
	Name() string
	// InspectImage resolves the image used in an immutable approval plan.
	InspectImage(context.Context, string) (ImageInfo, error)
	// Deploy applies an approved project with its exact secrets and recovery context.
	Deploy(context.Context, Project, DeployContext) (Deployment, error)
	// Action performs an explicitly requested lifecycle transition.
	Action(context.Context, string, string, ActionContext) error
	// Logs returns workload output for controller-side redaction.
	Logs(context.Context, string) (string, error)
	// Doctor reports backend readiness without applying a deployment.
	Doctor(context.Context) (any, error)
}

// TunnelIngressRuntime publishes an exact, approved tunnel hostname at the owned HTTPS edge.
// Runtimes with externally managed ingress can omit this interface.
type TunnelIngressRuntime interface {
	EnsureTunnelIngress(context.Context, string) error
}
