package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/workflow"
)

// packageContract interprets the describe document identically at enrollment and at plan time.
type packageContract struct {
	workflow.Capability
	schema *jsonschema.Schema
}

// compileContract permits in-document references but never loads files or remote schema documents.
func compileContract(cap workflow.Capability) (*packageContract, error) {
	c := jsonschema.NewCompiler()
	c.ExtractAnnotations = true
	// A package schema must be self-contained for offline discovery.
	c.LoadURL = func(string) (io.ReadCloser, error) { return nil, errors.New("external schema reference") }
	const id = "urn:hostlier:config"
	raw, err := json.Marshal(cap.ConfigSchema)
	// Only a complete schema can be used to scaffold project inputs.
	if err != nil || c.AddResource(id, bytes.NewReader(raw)) != nil {
		return nil, fmt.Errorf("invalid config schema from package %q", cap.Name)
	}
	schema, err := c.Compile(id)
	// Don't surface private filesystem locations or untrusted schema text.
	if err != nil {
		return nil, fmt.Errorf("invalid config schema from package %q", cap.Name)
	}
	return &packageContract{cap, schema}, nil
}

// controller identifies workflow packages that require no local deployment code.
func (c *packageContract) controller() bool {
	// Local deployment hooks own the project configuration when a package offers both.
	for _, hook := range []string{"project-plan", "project-package", "project-contribute"} {
		// The local package proposes its own reviewed controller steps.
		if slices.Contains(c.Hooks, hook) {
			return false
		}
	}
	return len(c.Hooks) == 0 || slices.Contains(c.Hooks, workflow.ProjectDeployHook)
}

// inputs supplies declared defaults and rejects incomplete or mistyped settings before any hook runs.
func (c *packageContract) inputs(settings map[string]any) (map[string]any, error) {
	values := maps.Clone(settings)
	// JSON Schema validates an empty input object, not null.
	if values == nil {
		values = map[string]any{}
	}
	prefix := "packages." + c.Name
	// Controller entries also carry secret references and application bindings.
	if c.controller() {
		prefix += ".config"
	}
	// A blank scaffold field means unset. False, zero and empty strings are actual values.
	for name, field := range c.schema.Properties {
		// Explicit values take precedence over defaults.
		if values[name] != nil {
			continue
		}
		delete(values, name)
		// Defaults are public values published by the package author.
		if field.Default != nil {
			values[name] = field.Default
		}
	}
	// Report missing properties by name without printing operator-supplied data.
	for _, name := range c.schema.Required {
		// A scaffold can be generated before required deployment choices are made.
		if _, ok := values[name]; !ok {
			return nil, fmt.Errorf("%s.%s is required", prefix, name)
		}
	}
	err := c.schema.Validate(values)
	// JSON Schema diagnostics can include values; emit only the field location.
	if err != nil {
		return nil, inputValidationError(prefix, err)
	}
	return values, nil
}

// inputValidationError keeps rejected configuration values out of CLI diagnostics.
func inputValidationError(prefix string, err error) error {
	var ve *jsonschema.ValidationError
	// A validation leaf identifies the offending field and constraint.
	if errors.As(err, &ve) {
		// Follow a representative cause without printing the library's raw message.
		for len(ve.Causes) > 0 {
			ve = ve.Causes[0]
		}
		return fmt.Errorf("%s%s fails package config schema (%s)", prefix, ve.InstanceLocation, ve.KeywordLocation)
	}
	return fmt.Errorf("%s must contain JSON-compatible values", prefix)
}

// controllerSettings are references and public inputs used to compose an ordinary reviewed step.
type controllerSettings struct {
	Config   map[string]any    `json:"config"`
	Secrets  map[string]string `json:"secrets"`
	Bindings map[string]string `json:"bindings"`
}

// contribution lowers a controller enrollment to existing Hostlier protocol types.
func (c *packageContract) contribution(settings map[string]any) (packagekit.ProjectContribution, error) {
	var out packagekit.ProjectContribution
	var opts controllerSettings
	raw, err := json.Marshal(settings)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	// Unknown envelope fields must not silently discard deployment intent.
	if err != nil || decoder.Decode(&opts) != nil {
		return out, fmt.Errorf("packages.%s accepts config, secrets and bindings", c.Name)
	}
	values, err := c.inputs(opts.Config)
	// Reject invalid settings before producing a workflow.
	if err != nil {
		return out, err
	}
	step := workflow.Step{ID: c.Name, Package: c.Name, Config: values, Secrets: opts.Secrets}
	// An explicit deploy entry point requires the controller's reviewed pre-rollout phase.
	if slices.Contains(c.Hooks, workflow.ProjectDeployHook) {
		step.Hook = workflow.ProjectDeployHook
		step.Phase = "beforeDeploy"
	}
	// Aliases are stored secret names only; an empty placeholder is not a binding.
	for name, ref := range opts.Secrets {
		// The package controls the set of available aliases.
		if !slices.Contains(c.SecretAliases, name) {
			return out, fmt.Errorf("undeclared secret alias in packages.%s.secrets: %s", c.Name, name)
		}
		// Leave unselected aliases out of controller requests.
		if ref == "" {
			delete(step.Secrets, name)
		}
	}
	out.Version = 1
	out.Steps = []workflow.Step{step}
	out.WorkflowBindings = opts.Bindings
	names := c.OutputNames()
	// Bind only explicitly selected return values to the application.
	for env, name := range opts.Bindings {
		// Reject typos before attempting remote provisioning.
		if !slices.Contains(names, name) {
			return out, fmt.Errorf("packages.%s.bindings.%s must name a declared output (%s)", c.Name, env, strings.Join(names, ", "))
		}
		out.WorkflowBindings[env] = c.Name + "." + name
	}
	return out, nil
}
