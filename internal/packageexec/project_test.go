package packageexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This fixture checks what actually crosses stdin and the process environment on both calls.
const projectFixture = `#!/bin/sh
case "$1" in
describe) echo '{"version":1,"name":"lantern","configSchema":{},"outputSchema":{},"hooks":["project-plan","project-package"],"projectPackageEnv":["LANTERN_CATALOG","LANTERN_UNREQUESTED"]}';;
project-plan)
  test -z "$LANTERN_CATALOG" || exit 1
  echo '{"version":1,"project":{"name":"reviewed"},"declarations":{"read":"catalog"}}';;
project-package)
  test "$LANTERN_CATALOG" = test && test -z "$LANTERN_UNREQUESTED" || exit 1
  test -x "$HOSTLIER_CLI" || exit 1
  request=$(cat)
  case "$request" in *'"declarations":{"read":"catalog"}'*) ;; *) exit 2;; esac
  target=$(printf '%s' "$request" | sed -n 's/^.*"outputDir":"\([^"]*\)".*$/\1/p')
  mkdir "$target/context"
  echo 'FROM scratch' > "$target/context/Dockerfile"
  echo '{"version":1,"artifact":{"kind":"container-context","path":"context"}}';;
*) exit 1;;
esac
`

// TestProjectHooksPinExecutableAndRestrictEnvironment crosses real process boundaries.
func TestProjectHooksPinExecutableAndRestrictEnvironment(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	t.Setenv("LANTERN_CATALOG", "test")
	t.Setenv("LANTERN_UNREQUESTED", "must-remain-private")
	t.Setenv("HOSTLIER_PACKAGE_ENV", "LANTERN_CATALOG")
	installed := filepath.Join(dir, "lantern")
	// Install the first executable in the protected package directory.
	if err := os.WriteFile(installed, []byte(projectFixture), 0700); err != nil {
		t.Fatal(err)
	}
	h, err := (Runner{Directory: dir}).OpenProject(context.Background(), "lantern", root)
	// Snapshot discovery is part of the protocol under test.
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	plan, err := h.Plan(context.Background(), "arm64")
	// Planning must work without the declared catalog credential.
	if err != nil {
		t.Fatal(err)
	}
	// Upgrading the installation after plan must not substitute another executable for packaging.
	if err := os.WriteFile(installed, []byte("#!/bin/sh\nexit 5"), 0700); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	result, err := h.Package(context.Background(), out, "arm64", plan.Project, plan.Declarations)
	// The snapshot must still accept its own plan and declarations.
	if err != nil {
		t.Fatal(err)
	}
	// Keep the returned subdirectory rather than silently building its parent.
	if result.Artifact.Path != "context" {
		t.Fatal(result)
	}
	// Verify the context was actually written to the requested directory.
	if _, err := os.Stat(filepath.Join(out, "context", "Dockerfile")); err != nil {
		t.Fatal(err)
	}
	// A retry must receive a fresh destination.
	if _, err := h.Package(context.Background(), out, "arm64", plan.Project, plan.Declarations); err == nil {
		t.Fatal("reused non-empty output directory")
	}
}

// TestProjectHookRejectsEscapingArtifact exercises both capability and containment checks.
func TestProjectHookRejectsEscapingArtifact(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	// This package advertises only packaging and returns a symlink into the source tree.
	if err := os.WriteFile(filepath.Join(dir, "lantern"), []byte(`#!/bin/sh
case "$1" in
describe) echo '{"version":1,"name":"lantern","configSchema":{},"outputSchema":{},"hooks":["project-package"]}';;
project-package)
  target=$(sed -n 's/^.*"outputDir":"\([^"]*\)".*$/\1/p')
  ln -s "$PWD" "$target/context"
  echo '{"version":1,"artifact":{"kind":"container-context","path":"context"}}';;
esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	h, err := (Runner{Directory: dir}).OpenProject(context.Background(), "lantern", root)
	// Take the same snapshot for both negative calls.
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	// An absent plan hook must be diagnosed before invoking the package.
	if _, err = h.Plan(context.Background(), "amd64"); err == nil || !strings.Contains(err.Error(), "does not provide") {
		t.Fatal(err)
	}
	// A filesystem escape takes precedence over a syntactically valid receipt.
	if _, err = h.Package(context.Background(), t.TempDir(), "amd64", json.RawMessage(`{"name":"example"}`), nil); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatal(err)
	}
}
