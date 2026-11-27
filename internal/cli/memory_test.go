package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryCLIValidationPlanningAndMountArguments(t *testing.T) {
	dir := t.TempDir()
	yaml := "version: 3\nkind: memory\nname: library\nexports:\n  files:\n    path: /existing/only/on/server\n    protocols: [webdav, s3]\n"
	if err := os.WriteFile(filepath.Join(dir, ".hostlier"), []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeClient{}
	options := Options{CWD: dir, Client: f}
	out, err := invoke([]string{"memory", "validate", "--json"}, "", options)
	if err != nil || !strings.Contains(out, `"valid":true`) {
		t.Fatalf("validate %s %v", out, err)
	}
	if len(f.calls) != 0 {
		t.Fatal("offline validation contacted controller")
	}
	_, err = invoke([]string{"memory", "plan", "--json"}, "", options)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].path != "/v1/plans" {
		t.Fatalf("unexpected calls %#v", f.calls)
	}
	for _, osname := range []string{"darwin", "linux"} {
		binary, args, err := memoryMountCommand(osname, "https://storage/memory/webdav/library/files/", "/Volumes/My files")
		if err != nil || binary == "" || args[len(args)-1] != "/Volumes/My files" {
			t.Fatalf("mount %s %v %v", binary, args, err)
		}
	}
	if _, _, err = memoryMountCommand("windows", "https://storage", "x"); err == nil {
		t.Fatal("unsupported mount platform")
	}
	if _, err = invoke([]string{"memory", "--help"}, "", Options{}); err != nil {
		t.Fatal(err)
	}
}
func TestMemoryCLIGrantSecretFile(t *testing.T) {
	dir := t.TempDir()
	f := &fakeClient{reply: func(c call) (any, error) {
		return map[string]any{"username": "grant-id", "password": "private-password", "expiresAt": 1234, "readOnly": false}, nil
	}}
	out, err := invoke([]string{"memory", "grant", "library", "files", "--allow", "write", "--for", "1h", "--output", "grant.json"}, "", Options{CWD: dir, Client: f})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "private-password") {
		t.Fatal("credential printed")
	}
	info, err := os.Stat(filepath.Join(dir, "grant.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential mode %v %v", info, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "grant.json"))
	if !strings.Contains(string(data), "private-password") {
		t.Fatal("credential missing")
	}
	before := len(f.calls)
	if _, err = invoke([]string{"memory", "grant", "library", "files", "--allow", "write", "--output", "grant.json"}, "", Options{CWD: dir, Client: f}); err == nil {
		t.Fatal("overwrote existing credentials")
	}
	if len(f.calls) != before {
		t.Fatal("issued grant before output conflict")
	}
}

func TestMemoryCLILifecycleCannotTouchCompute(t *testing.T) {
	f := &fakeClient{reply: func(c call) (any, error) {
		return map[string]any{"spec": map[string]any{"version": 1, "name": "compute"}}, nil
	}}
	if _, err := invoke([]string{"memory", "delete", "compute"}, "", Options{Client: f}); err == nil {
		t.Fatal("memory delete accepted compute application")
	}
	if len(f.calls) != 1 || f.calls[0].method != "GET" {
		t.Fatalf("unexpected mutation: %#v", f.calls)
	}
	if _, err := invoke([]string{"memory"}, "", Options{}); err != nil {
		t.Fatal(err)
	}
}
