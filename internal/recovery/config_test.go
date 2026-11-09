package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecoveryConfigurationProtectsKeys validates relocatable private credentials without making network requests.
func TestRecoveryConfigurationProtectsKeys(t *testing.T) {
	dir := t.TempDir()
	// Exercise each native recovery case independently.
	for name, value := range map[string]string{"access": "fixture-access", "secret": "fixture-secret", "key": strings.Repeat("a", 64)} {
		// Stop the fixture on an unexpected operation result.
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "recovery.yaml")
	configuration := "destinations:\n  - id: offsite\n    endpoint: %s\n    region: region\n    bucket: database\n    prefix: controller\n    accessKeyFile: access\n    secretKeyFile: secret\n    encryptionKeyFile: key\n"
	// Keep the callback bound to this fixture and its cleanup lifetime.
	writeConfig := func(endpoint string) {
		t.Helper()
		// Stop the fixture on an unexpected operation result.
		if err := os.WriteFile(path, []byte(fmt.Sprintf(configuration, endpoint)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("https://storage.example")
	loaded, err := Load(path)
	// Stop the fixture on an unexpected operation result.
	if err != nil || len(loaded) != 1 {
		t.Fatal(err)
	}
	// Exercise each native recovery case independently.
	for _, endpoint := range []string{"http://storage.example", "https://user:pass@storage.example", "https://storage.example/path", "https://storage.example?query=value"} {
		writeConfig(endpoint)
		// Stop the fixture on an unexpected operation result.
		if _, err = Load(path); err == nil {
			t.Fatal("unsafe endpoint accepted", endpoint)
		}
	}
	writeConfig("https://storage.example")
	checkPrivateRecoveryFiles(t, path, dir)
}

// checkPrivateRecoveryFiles rejects shared key material and symlink substitution before constructing an SDK client.
func checkPrivateRecoveryFiles(t *testing.T, path, dir string) {
	t.Helper()
	key := filepath.Join(dir, "key")
	// Stop the fixture on an unexpected operation result.
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	// Stop the fixture on an unexpected operation result.
	if _, err := Load(path); err == nil {
		t.Fatal("shared encryption key accepted")
	}
	// Stop the fixture on an unexpected operation result.
	if err := os.Chmod(key, 0600); err != nil {
		t.Fatal(err)
	}
	// Stop the fixture on an unexpected operation result.
	if err := os.Rename(key, key+".real"); err != nil {
		t.Fatal(err)
	}
	// Stop the fixture on an unexpected operation result.
	if err := os.Symlink(key+".real", key); err != nil {
		t.Fatal(err)
	}
	// Stop the fixture on an unexpected operation result.
	if _, err := Load(path); err == nil {
		t.Fatal("symlinked encryption key accepted")
	}
}
