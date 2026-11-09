package packagekit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hostlier/pkg/workflow"
	"io"
	"maps"
	"regexp"
	"sort"
	"strings"
)

// Codec pairs an explicit public schema with private typed validation.
type Codec[T any] struct {
	Schema   map[string]any
	Validate func(T) error
}

// Decode rejects unknown fields, trailing data and invalid typed values.
func (c Codec[T]) Decode(data []byte) (T, error) {
	var value T
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	// Strict decoding makes runtime inputs match the declared structure.
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF {
		return value, errors.New("invalid typed value")
	}
	// Keep private preparation within its bounded validated contract.
	if c.Validate != nil {
		// Keep private preparation within its bounded validated contract.
		if c.Validate(value) != nil {
			return value, errors.New("invalid typed value")
		}
	}
	return value, nil
}

// Call contains host-selected identity and private lifecycle inputs.
type Call[C, S any] struct {
	Request  workflow.Request
	Config   C
	Previous S
}

// Plan is a typed private preparation paired with a bounded public review.
type Plan[P any] struct {
	Prepared P
	Summary  []string
}

// Result is a typed durable receipt with explicitly mapped outputs.
type Result[S, O any] struct {
	State    S
	Outputs  O
	Networks []string
}

// Observation publishes outputs only after authoritative completion.
type Observation[S, O any] struct {
	Status workflow.InspectStatus
	Result *Result[S, O]
}

// OutputCodec maps typed outputs into declared public and private string bindings.
type OutputCodec[O any] struct {
	Codec[O]
	Names  []string
	Encode func(O) (map[string]string, map[string]string, error)
}

// Definition implements the existing transport with typed lifecycle callbacks.
type Definition[C, P, S, O any] struct {
	// Hooks lists optional entry points implemented by this package executable.
	Hooks         []string
	Update        bool
	KnownInputs   bool
	Name          string
	Config        Codec[C]
	Prepared      Codec[P]
	State         Codec[S]
	Outputs       OutputCodec[O]
	SecretAliases []string
	Plan          func(context.Context, Call[C, S]) (Plan[P], error)
	Apply         func(context.Context, Call[C, S], P) (Result[S, O], error)
	Inspect       func(context.Context, Call[C, S], P) (Observation[S, O], error)
	Reconcile     func(context.Context, Call[C, S], P) (Observation[S, O], error)
}

// Define validates the authoring contract before it can serve requests.
func Define[C, P, S, O any](d Definition[C, P, S, O]) (*Definition[C, P, S, O], error) {
	// Keep private preparation within its bounded validated contract.
	if d.Name == "" || d.Config.Schema == nil || d.Outputs.Schema == nil {
		return nil, errors.New("incomplete package definition")
	}
	// Keep private preparation within its bounded validated contract.
	if d.Outputs.Encode == nil || d.Plan == nil || d.Apply == nil {
		return nil, errors.New("incomplete package definition")
	}
	// An object output schema can declare binding names once for both discovery and execution.
	d.Outputs.Names = d.Capability().OutputNames()
	// Reject ambiguous or invalid declared binding names.
	if !validNames(d.SecretAliases) || !validNames(d.Outputs.Names) {
		return nil, errors.New("invalid contract names")
	}
	// Stop before publishing an incomplete or invalid preparation.
	if _, err := json.Marshal(d.Capability()); err != nil {
		return nil, errors.New("invalid public schema")
	}
	return &d, nil
}

// Capability exposes only explicit public contract metadata.
func (d *Definition[C, P, S, O]) Capability() workflow.Capability {
	return workflow.Capability{Version: 1, Name: d.Name, Hooks: d.Hooks, Update: d.Update, KnownInputs: d.KnownInputs, Inspect: d.Inspect != nil, ConfigSchema: d.Config.Schema, OutputSchema: d.Outputs.Schema, Outputs: d.Outputs.Names, SecretAliases: d.SecretAliases}
}

// Handle decodes private values before dispatching an explicit lifecycle callback.
func (d *Definition[C, P, S, O]) Handle(ctx context.Context, r workflow.Request) (workflow.Response, error) {
	// Require the supported protocol and a host-selected operation identity.
	if r.Version != 1 || r.Key == "" {
		return workflow.Response{}, errors.New("invalid package protocol")
	}
	call, err := d.call(r)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return workflow.Response{}, err
	}
	// Keep private preparation within its bounded validated contract.
	if r.Action == "plan" {
		return d.plan(ctx, call)
	}
	var envelope preparedEnvelope
	// Keep private preparation within its bounded validated contract.
	if json.Unmarshal(r.Prepared, &envelope) != nil || envelope.Identity != requestIdentity(r, envelope.DeferredInputs) {
		return workflow.Response{}, errors.New("prepared identity changed")
	}
	p, err := d.Prepared.Decode(envelope.Payload)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return workflow.Response{}, err
	}
	// Keep private preparation within its bounded validated contract.
	if r.Action == "apply" {
		result, err := d.Apply(ctx, call, p)
		// Stop before publishing an incomplete or invalid preparation.
		if err != nil {
			return workflow.Response{}, err
		}
		wire, err := d.result(result)
		return workflow.Response{Version: 1, Result: wire}, err
	}
	return d.observe(ctx, call, p)
}

