package packagekit

import (
	"encoding/json"
	"errors"
	"hostlier/pkg/workflow"
	"io"
)

// Describer exposes static public metadata without accepting execution inputs.
type Describer interface {
	// Capability returns the package's public versioned contract.
	Capability() workflow.Capability
}

// Describe writes public metadata without invoking the lifecycle handler.
func Describe(handler Describer, out io.Writer) error {
	capability := handler.Capability()
	// Reject incomplete contracts before publishing discovery output.
	if capability.Version != 1 || capability.Name == "" || capability.ConfigSchema == nil || capability.OutputSchema == nil {
		return errors.New("invalid package capability")
	}
	return json.NewEncoder(out).Encode(capability)
}
