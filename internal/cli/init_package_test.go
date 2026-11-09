package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitPackageInit(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": "invalid file from unrelated framework"})
	state := t.TempDir()
	dir := filepath.Join(state, "packages")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1" in
describe) echo '{"version":1,"name":"lantern","configSchema":{},"outputSchema":{},"hooks":["init"]}' ;;
init) cat >/dev/null; echo '{"version":1,"project":{"version":2,"name":"light","source":{"image":"light:v1"},"endpoint":{"exposure":"public","port":8080}},"files":{"src/owned.txt":"package-generated"}}' ;;
*) exit 1 ;;
esac
`
	path := filepath.Join(dir, "lantern")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"init", "--package", "lantern", "--state-dir", state}
	opts := initOptions(t, root)
	before := initTree(t, root)
	if out, err := invoke(append(args, "--dry-run"), "", opts); err != nil || !strings.Contains(out, "owned.txt") || len(initTree(t, root)) != len(before) {
		t.Fatal(out, err)
	}
	if _, err := invoke(args, "", opts); err != nil {
		t.Fatal(err)
	}
	project := initReadProject(t, filepath.Join(root, ".hostlier"))
	if _, enrolled := project.Packages["lantern"]; !enrolled || project.Package != "" || project.Name != "light" {
		t.Fatal(project)
	}
	if data, err := os.ReadFile(filepath.Join(root, "src", "owned.txt")); err != nil || string(data) != "package-generated" {
		t.Fatal(err)
	}
	if _, err := invoke(args, "", opts); err == nil {
		t.Fatal("reinitialized over existing files")
	}
	// A package without hooks uses the original capability document and must never enter init.
	old := strings.ReplaceAll(strings.ReplaceAll(script, "lantern", "legacy"), `,"hooks":["init"]`, "")
	if err := os.WriteFile(filepath.Join(dir, "legacy"), []byte(old), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, msg string }{{"legacy", "does not provide an init hook"}, {"absent", "not installed locally"}} {
		args[2] = tc.name
		if _, err := invoke(args, "", opts); err == nil || !strings.Contains(err.Error(), tc.msg) {
			t.Fatal(tc.name, err)
		}
	}
}

// TestStarterAddThenPackageInit exercises the command boundary using an installed initializer.
func TestStarterAddThenPackageInit(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": "unrelated invalid app metadata"})
	state := t.TempDir()
	dir := filepath.Join(state, "packages")
	// Install a local executable independent of unrelated app metadata.
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1" in
describe) echo '{"version":1,"name":"lamp","hooks":["init"],"configSchema":{"type":"object","properties":{"region":{"type":"string"}}},"outputSchema":{}}' ;;
init) cat | grep -q '"region":"west"' || exit 4
echo '{"version":1,"project":{"version":2,"name":"lamp","source":{"image":"lamp:v1"},"endpoint":{"exposure":"public","port":8080}},"files":{"starter.txt":"initialized"}}' ;;
*) exit 1 ;;
esac`
	writeContractFile(t, filepath.Join(dir, "lamp"), script)
	opts := initOptions(t, root)
	path := checkPlainInit(t, root, opts)
	add := []string{"package", "add", "lamp", "--state-dir", state}
	// Add may only discover the setup contract.
	if _, err := invoke(add, "", opts); err != nil {
		t.Fatal(err)
	}
	raw := readContractFile(t, path)
	// The declared input must now be editable.
	if !strings.Contains(raw, "region: null") {
		t.Fatal(raw)
	}
	// Only explicit initialization may scaffold the extra file.
	if _, err := os.Stat(filepath.Join(root, "starter.txt")); !os.IsNotExist(err) {
		t.Fatal("add ran init")
	}
	raw = strings.Replace(raw, "region: null", "region: west", 1) + "  second: {input: keep}\n"
	writeContractFile(t, path, raw)
	checkConfiguredInit(t, state, path, raw, opts)
}

// checkConfiguredInit verifies that init consumes one namespace without disturbing another.
func checkConfiguredInit(t *testing.T, state, path, raw string, opts Options) {
	t.Helper()
	args := []string{"init", "--package", "lamp", "--state-dir", state}
	// A dry run cannot mutate an already configured starter.
	if _, err := invoke(append(args, "--dry-run"), "", opts); err != nil || raw != readContractFile(t, path) {
		t.Fatal("preview changed starter", err)
	}
	// Exercise the real atomic replacement.
	if _, err := invoke(args, "", opts); err != nil {
		t.Fatal(err)
	}
	p := initReadProject(t, path)
	// The initializer fills the workload and preserves both package entries.
	if p.Name != "lamp" || p.Packages["lamp"]["region"] != "west" || p.Packages["second"]["input"] != "keep" || readContractFile(t, filepath.Join(filepath.Dir(path), "starter.txt")) != "initialized" {
		t.Fatal(p)
	}
}

// checkPlainInit verifies the starter contents and that preview leaves the directory alone.
func checkPlainInit(t *testing.T, root string, opts Options) string {
	t.Helper()
	// A preview should succeed even when app markers cannot be decoded.
	if _, err := invoke([]string{"init", "--dry-run"}, "", opts); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".hostlier")
	// Check the filesystem rather than just the reported dry-run status.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preview wrote manifest")
	}
	// Plain init should create only a package-neutral project.
	if _, err := invoke([]string{"init"}, "", opts); err != nil {
		t.Fatal(err)
	}
	before := readContractFile(t, path)
	// The minimal starter has no framework-selected identity or source.
	if before != "version: 2\npackages: {}\n" {
		t.Fatalf("unexpected starter: %s", before)
	}
	return path
}
