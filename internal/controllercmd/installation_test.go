package controllercmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hostlier/internal/installer"
	"hostlier/internal/model"
)

// TestInitApprovalGates keeps preview, preflight and explicit approval ahead of all installation mutations.
func TestInitApprovalGates(t *testing.T) {
	cases := []struct {
		name                   string
		args                   []string
		supported, approve     bool
		wantError              string
		wantConfirm, wantApply int
	}{
		{name: "preview", args: []string{"--dry-run"}, supported: true},
		{name: "unsupported preview", args: []string{"--dry-run", "--yes"}, supported: false},
		{name: "decline", supported: true, wantError: "CANCELLED", wantConfirm: 1},
		{name: "confirm", supported: true, approve: true, wantConfirm: 1, wantApply: 1},
		{name: "explicit approval", args: []string{"--yes"}, supported: true, wantApply: 1},
		{name: "unsupported approval", args: []string{"--yes"}, wantError: "INSTALL_PREFLIGHT"},
	}
	// Exercise each authorization path with an isolated fixture.
	for _, tc := range cases {
		// Count approval and mutation without touching host state.
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			confirmations, applications := 0, 0
			plan := installer.Plan{Supported: tc.supported, Fingerprint: "reviewed-plan", Changes: []string{"fixture change"}}
			deps := initDependencies{executable: filepath.Join(t.TempDir(), "hostlier-controller")}
			// Supply deterministic host facts for the review.
			deps.inspect = func(context.Context, installer.Options) (installer.Plan, error) { return plan, nil }
			// Observe prompt timing and return the selected operator answer.
			deps.confirm = func() (bool, error) {
				confirmations++
				// Approval must review the actual serialized inspection result.
				if !strings.Contains(out.String(), "reviewed-plan") {
					t.Fatal("prompt before plan")
				}
				return tc.approve, nil
			}
			// Capture the approved immutable plan without invoking privileged installation.
			deps.apply = func(_ context.Context, actual installer.Plan, options installer.ApplyOptions) error {
				applications++
				// The exact reviewed snapshot and affirmative authorization must reach the installer unchanged.
				if !reflect.DeepEqual(actual, plan) || !options.Approved || !strings.Contains(out.String(), "reviewed-plan") {
					t.Fatal("unreviewed apply", actual, options)
				}
				return nil
			}
			err := runInit(context.Background(), tc.args, strings.NewReader(""), &out, io.Discard, deps)
			assertInitError(t, err, tc.wantError)
			// Every mutation must have the expected approval path.
			if confirmations != tc.wantConfirm || applications != tc.wantApply {
				t.Fatalf("confirm=%d apply=%d", confirmations, applications)
			} // Every mutation needs the expected approval path.
			if strings.Contains(out.String(), "Hostlier controller installed") != (applications == 1) {
				t.Fatal("incorrect installation result", out.String())
			} // Never report installation for a preview or decline.
		})
	}
}