// call validates configuration and secret aliases without exposing rejected values.
func (d *Definition[C, P, S, O]) call(r workflow.Request) (Call[C, S], error) {
	c := Call[C, S]{Request: r}
	raw, err := json.Marshal(r.Step.Config)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return c, errors.New("invalid config")
	}
	c.Config, err = d.Config.Decode(raw)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return c, err
	}
	// Check each declared identity against the explicit contract.
	for alias := range r.Secrets {
		// Keep private preparation within its bounded validated contract.
		if !contains(d.SecretAliases, alias) {
			return c, errors.New("undeclared secret alias")
		}
	}
	// Check each declared identity against the explicit contract.
	for alias := range r.Step.Secrets {
		// Keep private preparation within its bounded validated contract.
		if !contains(d.SecretAliases, alias) {
			return c, errors.New("undeclared secret alias")
		}
	}
	// Keep private preparation within its bounded validated contract.
	if len(r.Previous) > 0 {
		c.Previous, err = d.State.Decode(r.Previous)
	}
	return c, err
}

// plan serializes validated preparation and bounds the public review.
func (d *Definition[C, P, S, O]) plan(ctx context.Context, c Call[C, S]) (workflow.Response, error) {
	p, err := d.Plan(ctx, c)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return workflow.Response{}, err
	}
	raw, err := json.Marshal(p.Prepared)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return workflow.Response{}, errors.New("invalid preparation")
	}
	// Stop before publishing an incomplete or invalid preparation.
	if _, err = d.Prepared.Decode(raw); err != nil {
		return workflow.Response{}, err
	}
	// Keep private preparation within its bounded validated contract.
	if len(p.Summary) == 0 || len(p.Summary) > 32 {
		return workflow.Response{}, errors.New("invalid review")
	}
	// Keep private preparation within its bounded validated contract.
	if !safeSummary(p.Summary, c.Request.Secrets) {
		return workflow.Response{}, errors.New("private review value")
	}
	deferred := deferredHookInputs(c.Request)
	// A package requiring known inputs cannot defer review of upstream values.
	if d.KnownInputs && len(deferred) > 0 {
		return workflow.Response{}, errors.New("project deploy hook requires known input values during planning")
	}
	envelope, err := json.Marshal(preparedEnvelope{Identity: requestIdentity(c.Request, deferred), Payload: raw, DeferredInputs: deferred})
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return workflow.Response{}, err
	}
	return workflow.Response{Version: 1, Prepared: &workflow.Prepared{Summary: p.Summary, Payload: envelope}}, nil
}

// result validates typed values and refuses undeclared output publication.
func (d *Definition[C, P, S, O]) result(r Result[S, O]) (*workflow.Result, error) {
	state, err := json.Marshal(r.State)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return nil, errors.New("invalid state")
	}
	// Stop before publishing an incomplete or invalid preparation.
	if _, err = d.State.Decode(state); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(r.Outputs)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return nil, errors.New("invalid outputs")
	}
	// Stop before publishing an incomplete or invalid preparation.
	if _, err = d.Outputs.Decode(raw); err != nil {
		return nil, err
	}
	public, private, err := d.Outputs.Encode(r.Outputs)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return nil, errors.New("invalid outputs")
	}
	// Stop before publishing an incomplete or invalid preparation.
	if err := validateBindings(d.Outputs.Names, public, private); err != nil {
		return nil, err
	}
	result := &workflow.Result{State: state, Outputs: public, PrivateOutputs: private, Networks: r.Networks}
	// Keep private preparation within its bounded validated contract.
	if !workflow.ValidResult(*result) {
		return nil, errors.New("invalid result")
	}
	return result, nil
}

