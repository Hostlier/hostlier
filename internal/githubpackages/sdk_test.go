package githubpackages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSDKUsesPinnedModuleAndEphemeralCredentials runs the actual child-process setup against a recording Go executable.
func TestSDKUsesPinnedModuleAndEphemeralCredentials(t *testing.T) {
	root := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$@" > arguments
printf '%s\n' "$GOPRIVATE" > private
printf '%s\n' "$HOSTLIER_SDK_GITHUB_TOKEN" > token
`
	// A recording Go command validates arguments and scoped environment without network access.
	if err := os.WriteFile(filepath.Join(root, "go"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	// Existing Go projects retain control over their module identity.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/package\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOPRIVATE", "example.test/private")
	sha := strings.Repeat("a", 40)
	// A tag must already be resolved before it reaches the dependency installer.
	if err := InstallSDK(context.Background(), root, sha, "sdk-test-token"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(root, "arguments"))
	private, _ := os.ReadFile(filepath.Join(root, "private"))
	token, _ := os.ReadFile(filepath.Join(root, "token"))
	// Tokens never appear in command arguments and existing private module settings survive.
	if string(args) != "get\ngithub.com/"+DefaultRepository+"/sdk/packagekit@"+sha+"\n" || !strings.Contains(string(private), "example.test/private,github.com/"+DefaultRepository) || string(token) != "sdk-test-token\n" {
		t.Fatal("incorrect SDK subprocess configuration")
	}
	// An unresolved ref cannot invoke Go even if the project is valid.
	if InstallSDK(context.Background(), root, "main", "token") == nil {
		t.Fatal("unresolved SDK ref accepted")
	}
	// Installation does not create an implicit module in another directory.
	if InstallSDK(context.Background(), t.TempDir(), sha, "token") == nil {
		t.Fatal("missing go.mod accepted")
	}
}
