// Package model contains the shared CLI/controller/runtime contracts.
package model

import "hostlier/pkg/workflow"

// ServerConfig contains only server-wide authorized domains and the configuration version.
type ServerConfig struct {
	Version int      `json:"version" yaml:"version"`
	Domains []string `json:"domains" yaml:"domains"`
}

// Health defines the HTTP readiness probe used before routing an application.
type Health struct {
	Exec           []string `json:"exec,omitempty" yaml:"exec,omitempty"`
	Path           string   `json:"path" yaml:"path"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
}

// Limits declares the per-container CPU and memory allocation.
type Limits struct {
	CPU    string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`
}

// Volume declares a persistent mount and its requested storage capacity.
type Volume struct {
	Mount string `json:"mount" yaml:"mount"`
	Size  string `json:"size" yaml:"size"`
}

// Proxy controls whether the deployment route prefix reaches the application.
type Proxy struct {
	StripPrefix bool `json:"stripPrefix" yaml:"stripPrefix"`
}

// Dependency selects an existing resource or declares an approved provisioned instance.
type Dependency struct {
	Type     string `json:"type" yaml:"type"`
	Source   string `json:"source" yaml:"source"`
	Storage  string `json:"storage,omitempty" yaml:"storage,omitempty"`
	Instance string `json:"instance,omitempty" yaml:"instance,omitempty"`
}

// Project is the shared .hostlier deployment contract used by CLI, controller and runtimes.
type Project struct {
	// Packages holds per-package authoring settings; the CLI resolves each section against that package’s contract.
	Packages map[string]map[string]any `yaml:"packages,omitempty" json:"-"`
	// Package selects local CLI hooks and is never sent to the controller.
	Package         string                `yaml:"package,omitempty" json:"-"`
	Fused           *FusedConnection      `json:"fused,omitempty" yaml:"fused,omitempty"`
	Security        *ContainerSecurity    `json:"security,omitempty" yaml:"security,omitempty"`
	Endpoints       map[string]Endpoint   `json:"endpoints,omitempty" yaml:"endpoints,omitempty"`
	PrimaryEndpoint string                `json:"primaryEndpoint,omitempty" yaml:"primaryEndpoint,omitempty"`
	Configs         map[string]ConfigFile `json:"configs,omitempty" yaml:"configs,omitempty"`
	Requests        *Limits               `json:"requests,omitempty" yaml:"requests,omitempty"`
	SleepAfter      string                `json:"sleepAfter,omitempty" yaml:"sleepAfter,omitempty"`
	Autoscaling     *Autoscaling          `json:"autoscaling,omitempty" yaml:"autoscaling,omitempty"`
	NamespaceImage  string                `json:"namespaceImage,omitempty" yaml:"namespaceImage,omitempty"`
	Source          *Source               `json:"source,omitempty" yaml:"source,omitempty"`
	Workload        string                `json:"workload,omitempty" yaml:"workload,omitempty"`
	Endpoint        *Endpoint             `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Sidecars        map[string]Project    `json:"sidecars,omitempty" yaml:"sidecars,omitempty"`
	Command         []string              `json:"command,omitempty" yaml:"command,omitempty"`
	Args            []string              `json:"args,omitempty" yaml:"args,omitempty"`
	Shutdown        *Shutdown             `json:"shutdown,omitempty" yaml:"shutdown,omitempty"`
	Required        *bool                 `json:"required,omitempty" yaml:"required,omitempty"`

	Replicas     *int                  `json:"replicas,omitempty" yaml:"replicas,omitempty"`
	Workflow     *workflow.Spec        `json:"workflow,omitempty" yaml:"workflow,omitempty"`
	Version      int                   `json:"version" yaml:"version"`
	Name         string                `json:"name" yaml:"name"`
	Image        string                `json:"image" yaml:"image"`
	Port         int                   `json:"port" yaml:"port"`
	Route        string                `json:"route,omitempty" yaml:"route,omitempty"`
	Health       *Health               `json:"health,omitempty" yaml:"health,omitempty"`
	Resources    *Limits               `json:"resources,omitempty" yaml:"resources,omitempty"`
	Env          map[string]string     `json:"env,omitempty" yaml:"env,omitempty"`
	Secrets      map[string]string     `json:"secrets,omitempty" yaml:"secrets,omitempty"`
	Volumes      map[string]Volume     `json:"volumes,omitempty" yaml:"volumes,omitempty"`
	Proxy        *Proxy                `json:"proxy,omitempty" yaml:"proxy,omitempty"`
	Dependencies map[string]Dependency `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
	Bindings     map[string]string     `json:"bindings,omitempty" yaml:"bindings,omitempty"`
}

// Route is a normalized public address and route-conflict identity.
type Route struct {
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
	URL      string `json:"url"`
	Key      string `json:"key"`
}

// DesiredReplicas preserves explicit zero and supplies the omitted default.
func DesiredReplicas(p Project) int {
	// Omitted replica counts retain the single-instance default.
	if p.Replicas == nil {
		return 1
	}
	return *p.Replicas
}
