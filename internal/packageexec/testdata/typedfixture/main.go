// Command typedfixture is a local contract fixture without external resources.
package main

import (
	"context"
	"encoding/json"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/packagekit/protected"
	"hostlier/pkg/workflow"
	"os"
	"path/filepath"
	"time"
)

// config deliberately exposes no command or filesystem configuration.
type config struct {
	LoseResponse bool `json:"loseResponse"`
}

// outputs separates application-safe status from a private credential.
type outputs struct {
	Status     string
	Credential string
}

// receipt binds the independently observed outcome to the approved artifact.
type receipt struct {
	Owner, Operation, Artifact string
	Result                     packagekit.Result[string, outputs]
}

// binding derives the protected artifact's owner from the host operation.
func binding(c packagekit.Call[config, string]) protected.Binding {
	return protected.Binding{Owner: c.Request.Step.ID, Operation: c.Request.Key, Config: "fixture-v1"}
}

// plan persists an encrypted saved artifact and emits only a public count.
func plan(ctx context.Context, c packagekit.Call[config, string]) (packagekit.Plan[protected.Handle], error) {
	s, err := protected.Open(c.Request.Host.StateDir)
	// Open protected storage before preparing an encrypted plan.
	if err != nil {
		return packagekit.Plan[protected.Handle]{}, err
	}
	h, err := s.Save(binding(c), []byte("private-saved-plan"), time.Hour)
	return packagekit.Plan[protected.Handle]{Prepared: h, Summary: []string{"create 1 local receipt"}}, err
}

// result constructs the durable typed receipt used by apply and inspection.
func result() packagekit.Result[string, outputs] {
	return packagekit.Result[string, outputs]{State: "completed", Outputs: outputs{Status: "ready", Credential: "sentinel-private-output"}}
}

// apply consumes exactly the artifact and records completion before simulating response loss.
func apply(ctx context.Context, c packagekit.Call[config, string], h protected.Handle) (packagekit.Result[string, outputs], error) {
	s, err := protected.Open(c.Request.Host.StateDir)
	// Applying requires access to the original protected artifact store.
	if err != nil {
		return packagekit.Result[string, outputs]{}, err
	}
	// Verify the saved artifact and its ownership before recording any effect.
	if _, err = s.Load(h, binding(c)); err != nil {
		return packagekit.Result[string, outputs]{}, err
	}
	raw, _ := json.Marshal(receipt{Owner: c.Request.Step.ID, Operation: c.Request.Key, Artifact: h.SHA256, Result: result()})
	// Persist the completion receipt before returning or simulating a lost response.
	if err = os.WriteFile(filepath.Join(c.Request.Host.StateDir, "receipt"), raw, 0600); err != nil {
		return packagekit.Result[string, outputs]{}, err
	}
	// Simulate process failure only after the durable effect exists.
	if c.Config.LoseResponse {
		os.Exit(17)
	}
	return result(), nil
}

// inspect proves local durable completion without replaying the effect.
func inspect(ctx context.Context, c packagekit.Call[config, string], h protected.Handle) (packagekit.Observation[string, outputs], error) {
	raw, err := os.ReadFile(filepath.Join(c.Request.Host.StateDir, "receipt"))
	// A missing receipt proves this local fixture has not started.
	if os.IsNotExist(err) {
		return packagekit.Observation[string, outputs]{Status: workflow.NotStarted}, nil
	}
	var r receipt
	// Unreadable or malformed receipts cannot prove completion.
	if err != nil || json.Unmarshal(raw, &r) != nil {
		return packagekit.Observation[string, outputs]{Status: workflow.Uncertain}, nil
	}
	// Only the exact owner, operation and saved artifact establish the outcome.
	if r.Owner != c.Request.Step.ID || r.Operation != c.Request.Key || r.Artifact != h.SHA256 {
		return packagekit.Observation[string, outputs]{Status: workflow.Uncertain}, nil
	}
	return packagekit.Observation[string, outputs]{Status: workflow.Completed, Result: &r.Result}, nil
}

// main serves the same typed package contract used by real installed executables.
func main() {
	d, err := packagekit.Define(packagekit.Definition[config, protected.Handle, string, outputs]{
		Name:   "typedfixture",
		Config: packagekit.Codec[config]{Schema: map[string]any{"type": "object"}},
		Outputs: packagekit.OutputCodec[outputs]{
			Codec: packagekit.Codec[outputs]{Schema: map[string]any{"type": "object"}},
			Names: []string{"status", "credential"},
			// Keep credentials separate from application-safe status.
			Encode: func(o outputs) (map[string]string, map[string]string, error) {
				return map[string]string{"status": o.Status}, map[string]string{"credential": o.Credential}, nil
			},
		},
		Plan: plan, Apply: apply, Inspect: inspect,
	})
	// Invalid typed definitions cannot serve requests.
	if err != nil {
		os.Exit(1)
	}
	// Expose capabilities without running plan or apply handlers.
	if len(os.Args) > 1 && os.Args[1] == "describe" {
		err = packagekit.Describe(d, os.Stdout)
	} else {
		err = packagekit.Serve(context.Background(), d, os.Stdin, os.Stdout)
	}
	// Propagate serving or description failures to the process caller.
	if err != nil {
		os.Exit(1)
	}
}
