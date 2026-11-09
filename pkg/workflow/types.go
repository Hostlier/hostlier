// Package workflow defines the optional executable package protocol and deployment workflow schema.
package workflow

import (
	"context"
	"encoding/json"
)

// Spec is a dependency graph that completes before the application's ordinary deployment.
type Spec struct {
	Product       *ProductIdentity  `json:"product,omitempty" yaml:"product,omitempty"`
	ProductPolicy *ProductPolicy    `json:"productPolicy,omitempty" yaml:"productPolicy,omitempty"`
	Steps         []Step            `json:"steps" yaml:"steps"`
	Bindings      map[string]string `json:"bindings,omitempty" yaml:"bindings,omitempty"`
}

// ProjectDeployHook is an optional controller-side package entry point. It follows the
// same reviewed plan/apply/inspect protocol as a workflow step.
const ProjectDeployHook = "project-deploy"

// Step names a server-installed package; paths and shell commands are never accepted from a project.
type Step struct {
	Hook      string            `json:"hook,omitempty" yaml:"hook,omitempty"`
	Lifecycle string            `json:"lifecycle,omitempty" yaml:"lifecycle,omitempty"`
	Phase     string            `json:"phase,omitempty" yaml:"phase,omitempty"`
	ID        string            `json:"id" yaml:"id"`
	Package   string            `json:"package" yaml:"package"`
	Needs     []string          `json:"needs,omitempty" yaml:"needs,omitempty"`
	Config    map[string]any    `json:"config" yaml:"config"`
	Secrets   map[string]string `json:"secrets,omitempty" yaml:"secrets,omitempty"`
	Inputs    map[string]string `json:"inputs,omitempty" yaml:"inputs,omitempty"`
}

// Request travels over stdin; all values in Secrets, Inputs and Previous are private.
type Request struct {
	// Project contains the controller-pinned deployment spec only for a project-deploy hook.
	Project  json.RawMessage   `json:"project,omitempty"`
	Version  int               `json:"version"`
	Action   string            `json:"action"`
	Key      string            `json:"key"`
	Step     Step              `json:"step"`
	Secrets  map[string]string `json:"secrets,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Previous json.RawMessage   `json:"previous,omitempty"`
	Prepared json.RawMessage   `json:"prepared,omitempty"`
	Host     *HostContext      `json:"host,omitempty"`
}

// HostContext is supplied by the controller, never by project configuration.
type HostContext struct {
	Runtime  string `json:"runtime"`
	StateDir string `json:"stateDir"`
}

// Prepared contains a bounded public review and an opaque execution payload stored encrypted by the controller.
type Prepared struct {
	Digest  string          `json:"digest"`
	Summary []string        `json:"summary"`
	Payload json.RawMessage `json:"payload"`
}

// Result is private package state and outputs, both encrypted before the next step can run.
type Result struct {
	State          json.RawMessage   `json:"state"`
	Outputs        map[string]string `json:"outputs"`
	PrivateOutputs map[string]string `json:"privateOutputs,omitempty"`
	Networks       []string          `json:"networks,omitempty"`
}

// Response is exactly one JSON document written to stdout by a package process.
type Response struct {
	Version    int         `json:"version"`
	Inspection *Inspection `json:"inspection,omitempty"`
	Prepared   *Prepared   `json:"prepared,omitempty"`
	Result     *Result     `json:"result,omitempty"`
}

// Runner keeps package SDKs and runtime dependencies outside the controller binary.
type Runner interface {
	Plan(context.Context, Request) (Prepared, error)
	Apply(context.Context, Request, string) (Result, error)
}
