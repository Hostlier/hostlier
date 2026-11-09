// Package terraform implements approved exact saved-plan execution using a pinned CLI.
package terraform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hostlier/packages/iac"
	"hostlier/pkg/packagekit"
	"os"
	"path/filepath"
)

// Engine supports administrator-installed modules with the local state backend.
type Engine struct{}

// New constructs the optional executable package without controller dependencies.
func New(catalog map[string]iac.Installed) (packagekit.Handler, error) {
	return iac.New(Engine{}, catalog)
}

// Name identifies the installed package capability.
func (Engine) Name() string { return "terraform" }

// Guarantee describes the saved-plan execution boundary without promising atomic effects.
func (Engine) Guarantee() string {
	return "Terraform: apply the exact saved plan; provider failure can leave partial effects."
}

// run invokes the checksum-pinned CLI with fixed argv and private output capture.
func run(ctx context.Context, e iac.Execution, args ...string) ([]byte, error) {
	return e.Installed.Bundle.Run(ctx, "terraform", args, filepath.Join(e.Workspace, "program"), e.Environment)
}

// setup configures only the stable host-selected local backend and known variables.
func setup(ctx context.Context, e iac.Execution) error {
	// Stop this phase when private storage or native execution cannot be verified.
	if err := version(ctx, e); err != nil {
		return err
	}
	backend := map[string]any{"terraform": map[string]any{"backend": map[string]any{"local": map[string]string{"path": filepath.Join(e.Backend, "terraform.tfstate")}}}}
	raw, _ := json.Marshal(backend)
	// Stop this phase when private storage or native execution cannot be verified.
	if err := os.WriteFile(filepath.Join(e.Workspace, "program", "hostlier_backend_override.tf.json"), raw, 0600); err != nil {
		return err
	}
	raw, _ = json.Marshal(e.Variables)
	// Stop this phase when private storage or native execution cannot be verified.
	if err := os.WriteFile(filepath.Join(e.Workspace, "program", "hostlier.auto.tfvars.json"), raw, 0600); err != nil {
		return err
	}
	_, err := run(ctx, e, "init", "-input=false", "-no-color", "-get=false", "-lockfile=readonly", "-plugin-dir="+filepath.Join(e.Installed.Bundle.Root, "plugins"))
	return err
}

// Plan saves a native execution artifact and derives counts without exposing resource values.
func (Engine) Plan(ctx context.Context, e iac.Execution) ([]byte, []string, error) {
	// Stop this phase when private storage or native execution cannot be verified.
	if err := setup(ctx, e); err != nil {
		return nil, nil, err
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if _, err := run(ctx, e, "plan", "-input=false", "-no-color", "-out=hostlier.plan"); err != nil {
		return nil, nil, err
	}
	raw, err := run(ctx, e, "show", "-json", "hostlier.plan")
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, nil, err
	}
	summary, err := summarize(raw)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, nil, err
	}
	plan, err := os.ReadFile(filepath.Join(e.Workspace, "program", "hostlier.plan"))
	return plan, summary, err
}

// Apply supplies the approved saved plan and never asks Terraform to construct a replacement.
func (Engine) Apply(ctx context.Context, e iac.Execution, saved []byte) error {
	// Stop this phase when private storage or native execution cannot be verified.
	if err := setup(ctx, e); err != nil {
		return err
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err := os.WriteFile(filepath.Join(e.Workspace, "program", "hostlier.plan"), saved, 0600); err != nil {
		return err
	}
	_, err := run(ctx, e, "apply", "-input=false", "-no-color", "hostlier.plan")
	return err
}

// nativeOutput retains Terraform's sensitive classification at the protocol boundary.
type nativeOutput struct {
	Sensitive bool `json:"sensitive"`
	Value     any  `json:"value"`
}

// Outputs reads recorded native state and never refreshes or changes infrastructure.
func (Engine) Outputs(ctx context.Context, e iac.Execution) (iac.Outputs, error) {
	out := iac.Outputs{Public: map[string]string{}, Private: map[string]string{}}
	// Stop this phase when private storage or native execution cannot be verified.
	if err := setup(ctx, e); err != nil {
		return out, err
	}
	raw, err := run(ctx, e, "output", "-json")
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return out, err
	}
	var values map[string]nativeOutput
	// Reject an identity or visibility mismatch before publishing the operation result.
	if json.Unmarshal(raw, &values) != nil {
		return out, errors.New("invalid terraform outputs")
	}
	// Process only the explicitly declared identities or bindings for this operation.
	for name, value := range values {
		encoded, err := outputValue(value.Value)
		// Stop this phase when private storage or native execution cannot be verified.
		if err != nil {
			return out, err
		}
		// Reject an identity or visibility mismatch before publishing the operation result.
		if value.Sensitive {
			out.Private[name] = encoded
		} else {
			out.Public[name] = encoded
		}
	}
	return out, nil
}

// outputValue preserves strings and explicitly JSON-encodes structured native outputs.
func outputValue(value any) (string, error) {
	// Reject an identity or visibility mismatch before publishing the operation result.
	if s, ok := value.(string); ok {
		return s, nil
	}
	raw, err := json.Marshal(value)
	return string(raw), err
}

// Revision hashes the durable native checkpoint to invalidate stale saved plans.
func (Engine) Revision(e iac.Execution) (string, error) {
	raw, err := os.ReadFile(filepath.Join(e.Backend, "terraform.tfstate"))
	// Stop this phase when private storage or native execution cannot be verified.
	if os.IsNotExist(err) {
		return "absent", nil
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// version requires the explicitly supported pinned native plan-format runtime.
func version(ctx context.Context, e iac.Execution) error {
	raw, err := run(ctx, e, "version", "-json")
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return err
	}
	var v struct {
		Version string `json:"terraform_version"`
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if json.Unmarshal(raw, &v) != nil || v.Version != "1.9.8" {
		return errors.New("unsupported pinned terraform version")
	}
	return nil
}