// TestInitHumanPreflight shows the actionable blockers before any proposed host changes.
func TestInitHumanPreflight(t *testing.T) {
	plan := installer.Plan{Options: installer.Options{Domain: "example.test", ControllerHostname: "hostlier.example.test", Runtime: "docker", Proxy: "nginx-existing"}, Errors: []string{"Only Ubuntu 24.04 LTS amd64 or arm64 is supported.", "hostname hostlier.example.test is already served"}, Changes: []string{"Add Hostlier Nginx include"}, Warnings: []string{"Check DNS"}, Fingerprint: "reviewed-plan"}
	deps := initDependencies{executable: filepath.Join(t.TempDir(), "hostlier-controller"), inspect: func(context.Context, installer.Options) (installer.Plan, error) { return plan, nil }}
	var out bytes.Buffer
	err := runInit(context.Background(), []string{"--yes"}, strings.NewReader(""), &out, io.Discard, deps)
	assertInitError(t, err, "INSTALL_PREFLIGHT")
	text := out.String()
	for _, want := range []string{"Installation blocked", "Controller: https://hostlier.example.test", "Errors to resolve:", "Only Ubuntu 24.04", "hostname hostlier.example.test", "Proposed changes:", "Warnings:", "Plan fingerprint: reviewed-plan"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	if strings.Index(text, "Errors to resolve:") > strings.Index(text, "Proposed changes:") || strings.HasPrefix(text, "{") {
		t.Fatal("preflight did not prioritize human-readable errors", text)
	}
}

// TestInitForwardsOptionsWithoutCLI binds installation assets to the controller release alone.
func TestInitForwardsOptionsWithoutCLI(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "release", "hostlier-controller")
	state := filepath.Join(root, "state")
	config := filepath.Join(root, "config.json")
	expected := installer.Options{Domain: "apps.example.test", Runtime: "k3s", Proxy: "nginx-existing", ControllerHostname: "control.example.test", StateDir: state, ConfigPath: config, CollectorSource: filepath.Join(root, "release", "collector-image")}
	var inspected installer.Options
	var applied installer.ApplyOptions
	var out bytes.Buffer
	var progress bytes.Buffer
	deps := initDependencies{executable: executable,
		// Capture every parsed option at the inspection boundary.
		inspect: func(_ context.Context, options installer.Options) (installer.Plan, error) {
			inspected = options
			return installer.Plan{Options: options, Supported: true}, nil
		},
		// Record exact release payload locations without copying executables.
		apply: func(_ context.Context, _ installer.Plan, options installer.ApplyOptions) error {
			applied = options
			options.OnProgress("edge-readiness", "started")
			options.OnProgress("edge-readiness", "completed")
			return nil
		},
		// An explicit approval flag must bypass interactive input.
		confirm: func() (bool, error) { t.Fatal("--yes prompted"); return false, nil },
	}
	err := runInit(context.Background(), []string{"--domain", expected.Domain, "--runtime", expected.Runtime, "--proxy", expected.Proxy, "--controller-hostname", expected.ControllerHostname, "--state-dir", state, "--config", config, "--json", "--yes"}, strings.NewReader(""), &out, &progress, deps)
	// Parsing must not drop runtime, coexistence or explicit storage choices.
	if err != nil || !reflect.DeepEqual(inspected, expected) {
		t.Fatalf("options=%+v error=%v", inspected, err)
	}
	// Server setup installs only the approved controller release.
	if !applied.Approved || applied.SourceExecutable != executable {
		t.Fatal(applied)
	}
	decoder := json.NewDecoder(&out)
	var plan installer.Plan
	var result map[string]bool
	// Preview and success remain separate machine-readable JSON values.
	if err := decoder.Decode(&plan); err != nil {
		t.Fatal(err)
	}
	// Installation completion follows its separately decoded preview.
	if err := decoder.Decode(&result); err != nil || !result["installed"] {
		t.Fatal(result, err)
	}
	var events []string
	progressDecoder := json.NewDecoder(&progress)
	for {
		var event struct{ Step, Status string }
		if err := progressDecoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		events = append(events, event.Step+":"+event.Status)
	}
	if strings.Join(events, ",") != "inspection:started,inspection:completed,edge-readiness:started,edge-readiness:completed" {
		t.Fatal(events)
	}
}

// TestInitHumanProgress gives interactive installers readable live stage updates.
func TestInitHumanProgress(t *testing.T) {
	var output, progress bytes.Buffer
	deps := initDependencies{executable: filepath.Join(t.TempDir(), "hostlier-controller"), inspect: func(context.Context, installer.Options) (installer.Plan, error) {
		return installer.Plan{Supported: true}, nil
	}, apply: func(_ context.Context, _ installer.Plan, options installer.ApplyOptions) error {
		options.OnProgress("edge-readiness", "started")
		options.OnProgress("edge-readiness", "failed")
		return errors.New("certificate validation timed out")
	}}
	err := runInit(context.Background(), []string{"--yes"}, strings.NewReader(""), &output, &progress, deps)
	if err == nil || !strings.Contains(err.Error(), "certificate validation timed out") {
		t.Fatal(err)
	}
	for _, want := range []string{"Inspecting server — started", "Inspecting server — completed", "Configuring edge and TLS — started", "Configuring edge and TLS — failed"} {
		if !strings.Contains(progress.String(), want) {
			t.Fatalf("missing %s from %s", want, progress.String())
		}
	}
	if strings.Contains(output.String(), "installed at") {
		t.Fatal("reported failed install as complete", output.String())
	}
}

