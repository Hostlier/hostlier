// Package product composes local declarative products from server-installed workflow packages.
package product

import "hostlier/pkg/workflow"

// Definition is a versioned local product; it cannot introduce executables or service providers.
type Definition struct {
	Policy   *workflow.ProductPolicy `json:"policy,omitempty" yaml:"policy,omitempty"`
	Version  int                     `json:"version" yaml:"version"`
	Name     string                  `json:"name" yaml:"name"`
	Revision string                  `json:"revision" yaml:"revision"`
	Inputs   map[string]Input        `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Secrets  []string                `json:"secrets,omitempty" yaml:"secrets,omitempty"`
	Steps    []Step                  `json:"steps" yaml:"steps"`
	Bindings map[string]string       `json:"bindings,omitempty" yaml:"bindings,omitempty"`
}

// Input declares a typed public value and optional bounded set of allowed values.
type Input struct {
	Type     string `json:"type" yaml:"type"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Default  any    `json:"default,omitempty" yaml:"default,omitempty"`
	Enum     []any  `json:"enum,omitempty" yaml:"enum,omitempty"`
}

// Step extends an installed package invocation with explicit public and secret-reference bindings.
type Step struct {
	workflow.Step `yaml:",inline"`
	ConfigInputs  map[string]string `json:"configInputs,omitempty" yaml:"configInputs,omitempty"`
	SecretInputs  map[string]string `json:"secretInputs,omitempty" yaml:"secretInputs,omitempty"`
}

// Invocation supplies public values and stored secret names, never secret plaintext.
type Invocation struct {
	Inputs     map[string]any    `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	SecretRefs map[string]string `json:"secretRefs,omitempty" yaml:"secretRefs,omitempty"`
}
