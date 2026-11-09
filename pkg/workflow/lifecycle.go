package workflow

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
)

// InspectStatus describes authoritative observations without authorizing a mutation.
type InspectStatus string

const (
	// Completed means the approved outcome and ownership were independently verified.
	Completed InspectStatus = "completed"
	// NotStarted means absence of all effects was proven and apply may safely begin.
	NotStarted InspectStatus = "not_started"
	// Uncertain requires operator investigation and must never trigger blind replay.
	Uncertain InspectStatus = "uncertain"
)

// Inspection returns private outputs only after the approved outcome is verified.
type Inspection struct {
	Status InspectStatus `json:"status"`
	Result *Result       `json:"result,omitempty"`
}

// Inspector is an optional read-only lifecycle extension to Runner.
type Inspector interface {
	// Inspect observes approved effects without performing package mutations.
	Inspect(context.Context, Request) (Inspection, error)
}

// Valid rejects unproven outputs and incomplete completed observations.
func (i Inspection) Valid() bool {
	// Completed observations alone may publish private outputs.
	if i.Status == Completed {
		return i.Result != nil && ValidResult(*i.Result)
	}
	return (i.Status == NotStarted || i.Status == Uncertain) && i.Result == nil
}

// Capability describes an executable package's versioned public contract.
type Capability struct {
	ProjectOriginEnv  string         `json:"projectOriginEnv,omitempty"`
	ProjectPackageEnv []string       `json:"projectPackageEnv,omitempty"`
	Hooks             []string       `json:"hooks,omitempty"`
	Update            bool           `json:"update,omitempty"`
	KnownInputs       bool           `json:"knownInputs,omitempty"`
	Version           int            `json:"version"`
	Name              string         `json:"name"`
	Inspect           bool           `json:"inspect"`
	ConfigSchema      map[string]any `json:"configSchema"`
	SecretAliases     []string       `json:"secretAliases,omitempty"`
	OutputSchema      map[string]any `json:"outputSchema"`
	Outputs           []string       `json:"outputs"`
}

// OutputNames returns the explicit binding names, or the public schema's properties when omitted.
func (c Capability) OutputNames() []string {
	// Explicit encoders may map a typed result to a different set of binding names.
	if c.Outputs != nil {
		return slices.Clone(c.Outputs)
	}
	// Normalize both typed Go maps and JSON-decoded metadata.
	raw, err := json.Marshal(c.OutputSchema["properties"])
	var properties map[string]json.RawMessage
	// An opaque result type has no implicit binding names.
	if err != nil || json.Unmarshal(raw, &properties) != nil {
		return nil
	}
	return slices.Sorted(maps.Keys(properties))
}
