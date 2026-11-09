package iac

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/packagekit/protected"
	"hostlier/pkg/workflow"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Package holds an immutable administrator catalog and one optional native engine.
type Package struct {
	engine  Engine
	catalog map[string]Installed
}

// New validates the catalog and constructs the existing typed package protocol.
func New(engine Engine, catalog map[string]Installed) (packagekit.Handler, error) {
	p := &Package{engine: engine, catalog: catalog}
	names, aliases, err := p.declarations()
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, err
	}
	// Validate each entry without exposing private values to the public protocol.
	d := packagekit.Definition[Config, Prepared, State, Outputs]{Update: true, KnownInputs: true, Name: engine.Name(), Config: packagekit.Codec[Config]{Schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"bundle"}, "properties": map[string]any{"bundle": map[string]any{"type": "string"}, "variables": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}}}, Validate: p.validate}, Outputs: packagekit.OutputCodec[Outputs]{Codec: packagekit.Codec[Outputs]{Schema: outputSchema(names)}, Names: names, Encode: func(o Outputs) (map[string]string, map[string]string, error) { return o.Public, o.Private, nil }}, SecretAliases: aliases, Plan: p.plan, Apply: p.apply, Inspect: p.inspect, Reconcile: p.inspect}
	return packagekit.Define(d)
}

// declarations collects only administrator-declared outputs and credential aliases.
func (p *Package) declarations() ([]string, []string, error) {
	outputs, secrets := map[string]bool{}, map[string]bool{}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if p.engine == nil || len(p.catalog) == 0 {
		return nil, nil, errors.New("empty engine catalog")
	}
	// Process only the explicitly declared identities or bindings for this operation.
	for name, b := range p.catalog {
		// Reject an identity or visibility mismatch before publishing the operation result.
		if name == "" {
			return nil, nil, errors.New("invalid bundle name")
		}
		// Stop this phase when private storage or native execution cannot be verified.
		if err := validateInstalled(b); err != nil {
			return nil, nil, err
		}
		// Process only the explicitly declared identities or bindings for this operation.
		for _, n := range b.Outputs {
			outputs[n] = true
		}
		// Process only the explicitly declared identities or bindings for this operation.
		for n := range b.SecretEnvironment {
			secrets[n] = true
		}
	}
	return sorted(outputs), sorted(secrets), nil
}

// sorted makes public declarations deterministic.
func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	// Process only the explicitly declared identities or bindings for this operation.
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// outputSchema exposes the installed catalog's finite binding names.
func outputSchema(names []string) map[string]any {
	props := map[string]any{}
	// Process only the explicitly declared identities or bindings for this operation.
	for _, n := range names {
		props[n] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": props}
}

// validate refuses unknown bundles without interpreting paths or commands.
func (p *Package) validate(c Config) error {
	// Reject an identity or visibility mismatch before publishing the operation result.
	if _, ok := p.catalog[c.Bundle]; !ok {
		return errors.New("unknown installed bundle")
	}
	return nil
}

// hash computes a private canonical identity without exposing source values.
func hash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// operation binds all resolved intent while remaining stable between lifecycle actions.
func operation(r workflow.Request) string { r.Action = ""; r.Prepared = nil; return hash(r) }

// session creates host-selected durable storage and an isolated disposable program.
func (p *Package) session(c packagekit.Call[Config, State]) (*protected.Store, Execution, func(), error) {
	// Reject an identity or visibility mismatch before publishing the operation result.
	if c.Request.Host == nil || c.Request.Host.StateDir == "" || c.Request.Key == "" {
		return nil, Execution{}, nil, errors.New("host storage required")
	}
	root := filepath.Join(c.Request.Host.StateDir, "iac-"+p.engine.Name())
	store, err := protected.Open(root)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, Execution{}, nil, err
	}
	backend := filepath.Join(root, "owner-"+hash(c.Request.Key))
	// Stop this phase when private storage or native execution cannot be verified.
	if err = privateBackend(backend); err != nil {
		return nil, Execution{}, nil, err
	}
	workspace, clean, err := store.Workspace(c.Request.Key, operation(c.Request))
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, Execution{}, nil, err
	}
	e := Execution{Installed: p.catalog[c.Config.Bundle], Workspace: workspace, Backend: backend, Owner: c.Request.Key, Operation: operation(c.Request), Variables: map[string]string{}, Environment: map[string]string{}}
	// Stop this phase when private storage or native execution cannot be verified.
	if err = e.Installed.Bundle.Materialize(workspace); err != nil {
		clean()
		return nil, e, nil, err
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err = configureExecution(&e, c); err != nil {
		clean()
		return nil, e, nil, err
	}
	return store, e, clean, nil
}

// configureExecution resolves known variables and explicitly mapped credentials.
func configureExecution(e *Execution, c packagekit.Call[Config, State]) error {
	// Process only the explicitly declared identities or bindings for this operation.
	for k, v := range c.Config.Variables {
		e.Variables[k] = v
	}
	// Process only the explicitly declared identities or bindings for this operation.
	for k, v := range c.Request.Inputs {
		// Reject an identity or visibility mismatch before publishing the operation result.
		if _, ok := e.Variables[k]; ok {
			return errors.New("duplicate input variable")
		}
		e.Variables[k] = v
	}
	// Process only the explicitly declared identities or bindings for this operation.
	for alias, value := range c.Request.Secrets {
		env, ok := e.Installed.SecretEnvironment[alias]
		// Reject an identity or visibility mismatch before publishing the operation result.
		if !ok {
			return errors.New("credential not configured for bundle")
		}
		e.Environment[env] = value
	}
	return nil
}

// binding pins the native revision and full immutable installed toolchain.
func (p *Package) binding(e Execution) (protected.Binding, error) {
	digest, err := e.Installed.Bundle.Digest()
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return protected.Binding{}, err
	}
	revision, err := p.engine.Revision(e)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return protected.Binding{}, err
	}
	return protected.Binding{Owner: e.Owner, Operation: e.Operation, Config: e.Operation, Executable: executableDigest(), Bundle: hash(e.Installed), Source: digest, Backend: hash(e.Backend), State: revision}, nil
}

// executableDigest pins the adapter process alongside its external toolchain.
func executableDigest() string {
	path, err := os.Executable()
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(path)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// plan persists native saved plans encrypted and publishes only constructed summaries.
func (p *Package) plan(ctx context.Context, c packagekit.Call[Config, State]) (packagekit.Plan[Prepared], error) {
	store, e, clean, err := p.session(c)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Plan[Prepared]{}, err
	}
	defer clean()
	unlock, err := store.Lock(e.Owner)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Plan[Prepared]{}, err
	}
	defer unlock()
	// Stop this phase when private storage or native execution cannot be verified.
	if err = unresolved(e); err != nil {
		return packagekit.Plan[Prepared]{}, err
	}
	raw, summary, err := p.engine.Plan(ctx, e)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Plan[Prepared]{}, err
	}
	binding, err := p.binding(e)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return packagekit.Plan[Prepared]{}, err
	}
	handle, err := store.Save(binding, raw, 24*time.Hour)
	summary = append([]string{p.engine.Guarantee(), "Recovery verifies recorded checkpoint state; live provider drift is not refreshed."}, summary...)
	return packagekit.Plan[Prepared]{Prepared: Prepared{Artifact: handle, Binding: binding}, Summary: summary}, err
}
