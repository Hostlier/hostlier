// Package pulumi implements optional constrained update plans through the Go Automation API.
package pulumi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hostlier/packages/iac"
	"hostlier/pkg/packagekit"
	"os"
	"path/filepath"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
)

// Engine supports pinned prebuilt Go programs and a local file backend.
type Engine struct{}

// New keeps the optional Automation API dependency outside the controller module.
func New(catalog map[string]iac.Installed) (packagekit.Handler, error) {
	return iac.New(Engine{}, catalog)
}

// Name identifies the package contract.
func (Engine) Name() string { return "pulumi" }

// Guarantee accurately labels incremental enforcement and experimental plan format.
func (Engine) Guarantee() string {
	return "Pulumi: experimental saved constrained update plan; a later mismatch can follow earlier completed mutations."
}

// stackName gives one stable native ownership key across approved updates.
func stackName(e iac.Execution) string {
	sum := sha256.Sum256([]byte(e.Owner))
	return "owner-" + hex.EncodeToString(sum[:12])
}

// workspace uses an explicitly supplied command and never inherits controller credentials.
func workspace(ctx context.Context, e iac.Execution) (auto.Workspace, error) {
	// Stop this phase when private storage or native execution cannot be verified.
	if err := version(ctx, e); err != nil {
		return nil, err
	}
	env := map[string]string{"PULUMI_BACKEND_URL": "file://" + e.Backend, "PULUMI_HOME": filepath.Join(e.Workspace, "home"), "PULUMI_SKIP_UPDATE_CHECK": "true", "PULUMI_DISABLE_AUTOMATIC_PLUGIN_ACQUISITION": "true", "GOROOT": filepath.Join(e.Installed.Bundle.Root, "goroot"), "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOENV": "off"}
	// Process only the explicitly declared identities or bindings for this operation.
	for k, v := range e.Environment {
		env[k] = v
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if _, ok := env["PULUMI_CONFIG_PASSPHRASE"]; !ok {
		return nil, errors.New("backend passphrase secret required")
	}
	return auto.NewLocalWorkspace(ctx, auto.WorkDir(filepath.Join(e.Workspace, "program")), auto.EnvVars(env), auto.Pulumi(command{execution: e, environment: env}))
}

// stack selects the owner's checkpoint and writes only disposable stack configuration.
func stack(ctx context.Context, e iac.Execution, create bool) (auto.Stack, error) {
	// Stop this phase when private storage or native execution cannot be verified.
	if err := restoreEncryptionSalt(e); err != nil {
		return auto.Stack{}, err
	}
	ws, err := workspace(ctx, e)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return auto.Stack{}, err
	}
	var s auto.Stack
	// Reject an identity or visibility mismatch before publishing the operation result.
	revision, revisionErr := (Engine{}).Revision(e)
	// Stop this phase when private storage or native execution cannot be verified.
	if revisionErr != nil {
		return auto.Stack{}, revisionErr
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if create && revision == "absent" {
		s, err = auto.NewStack(ctx, stackName(e), ws)
	} else {
		s, err = auto.SelectStack(ctx, stackName(e), ws)
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return s, errors.New("pulumi stack unavailable")
	}
	config := auto.ConfigMap{}
	// Process only the explicitly declared identities or bindings for this operation.
	for k, v := range e.Variables {
		config[k] = auto.ConfigValue{Value: v}
	}
	// Stop this phase when private storage or native execution cannot be verified.
	if err = s.SetAllConfig(ctx, config); err != nil {
		return s, errors.New("pulumi configuration failed")
	}
	return s, nil
}

// Plan stores the actual native update plan and exposes only aggregate operation counts.
func (Engine) Plan(ctx context.Context, e iac.Execution) ([]byte, []string, error) {
	s, err := stack(ctx, e, true)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(e.Workspace, "saved-plan.json")
	result, err := s.Preview(ctx, optpreview.Plan(path))
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, nil, errors.New("pulumi constrained preview failed")
	}
	counts := result.ChangeSummary
	summary := fmt.Sprintf("Resources: create %d, update %d, delete %d, replace %d, unchanged %d. Costs and exposure are not inferred.", counts["create"], counts["update"], counts["delete"], counts["replace"], counts["same"])
	raw, err := os.ReadFile(path)
	return raw, []string{summary}, err
}

// Apply supplies the saved update plan; enforcement is incremental, not transactional.
func (Engine) Apply(ctx context.Context, e iac.Execution, saved []byte) error {
	s, err := stack(ctx, e, false)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return err
	}
	path := filepath.Join(e.Workspace, "saved-plan.json")
	// Stop this phase when private storage or native execution cannot be verified.
	if err = os.WriteFile(path, saved, 0600); err != nil {
		return err
	}
	_, err = s.Up(ctx, optup.Plan(path))
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return errors.New("pulumi constrained update failed; effects may be partial")
	}
	return nil
}

