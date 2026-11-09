package model

import "context"

// ResourceSpec describes an instance identity, engine and requested persistent storage.
type ResourceSpec struct {
	PackageDigest string `json:"packageDigest,omitempty"`
	CPU           string `json:"cpu,omitempty"`
	Memory        string `json:"memory,omitempty"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Storage       string `json:"storage"`
	Image         string `json:"image,omitempty"`
}

// ResourceCredentials holds transient engine authentication values kept encrypted at rest.
type ResourceCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
}

// ResourcePlan pairs a validated resource specification with its approval preview.
type ResourcePlan struct {
	Spec    ResourceSpec `json:"spec"`
	Changes any          `json:"changes"`
}

// Resource is the runtime-neutral identity and private connectivity of an instance.
type Resource struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	RuntimeID string `json:"runtimeId"`
	Status    string `json:"status"`
	Network   string `json:"network,omitempty"`
}

// ResourceContext carries credentials and approved lifecycle authority to the resource adapter.
type ResourceContext struct {
	PackageDigest string
	Credentials   ResourceCredentials
	OperationID   string
	OnEvent       func(Event)
	Purge         bool
}

// ResourceManager never stores plaintext credentials in general resource records.
type ResourceManager interface {
	// Name identifies the resource backend paired with an application runtime.
	Name() string
	// Plan validates resource intent and describes the proposed changes.
	Plan(context.Context, ResourceSpec) (ResourcePlan, error)
	// Provision creates or resumes the owned instance using supplied credentials.
	Provision(context.Context, ResourceSpec, ResourceContext) (Resource, error)
	// Action applies lifecycle or explicitly authorized purge behavior.
	Action(context.Context, string, string, ResourceContext) error
	// Status observes the named owned instance.
	Status(context.Context, string) (Resource, error)
	// Connection returns transient application bindings using scoped credentials.
	Connection(context.Context, string, ResourceCredentials) (map[string]string, error)
	// Logs returns instance output for controller-side redaction.
	Logs(context.Context, string) (string, error)
}