// observe dispatches only explicitly declared read-only recovery callbacks.
func (d *Definition[C, P, S, O]) observe(ctx context.Context, c Call[C, S], p P) (workflow.Response, error) {
	callback := d.Inspect
	// Keep private preparation within its bounded validated contract.
	if c.Request.Action == "reconcile" {
		callback = d.Reconcile
		// Keep private preparation within its bounded validated contract.
	} else if c.Request.Action != "inspect" {
		return workflow.Response{}, errors.New("unsupported package action")
	}
	// Keep private preparation within its bounded validated contract.
	if callback == nil {
		return workflow.Response{}, errors.New("unsupported package action")
	}
	observed, err := callback(ctx, c, p)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return workflow.Response{}, err
	}
	inspection := &workflow.Inspection{Status: observed.Status}
	// Keep private preparation within its bounded validated contract.
	if observed.Result != nil {
		inspection.Result, err = d.result(*observed.Result)
		// Stop before publishing an incomplete or invalid preparation.
		if err != nil {
			return workflow.Response{}, err
		}
	}
	// Keep private preparation within its bounded validated contract.
	if !inspection.Valid() {
		return workflow.Response{}, errors.New("invalid inspection")
	}
	return workflow.Response{Version: 1, Inspection: inspection}, nil
}

// contains checks an explicit alias or output allowlist.
func contains(values []string, value string) bool {
	// Check each declared identity against the explicit contract.
	for _, v := range values {
		// Keep private preparation within its bounded validated contract.
		if v == value {
			return true
		}
	}
	return false
}

// preparedEnvelope prevents replay under changed private or public operation inputs.
type preparedEnvelope struct {
	DeferredInputs []string        `json:"deferredInputs,omitempty"`
	Identity       string          `json:"identity"`
	Payload        json.RawMessage `json:"payload"`
}

// requestIdentity hashes stable intent without exposing private values in diagnostics.
func requestIdentity(r workflow.Request, deferred []string) string {
	// Only project hooks may receive new values after a predecessor executes.
	if r.Step.Hook == workflow.ProjectDeployHook && len(deferred) > 0 {
		r.Inputs = maps.Clone(r.Inputs)
		// Remove only unresolved aliases recorded in this prepared envelope.
		for _, alias := range deferred {
			// The producer reference stays in Step.Inputs and therefore in the identity.
			if _, declared := r.Step.Inputs[alias]; declared {
				delete(r.Inputs, alias)
			}
		}
	}
	r.Action = ""
	r.Prepared = nil
	data, _ := json.Marshal(r)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// deferredHookInputs keeps reviewed producer references in the identity when their
// private values will first be available after an approved predecessor runs.
func deferredHookInputs(r workflow.Request) []string {
	// Ordinary workflow plan identities keep their original wire encoding.
	if r.Step.Hook != workflow.ProjectDeployHook {
		return nil
	}
	var deferred []string
	// Retain the concrete values of any inputs already known at plan time.
	for alias := range r.Step.Inputs {
		// Only values a predecessor has yet to produce are deferred.
		if _, known := r.Inputs[alias]; !known {
			deferred = append(deferred, alias)
		}
	}
	sort.Strings(deferred)
	return deferred
}

// safeSummary bounds public text and rejects literal resolved secret values.
func safeSummary(summary []string, secrets map[string]string) bool {
	// Check each declared identity against the explicit contract.
	for _, line := range summary {
		// Keep private preparation within its bounded validated contract.
		if len(line) > 4096 {
			return false
		}
		// Check each declared identity against the explicit contract.
		for _, secret := range secrets {
			// Keep private preparation within its bounded validated contract.
			if secret != "" && strings.Contains(line, secret) {
				return false
			}
		}
	}
	return true
}

// validateBindings enforces declared names and prevents ambiguous visibility classes.
func validateBindings(names []string, public, private map[string]string) error {
	// Check each declared identity against the explicit contract.
	for name := range public {
		// Keep private preparation within its bounded validated contract.
		if !contains(names, name) {
			return errors.New("undeclared output")
		}
		// Keep private preparation within its bounded validated contract.
		if _, ok := private[name]; ok {
			return errors.New("ambiguous output")
		}
	}
	// Check each declared identity against the explicit contract.
	for name := range private {
		// Keep private preparation within its bounded validated contract.
		if !contains(names, name) {
			return errors.New("undeclared output")
		}
	}
	return nil
}

// validNames bounds contract bindings and rejects duplicate or unsafe aliases.
func validNames(names []string) bool {
	// Keep private preparation within its bounded validated contract.
	if len(names) > 32 {
		return false
	}
	seen := map[string]bool{}
	// Check each declared identity against the explicit contract.
	for _, name := range names {
		// Keep private preparation within its bounded validated contract.
		if !contractName.MatchString(name) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

// contractName matches process-safe workflow binding names.
var contractName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
