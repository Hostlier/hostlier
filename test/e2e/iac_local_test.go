package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hostlier/internal/cli"
	"hostlier/internal/controller"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
	"hostlier/internal/resources"
	"hostlier/packages/iac"
	"hostlier/pkg/packagekit/protected"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// iacApplicationRuntime observes actual infrastructure bindings without requiring an application daemon.
// Infrastructure commands and native state use the real installed Terraform package and CLI.
type iacApplicationRuntime struct {
	mu     sync.Mutex
	values []string
}

// Name preserves stable host ownership across controller restarts.
func (*iacApplicationRuntime) Name() string { return "docker" }

// InspectImage supplies the immutable image for this application-boundary fixture.
func (*iacApplicationRuntime) InspectImage(context.Context, string) (model.ImageInfo, error) {
	return model.ImageInfo{Digest: resources.Images["postgres"]}, nil
}

// Deploy records the real public native output delivered through the controller's application bindings.
func (r *iacApplicationRuntime) Deploy(ctx context.Context, p model.Project, c model.DeployContext) (model.Deployment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, c.Secrets["VALUE"])
	return model.Deployment{Name: p.Name, RuntimeID: fmt.Sprint("application-", len(r.values)), Status: "running"}, nil
}

// Action is unused because this fixture tests approved infrastructure updates.
func (*iacApplicationRuntime) Action(context.Context, string, string, model.ActionContext) error {
	return nil
}

// Logs supplies no fabricated workload diagnostics.
func (*iacApplicationRuntime) Logs(context.Context, string) (string, error) { return "", nil }

// Doctor exposes the unused application health boundary.
func (*iacApplicationRuntime) Doctor(context.Context) (any, error) { return nil, nil }

// TestTerraformManagedControllerCLI verifies real saved plans through strict YAML, approval and durable execution.
func TestTerraformManagedControllerCLI(t *testing.T) {
	cliPath := os.Getenv("HOSTLIER_TERRAFORM_TEST_CLI")
	// Reject an identity or visibility mismatch before publishing the operation result.
	if cliPath == "" {
		t.Skip("set HOSTLIER_TERRAFORM_TEST_CLI to checksum-verified Terraform 1.9.8")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if err = os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	installTerraformE2E(t, ctx, directory, cliPath)
	appRuntime := &iacApplicationRuntime{}
	server := terraformController(t, directory, appRuntime)
	terraformManagedCLI(t, ctx, directory, server, "first", "deploy")
	firstID := terraformNativeIdentity(t, directory, "first")
	// Require this native lifecycle invariant before continuing the fixture.
	if err = server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	server = terraformController(t, directory, appRuntime)

	terraformManagedCLI(t, ctx, directory, server, "second", "update")
	secondID := terraformNativeIdentity(t, directory, "second")
	// Require this native lifecycle invariant before continuing the fixture.
	if firstID != secondID {
		t.Fatal("managed update replaced stable native ownership")
	}
	appRuntime.mu.Lock()
	defer appRuntime.mu.Unlock()
	// Require this native lifecycle invariant before continuing the fixture.
	if strings.Join(appRuntime.values, ",") != "first,second" {
		t.Fatal("native output did not reach application bindings", appRuntime.values)
	}
}

// terraformController uses the production executable runner and persistent controller state.
func terraformController(t *testing.T, directory string, r *iacApplicationRuntime) *controller.Controller {
	t.Helper()
	server, err := controller.New(controller.Options{StateDir: directory, Config: model.ServerConfig{Version: 1, Domains: []string{"hostlier.test"}}, Runtime: r})
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) }) // Stop the fixture even if an earlier CLI assertion fails.
	return server
}