// TestInitParsingAndHelp cannot inspect or mutate a host for invalid syntax or help.
func TestInitParsingAndHelp(t *testing.T) {
	// Help and malformed invocations share the no-inspection guarantee.
	for _, tc := range []struct {
		name    string
		args    []string
		success bool
	}{
		{"help", []string{"--help"}, true}, {"unknown", []string{"--not-an-option"}, false},
		{"missing value", []string{"--domain"}, false}, {"positional", []string{"server"}, false},
	} {
		// Isolate parser diagnostics for each invocation.
		t.Run(tc.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			// Inspection would violate the syntax-only command boundary.
			deps := initDependencies{inspect: func(context.Context, installer.Options) (installer.Plan, error) {
				t.Fatal("inspection during parsing")
				return installer.Plan{}, nil
			}}
			err := runInit(context.Background(), tc.args, strings.NewReader(""), io.Discard, &diagnostics, deps)
			// Syntax errors fail early while standard help exits successfully.
			if (err == nil) != tc.success {
				t.Fatal(err)
			}
			// Standard help must identify the server executable.
			if tc.success && !strings.Contains(diagnostics.String(), "hostlier-controller init") {
				t.Fatal(diagnostics.String())
			} // Help must identify the server executable.
		})
	}
}

// TestInitFailurePropagation never reports success after inspection, approval or installer failures.
func TestInitFailurePropagation(t *testing.T) {
	sentinel := errors.New("fixture failure")
	// Inject failures independently at each side-effect boundary.
	for _, stage := range []string{"inspect", "confirm", "apply"} {
		// Keep each failed stage independent from other cases.
		t.Run(stage, func(t *testing.T) {
			var out bytes.Buffer
			applies := 0
			deps := initDependencies{executable: filepath.Join(t.TempDir(), "hostlier-controller")}
			// Return a supported plan unless inspection itself fails.
			deps.inspect = func(context.Context, installer.Options) (installer.Plan, error) {
				// Inspection errors cannot yield a reviewable plan.
				if stage == "inspect" {
					return installer.Plan{}, sentinel
				} // Inspection failures cannot produce an approvable plan.
				return installer.Plan{Supported: true}, nil
			}
			// Approval input failure cannot authorize the installer.
			deps.confirm = func() (bool, error) {
				// Represent interrupted operator confirmation.
				if stage == "confirm" {
					return false, sentinel
				} // Interrupted approval is never affirmative.
				return true, nil
			}
			// Fail only after recording the attempted authorized installation.
			deps.apply = func(context.Context, installer.Plan, installer.ApplyOptions) error { applies++; return sentinel }
			err := runInit(context.Background(), nil, strings.NewReader(""), &out, io.Discard, deps)
			// Preserve the originating failure without publishing an installed response.
			if !errors.Is(err, sentinel) || strings.Contains(out.String(), `"installed":true`) {
				t.Fatal(err, out.String())
			}
			// Earlier failures must never reach apply.
			if stage != "apply" && applies != 0 {
				t.Fatal("applied after failed review")
			} // Earlier failures leave the host untouched.
		})
	}
}

// TestInitRejectsPipedApproval requires explicit --yes for noninteractive installation.
func TestInitRejectsPipedApproval(t *testing.T) {
	// Provide supported host facts without opening server resources.
	deps := initDependencies{executable: filepath.Join(t.TempDir(), "hostlier-controller"), inspect: func(context.Context, installer.Options) (installer.Plan, error) {
		return installer.Plan{Supported: true}, nil
		// Reject any mutation authorized solely by piped text.
	}, apply: func(context.Context, installer.Plan, installer.ApplyOptions) error {
		t.Fatal("piped approval installed")
		return nil
	}}
	err := runInit(context.Background(), nil, strings.NewReader("yes\n"), io.Discard, io.Discard, deps)
	assertInitError(t, err, "INTERACTIVE_REQUIRED")
}

// assertInitError checks stable machine-readable failures while retaining useful diagnostics.
func assertInitError(t *testing.T, err error, code string) {
	t.Helper()
	// Expected success includes preview and explicitly approved completion.
	if code == "" {
		// A successful path must not hide an inspection or installer failure.
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var publicError *model.Error
	// Rejected paths must preserve their public failure category.
	if !errors.As(err, &publicError) || publicError.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
