package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecutableDependencies prevents either entry point from absorbing the other one's implementation.
func TestExecutableDependencies(t *testing.T) {
	cases := []struct {
		command   string
		forbidden []string
	}{
		{"hostlier", []string{"hostlier/internal/installer", "hostlier/internal/controller", "hostlier/internal/controllercmd", "hostlier/internal/runtime", "hostlier/internal/store", "hostlier/internal/recovery", "modernc.org/sqlite", "github.com/nats-io/nats-server", "github.com/aws/aws-sdk-go-v2"}},
		{"hostlier-controller", []string{"hostlier/internal/cli", "hostlier/internal/client", "hostlier/internal/localui", "hostlier/internal/skills", "hostlier/internal/githubpackages", "hostlier/internal/gitpackages"}},
	}
	// Inspect production dependency graphs rather than test-only imports.
	for _, test := range cases {
		command := exec.Command("go", "list", "-deps", "../"+test.command)
		output, err := command.CombinedOutput()
		// Incomplete graphs must fail instead of weakening the boundary check.
		if err != nil {
			t.Fatalf("%s: %v\n%s", test.command, err, output)
		}
		assertDependencies(t, test.command, string(output), test.forbidden)
	}
}

// assertDependencies matches full package paths and descendants without confusing shared helper names.
func assertDependencies(t *testing.T, command, graph string, forbidden []string) {
	t.Helper()
	// Every transitive production package participates in the separation guarantee.
	for _, dependency := range strings.Fields(graph) {
		// Prefixes cover external modules and their internal packages.
		for _, prefix := range forbidden {
			// Exact package boundaries avoid treating controllerbinary as controller.
			if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
				t.Errorf("%s links forbidden dependency %s", command, dependency)
			}
		}
	}
}

// TestSeparateExecutables exercises actual process forwarding and each independent command surface.
func TestSeparateExecutables(t *testing.T) {
	directory := t.TempDir()
	// Build the same two entry points distributed by the release script.
	for _, name := range []string{"hostlier", "hostlier-controller"} {
		output, err := exec.Command("go", "build", "-o", filepath.Join(directory, name), "../"+name).CombinedOutput()
		// Do not test stale executables after a failed build.
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, output)
		}
	}
	checkProcess(t, directory, "hostlier", []string{"help"}, false, "hostlier [command]")
	checkProcess(t, directory, "hostlier", []string{"app", "--help"}, false, "hostlier app [command]")
	checkProcess(t, directory, "hostlier-controller", []string{"help"}, false, "Usage: hostlier-controller run")
	checkProcess(t, directory, "hostlier-controller", []string{"connect", "--help"}, false, "hostlier server join MASTER")
	checkProcess(t, directory, "hostlier-controller", []string{"deploy"}, true, "unknown controller command")
	checkProcess(t, directory, "hostlier", []string{"controller", "backup", "--json"}, true, "requires --output")
	checkProcess(t, directory, "hostlier", []string{"agent", "run"}, true, "controller-fingerprint")
}

// checkProcess verifies user-visible output and success status across the executable boundary.
func checkProcess(t *testing.T, directory, name string, args []string, failure bool, want string) {
	t.Helper()
	output, err := exec.Command(filepath.Join(directory, name), args...).CombinedOutput()
	// Correct error status is as important as dispatching to the intended executable.
	if (err != nil) != failure || !strings.Contains(string(output), want) {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
}
