// Package iac supplies the shared approval and recovery boundary for optional engines.
package iac

import (
	"context"
	"hostlier/pkg/packagekit/protected"
)

// Config selects an administrator-installed immutable program and known variables.
type Config struct {
	Bundle    string            `json:"bundle"`
	Variables map[string]string `json:"variables,omitempty"`
}

// Installed contains trusted host configuration, never project-selected paths.
type Installed struct {
	Bundle            protected.Bundle
	Outputs           []string
	SecretEnvironment map[string]string
}

// Execution contains isolated program files and a stable native backend directory.
type Execution struct {
	Installed                            Installed
	Workspace, Backend, Owner, Operation string
	Variables, Environment               map[string]string
}

// Engine implements only saved planning, approved execution and recorded state reads.
type Engine interface {
	Name() string
	Guarantee() string
	Plan(context.Context, Execution) ([]byte, []string, error)
	Apply(context.Context, Execution, []byte) error
	Outputs(context.Context, Execution) (Outputs, error)
	Revision(Execution) (string, error)
}

// Outputs explicitly separates native sensitive outputs from public bindings.
type Outputs struct {
	Public  map[string]string `json:"public"`
	Private map[string]string `json:"private"`
}

// Prepared binds an encrypted saved native plan to the reviewed identities.
type Prepared struct {
	Artifact protected.Handle  `json:"artifact"`
	Binding  protected.Binding `json:"binding"`
}

// State stores ownership and a durable operation receipt, never a competing engine state.
type State struct {
	Owner     string `json:"owner"`
	Operation string `json:"operation"`
	Revision  string `json:"revision"`
}

// receipt is written only after the engine returns success and outputs validate.
type receipt struct {
	State   State
	Outputs Outputs
}
