package protected

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// TestBundleRejectsChangedMissingAndRedirectedFiles enforces the complete manifest boundary.
func TestBundleRejectsChangedMissingAndRedirectedFiles(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.Chmod(root, 0700)
	path := filepath.Join(root, "tool")
	data := []byte("pinned bytes")
	os.WriteFile(path, data, 0700)
	sum := sha256.Sum256(data)
	b := Bundle{Root: root, Platform: runtime.GOOS + "/" + runtime.GOARCH, Files: map[string]string{"tool": hex.EncodeToString(sum[:])}}
	// Verify the expected preparation and approval boundary.
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("changed"), 0700)
	// Verify the expected preparation and approval boundary.
	if b.Validate() == nil {
		t.Fatal("changed tool accepted")
	}
	os.Remove(path)
	// Verify the expected preparation and approval boundary.
	if b.Validate() == nil {
		t.Fatal("missing tool accepted")
	}
	os.Symlink(os.Args[0], path)
	// Verify the expected preparation and approval boundary.
	if b.Validate() == nil {
		t.Fatal("symlink accepted")
	}
}

// TestEnvironmentRejectsProcessInjection prevents inherited loader and CLI control variables.
func TestEnvironmentRejectsProcessInjection(t *testing.T) {
	// Exercise each controlled fixture path without real provider mutations.
	for _, name := range []string{"PATH", "HOME", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "TF_CLI_ARGS", "AWS_BAD=VALUE"} {
		// Verify the expected preparation and approval boundary.
		if allowedEnvironment(name) {
			t.Fatal("unsafe environment", name)
		}
	}
	// Verify the expected preparation and approval boundary.
	if !allowedEnvironment("AWS_ACCESS_KEY_ID") {
		t.Fatal("scoped credential rejected")
	}
}

// TestMaterializePreservesOwnerExecute verifies pinned programs remain runnable privately.
func TestMaterializePreservesOwnerExecute(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.Chmod(root, 0700)
	files := map[string]string{}
	// Exercise each controlled fixture path without real provider mutations.
	for name, mode := range map[string]os.FileMode{"program": 0755, "source": 0644} {
		data := []byte(name)
		// Verify the expected preparation and approval boundary.
		if err := os.WriteFile(filepath.Join(root, name), data, mode); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		files[name] = hex.EncodeToString(sum[:])
	}
	b := Bundle{Root: root, Platform: runtime.GOOS + "/" + runtime.GOARCH, Files: files}
	workspace, _ := filepath.EvalSymlinks(t.TempDir())
	os.Chmod(workspace, 0700)
	// Verify the expected preparation and approval boundary.
	if err := b.Materialize(workspace); err != nil {
		t.Fatal(err)
	}
	// Exercise each controlled fixture path without real provider mutations.
	for name, expected := range map[string]os.FileMode{"program": 0700, "source": 0600} {
		info, err := os.Stat(filepath.Join(workspace, name))
		// Verify the expected preparation and approval boundary.
		if err != nil || info.Mode().Perm() != expected {
			t.Fatalf("%s permission: %v %v", name, info, err)
		}
	}
	// Verify the expected preparation and approval boundary.
	if err := b.Materialize(workspace); err == nil {
		t.Fatal("existing destination overwritten")
	}
}

// TestEnvironmentOrderingAndNUL verifies deterministic clean environment construction.
func TestEnvironmentOrderingAndNUL(t *testing.T) {
	command := &exec.Cmd{}
	// Verify the expected preparation and approval boundary.
	if err := appendEnvironment(command, map[string]string{"AWS_Z": "z", "AWS_A": "a"}); err != nil {
		t.Fatal(err)
	}
	// Verify the expected preparation and approval boundary.
	if !reflect.DeepEqual(command.Env, []string{"AWS_A=a", "AWS_Z=z"}) {
		t.Fatal(command.Env)
	}
	// Verify the expected preparation and approval boundary.
	if appendEnvironment(command, map[string]string{"AWS_A": "x\x00y"}) == nil {
		t.Fatal("NUL accepted")
	}
}