// Outputs reads recorded outputs and preserves the engine's secret bit.
func (Engine) Outputs(ctx context.Context, e iac.Execution) (iac.Outputs, error) {
	out := iac.Outputs{Public: map[string]string{}, Private: map[string]string{}}
	s, err := stack(ctx, e, false)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return out, err
	}
	values, err := s.Outputs(ctx)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return out, errors.New("pulumi output read failed")
	}
	// Process only the explicitly declared identities or bindings for this operation.
	for name, value := range values {
		encoded, err := encode(value.Value)
		// Stop this phase when private storage or native execution cannot be verified.
		if err != nil {
			return out, err
		}
		// Reject an identity or visibility mismatch before publishing the operation result.
		if value.Secret {
			out.Private[name] = encoded
		} else {
			out.Public[name] = encoded
		}
	}
	return out, nil
}

// encode retains native strings and serializes structured binding values explicitly.
func encode(value any) (string, error) {
	// Reject an identity or visibility mismatch before publishing the operation result.
	if s, ok := value.(string); ok {
		return s, nil
	}
	raw, err := json.Marshal(value)
	return string(raw), err
}

// Revision hashes the stable checkpoint and excludes transient locks and history files.
func (Engine) Revision(e iac.Execution) (string, error) {
	root := filepath.Join(e.Backend, ".pulumi", "stacks")
	sum := sha256.New()
	found := false
	// Validate each entry without exposing private values to the public protocol.
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		// Stop this phase when private storage or native execution cannot be verified.
		if os.IsNotExist(walkErr) {
			return nil
		}
		// Stop this phase when private storage or native execution cannot be verified.
		if walkErr != nil {
			return walkErr
		}
		// Reject an identity or visibility mismatch before publishing the operation result.
		if entry.IsDir() {
			return nil
		}
		// Reject an identity or visibility mismatch before publishing the operation result.
		if entry.Name() != stackName(e)+".json" && entry.Name() != stackName(e)+".json.gz" {
			return nil
		}
		raw, err := os.ReadFile(path)
		// Stop this phase when private storage or native execution cannot be verified.
		if err != nil {
			return err
		}
		sum.Write(raw)
		found = true
		return nil
	})
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return "", err
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if !found {
		return "absent", nil
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// version verifies the actual executable before exposing a fixed Automation API version contract.
func version(ctx context.Context, e iac.Execution) error {
	raw, err := e.Installed.Bundle.Run(ctx, "pulumi", []string{"version"}, filepath.Join(e.Workspace, "program"), map[string]string{"PULUMI_SKIP_UPDATE_CHECK": "true"})
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return err
	}
	// Reject an identity or visibility mismatch before publishing the operation result.
	if strings.TrimSpace(string(raw)) != "v3.142.0" {
		return errors.New("unsupported pinned pulumi version")
	}
	return nil
}

// restoreEncryptionSalt uses the durable native checkpoint's passphrase provider identity.
// Recreating a workspace with a new salt would make the approved saved plan undecryptable.
func restoreEncryptionSalt(e iac.Execution) error {
	root := filepath.Join(e.Backend, ".pulumi", "stacks")
	// Validate each entry without exposing private values to the public protocol.
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		// Stop this phase when private storage or native execution cannot be verified.
		if os.IsNotExist(walkErr) {
			return nil
		}
		// Stop this phase when private storage or native execution cannot be verified.
		if walkErr != nil {
			return walkErr
		}
		// Reject an identity or visibility mismatch before publishing the operation result.
		if entry.IsDir() || entry.Name() != stackName(e)+".json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		// Stop this phase when private storage or native execution cannot be verified.
		if err != nil {
			return err
		}
		var checkpoint struct {
			Checkpoint struct {
				Latest struct {
					Secrets struct {
						Type  string `json:"type"`
						State struct {
							Salt string `json:"salt"`
						} `json:"state"`
					} `json:"secrets_providers"`
				} `json:"Latest"`
			} `json:"checkpoint"`
		}
		// Reject an identity or visibility mismatch before publishing the operation result.
		if json.Unmarshal(raw, &checkpoint) != nil {
			return errors.New("invalid native pulumi checkpoint")
		}
		provider := checkpoint.Checkpoint.Latest.Secrets
		// Reject an identity or visibility mismatch before publishing the operation result.
		if provider.Type != "passphrase" || provider.State.Salt == "" {
			return errors.New("unsupported native secrets provider")
		}
		settings, _ := json.Marshal(map[string]string{"encryptionsalt": provider.State.Salt})
		return os.WriteFile(filepath.Join(e.Workspace, "program", "Pulumi."+stackName(e)+".yaml"), settings, 0600)
	})
}