// installTerraformE2E installs an actual wrapper whose fixed catalog points to pinned local fixture bytes.
func installTerraformE2E(t *testing.T, ctx context.Context, directory, cliPath string) {
	t.Helper()
	root := filepath.Join(directory, "bundle")
	// Require this native lifecycle invariant before continuing the fixture.
	if err := os.MkdirAll(filepath.Join(root, "program"), 0700); err != nil {
		t.Fatal(err)
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if err := os.Mkdir(filepath.Join(root, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(cliPath)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if err = os.WriteFile(filepath.Join(root, "terraform"), binary, 0700); err != nil {
		t.Fatal(err)
	}
	source := []byte("terraform { required_version = \"= 1.9.8\" }\nvariable \"value\" { type = string }\nresource \"terraform_data\" \"owned\" { input = var.value }\noutput \"value\" { value = terraform_data.owned.output }\noutput \"private\" {\n value = \"sentinel-private-output\"\n sensitive = true\n}\n")
	// Require this native lifecycle invariant before continuing the fixture.
	if err = os.WriteFile(filepath.Join(root, "program", "main.tf"), source, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]string{"terraform": terraformSHA(binary), "program/main.tf": terraformSHA(source)}
	catalog := map[string]iac.Installed{"local": {Bundle: protected.Bundle{Root: root, Platform: runtime.GOOS + "/" + runtime.GOARCH, Files: manifest}, Outputs: []string{"value", "private"}}}
	installTerraformWrapper(t, ctx, directory, catalog)
}

// terraformSHA pins source and CLI bytes without shell command construction.
func terraformSHA(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// installTerraformWrapper fixes the catalog location in the binary before digest-pinned installation.
func installTerraformWrapper(t *testing.T, ctx context.Context, directory string, catalog map[string]iac.Installed) {
	t.Helper()
	raw, err := json.Marshal(catalog)
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(directory, "terraform.json")
	// Require this native lifecycle invariant before continuing the fixture.
	if err = os.WriteFile(catalogPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "hostlier-terraform")
	build := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X main.catalogPath="+catalogPath, "-o", target, "./cmd/hostlier-terraform")
	build.Dir = filepath.Join("..", "..")
	// Require this native lifecycle invariant before continuing the fixture.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("wrapper build: %v %s", err, output)
	}
	runner := packageexec.Runner{Directory: filepath.Join(directory, "packages")}
	// Require this native lifecycle invariant before continuing the fixture.
	if _, err = runner.Install("terraform", target); err != nil {
		t.Fatal(err)
	}
}

// terraformManagedCLI reviews the real saved native plan before allowing each controller operation.
func terraformManagedCLI(t *testing.T, ctx context.Context, directory string, server *controller.Controller, value, command string) {
	t.Helper()
	project := "version: 1\nname: managed-iac\nimage: " + resources.Images["postgres"] + "\nport: 80\nroute: managed-iac.hostlier.test\nworkflow:\n  steps:\n    - id: infrastructure\n      package: terraform\n      lifecycle: managed\n      config:\n        bundle: local\n        variables:\n          value: " + value + "\n  bindings:\n    VALUE: infrastructure.value\n"
	// Require this native lifecycle invariant before continuing the fixture.
	if err := os.WriteFile(filepath.Join(directory, ".hostlier"), []byte(project), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	approvals := 0
	// Validate each entry without exposing private values to the public protocol.
	approve := func(string, bool) (string, error) {
		terraformBeforeApproval(t, directory, value)
		approvals++
		return "yes", nil
	}
	err := cli.RunWithOptions(ctx, []string{command}, strings.NewReader(""), &output, io.Discard, cli.Options{CWD: directory, Home: filepath.Join(directory, "home"), Client: localWorkflowAPI{handler: server.LocalHandler()}, Prompt: approve, PollInterval: 10 * time.Millisecond})
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil || approvals != 1 {
		t.Fatalf("managed %s: %v approvals=%d output=%s", command, err, approvals, output.String())
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if strings.Contains(output.String(), "sentinel-private-output") {
		t.Fatal("private native output leaked through CLI")
	}
}

// terraformNativeIdentity independently reads the real backend rather than trusting controller receipts.
func terraformNativeIdentity(t *testing.T, directory, want string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(directory, "iac-terraform", "owner-*", "terraform.tfstate"))
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil || len(paths) != 1 {
		t.Fatal("expected one stable native backend", err)
	}
	raw, err := os.ReadFile(paths[0])
	// Require this native lifecycle invariant before continuing the fixture.
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint struct {
		Outputs map[string]struct {
			Value string `json:"value"`
		}
		Resources []struct {
			Instances []struct {
				Attributes struct {
					ID string `json:"id"`
				}
			}
		}
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if json.Unmarshal(raw, &checkpoint) != nil || checkpoint.Outputs["value"].Value != want {
		t.Fatal("native checkpoint did not match approved value")
	}
	// Require this native lifecycle invariant before continuing the fixture.
	if len(checkpoint.Resources) != 1 || len(checkpoint.Resources[0].Instances) != 1 {
		t.Fatal("unexpected native resource count")
	}
	return checkpoint.Resources[0].Instances[0].Attributes.ID
}

// terraformBeforeApproval independently verifies no reviewed native mutation has run before approval.
func terraformBeforeApproval(t *testing.T, directory, next string) {
	t.Helper()
	// Updating must retain the prior native output until the user approves.
	if next == "second" {
		terraformNativeIdentity(t, directory, "first")
		return
	}
	paths, err := filepath.Glob(filepath.Join(directory, "iac-terraform", "owner-*", "terraform.tfstate"))
	// Initial preparation must not create the native resource checkpoint.
	if err != nil || len(paths) != 0 {
		t.Fatal("native mutation preceded interactive approval", err)
	}
}
