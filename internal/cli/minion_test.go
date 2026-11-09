package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// minionFixture gives process-delegation tests a project whose path exercises literal argument handling.
func minionFixture(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project with spaces")
	// Every fixture must exist before invocation.
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "minion.json")
	// Test setup never publishes a fake ready config on write failure.
	if err := os.WriteFile(config, []byte(`{"entry":"app.ts"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return root, config
}

// installMinionFixture creates only the local CLI entrypoint needed to resolve installed-package delegation.
func installMinionFixture(t *testing.T, root string) string {
	t.Helper()
	script := filepath.Join(root, "node_modules", "@fused", "minion", "dist", "src", "cli.js")
	// The nested package shape matches npm's layout.
	if err := os.MkdirAll(filepath.Dir(script), 0755); err != nil {
		t.Fatal(err)
	}
	// A missing script must not pass resolution.
	if err := os.WriteFile(script, []byte("// fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return script
}

// TestMinionDevUsesInstalledPackage proves local commands require no controller and preserve exact config/port arguments.
func TestMinionDevUsesInstalledPackage(t *testing.T) {
	// Both fixed and ephemeral ports preserve their explicit selection through delegation.
	for _, port := range []string{"4321", "0"} {
		// Each listener selection has an isolated project and subprocess recorder.
		t.Run(port, func(t *testing.T) { checkMinionDevDelegation(t, port) })
	}
}

// checkMinionDevDelegation validates the local project's exact CLI, config and listener arguments.
func checkMinionDevDelegation(t *testing.T, port string) {
	t.Helper()
	root, config := minionFixture(t)
	script := installMinionFixture(t, root)
	client := &fakeClient{}
	calls := 0
	// Capture literal subprocess arguments while avoiding external package tools.
	run := func(_ context.Context, dir, executable string, args []string, _ io.Reader, out, errOut io.Writer) error {
		calls++
		// Runtime discovery is local and precedes delegation.
		if len(args) == 1 && args[0] == "--version" {
			_, err := io.WriteString(out, "v24.21.0\n")
			return err
		}
		expected := []string{script, "dev", config, "--port", port}
		// No shell interpolation or remote executable selection is permitted.
		if dir != root || executable != "node" || strings.Join(args, "\x00") != strings.Join(expected, "\x00") {
			t.Fatalf("unexpected process: %q %q %q", dir, executable, args)
		}
		return nil
	}
	_, err := invoke([]string{"minion", "dev", "--port", port}, "", Options{CWD: root, Home: t.TempDir(), Client: client, RunMinion: run})
	// Neither package discovery nor dev opens a controller session.
	if err != nil || calls != 2 || len(client.calls) != 0 {
		t.Fatal(err, calls, client.calls)
	}
}

// TestMinionAutoInstallUsesExactLocalArchive covers the first-run install followed by the requested framework command.
func TestMinionAutoInstallUsesExactLocalArchive(t *testing.T) {
	root, config := minionFixture(t)
	archive := filepath.Join(t.TempDir(), "fused-minion-0.1.0.tgz")
	// Install selection requires a concrete local file.
	if err := os.WriteFile(archive, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	// Capture literal subprocess arguments while avoiding external package tools.
	run := func(_ context.Context, dir, executable string, args []string, _ io.Reader, out, errOut io.Writer) error {
		calls++
		// Avoid requiring Node in Go-only unit-test environments.
		if executable == "node" && len(args) == 1 {
			_, err := io.WriteString(out, "v24.21.0\n")
			return err
		}
		if executable == "npm" { // npm receives the bundled archive as one argument, never a remote package name.
			checkMinionInstall(t, root, dir, args)
			return nil
		}
		// Installation must resume the original action exactly once.
		checkMinionPrepare(t, executable, config, args)
		return nil
	}
	_, err := invoke([]string{"minion", "prepare", config}, "", Options{CWD: root, Home: t.TempDir(), RunMinion: run, Environment: map[string]string{"HOSTLIER_MINION_PACKAGE": archive}})
	// One version check, one install and one framework command are sufficient.
	if err != nil || calls != 3 {
		t.Fatal(err, calls)
	}
}

// TestMinionRejectsInvalidInputsBeforeProcess checks command boundaries without any external effects.
func TestMinionRejectsInvalidInputsBeforeProcess(t *testing.T) {
	// Each malformed command must fail before runtime discovery.
	for _, args := range [][]string{{"minion", "dev", "--port", "-1"}, {"minion", "dev", "--port", "65536"}, {"minion", "dev", "one.json", "--file", "two.json"}, {"minion", "dev", "--server", "production"}, {"minion", "install", "unexpected"}, {"minion", "unknown"}} {
		// Isolate subprocess counters per invalid command.
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			calls := 0
			// An invalid invocation may not execute even runtime discovery.
			run := func(context.Context, string, string, []string, io.Reader, io.Writer, io.Writer) error {
				calls++
				return nil
			}
			_, err := invoke(args, "", Options{CWD: t.TempDir(), Home: t.TempDir(), RunMinion: run})
			// Argument validation owns this boundary.
			if err == nil || calls != 0 {
				t.Fatal("invalid invocation ran", err, calls)
			}
		})
	}
}

// TestMinionRejectsOldNode keeps unsupported interpreters from changing project dependencies.
func TestMinionRejectsOldNode(t *testing.T) {
	calls := 0
	// The only allowed effect is interpreter version detection.
	run := func(_ context.Context, _, _ string, _ []string, _ io.Reader, out, _ io.Writer) error {
		calls++
		_, err := io.WriteString(out, "v22.0.0\n")
		return err
	}
	_, err := invoke([]string{"minion", "install"}, "", Options{CWD: t.TempDir(), Home: t.TempDir(), RunMinion: run})
	// No npm process may follow an unsupported runtime.
	if err == nil || calls != 1 || !strings.Contains(err.Error(), "24") {
		t.Fatal(err, calls)
	}
}

// checkMinionInstall asserts the archive survives source cleanup and npm receives literal safe arguments.
func checkMinionInstall(t *testing.T, root, dir string, args []string) {
	t.Helper()
	archive := filepath.Join(root, ".minion", "framework", "fused-minion-0.1.0.tgz")
	want := []string{"install", "--ignore-scripts", "--no-audit", "--no-fund", "--save", archive}
	// No temporary or remote package source belongs in the saved dependency manifest.
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") || dir != root {
		t.Fatalf("unsafe install: %q %q", dir, args)
	}
	data, err := os.ReadFile(archive)
	// A saved path must already contain the exact selected archive.
	if err != nil || string(data) != "fixture" {
		t.Fatal("archive was not preserved", err)
	}
	installMinionFixture(t, root)
}

// checkMinionPrepare confirms installation resumes the originally requested command.
func checkMinionPrepare(t *testing.T, executable, config string, args []string) {
	t.Helper()
	// A successful install never changes the selected config or action.
	if executable != "node" || len(args) != 3 || args[1] != "prepare" || args[2] != config {
		t.Fatalf("unexpected delegation: %q %q", executable, args)
	}
}

// TestMinionSignalChild is the subprocess fixture used to observe graceful signal forwarding.
func TestMinionSignalChild(t *testing.T) {
	// Normal test runs do not enter the standalone child protocol.
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "minion-signal-child" {
		return
	}
	root := os.Args[len(os.Args)-1]
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	// Mark readiness only after the interrupt handler has been registered.
	if err := os.WriteFile(filepath.Join(root, "ready"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	<-interrupt
	// This marker distinguishes graceful cancellation from forced termination.
	if err := os.WriteFile(filepath.Join(root, "stopped"), []byte("stopped"), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestMinionProcessForwardsCancellation exercises a real subprocess without requiring Node or npm.
func TestMinionProcessForwardsCancellation(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	// The current test executable supplies a platform-native signal fixture.
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &session{ctx: ctx}
	done := make(chan error, 1)
	// Execute the real cancellation path rather than the injected process mock.
	go func() {
		done <- s.runMinionProcess(root, executable, []string{"-test.run=^TestMinionSignalChild$", "minion-signal-child", root}, nil, io.Discard, io.Discard)
	}()
	// Give the child time to register its handler before cancellation.
	if err := waitMinionSignalReady(root); err != nil {
		t.Fatal(err)
	}
	cancel()
	// A graceful signal must finish well before the forced termination deadline.
	select {
	case err := <-done: // Successful shutdown should not appear as a command failure.
		if err != nil { // The child exits normally after its interrupt handler runs.
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second): // Bound regressions instead of hanging the suite.
		t.Fatal("Minion child did not stop after cancellation")
	}
	// The child must have run its cleanup before exiting.
	if _, err := os.Stat(filepath.Join(root, "stopped")); err != nil {
		t.Fatal("Minion child was killed without graceful cleanup", err)
	}
}

// waitMinionSignalReady waits for a bounded fixture handshake rather than relying on process timing.
func waitMinionSignalReady(root string) error {
	deadline := time.Now().Add(5 * time.Second)
	// Polling a private local marker keeps this test independent of TCP ports and Node.
	for time.Now().Before(deadline) {
		// Readiness is published after signal.Notify, so cancellation cannot race setup.
		if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("Minion signal fixture did not start")
}
