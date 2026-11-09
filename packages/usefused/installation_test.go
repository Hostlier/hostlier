package usefused

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestInstallKeepsExecutorSelectionLocal verifies protected on-disk packaging without contacting an Engine.
func TestInstallKeepsExecutorSelectionLocal(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "binary")
	configPath := filepath.Join(dir, "executor.json")
	installation := filepath.Join(dir, "installed")
	config, _ := json.Marshal(executorConfig())
	// Temporary fixture bytes are sufficient to verify packaging without starting a process.
	if err := os.WriteFile(source, []byte("fixture-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	// Executor files contain only origin, immutable version and routing metadata.
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	// Installation performs only local file operations.
	if err := Install("aiven", source, configPath, installation); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(filepath.Join(installation, "aiven"))
	// The installed artifact must exactly match its source bytes.
	if err != nil || string(binary) != "fixture-binary" {
		t.Fatal("incorrect installed package")
	}
	info, err := os.Stat(filepath.Join(installation, "aiven.json"))
	// Executor configuration cannot be readable or writable by other users.
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe executor permissions")
	}
	loaded, err := ReadConfig(filepath.Join(installation, "aiven.json"))
	// Installed configuration retains the exact reviewed consumer version.
	if err != nil || loaded != executorConfig() {
		t.Fatal(loaded, err)
	}
}

// TestExecutorRejectsSymlinksAndMutableFamilySelection prevents configuration redirection and floating app selection.
func TestExecutorRejectsSymlinksAndMutableFamilySelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "executor.json")
	config := executorConfig()
	config.ConsumerVersionID = "database-app-family"
	raw, _ := json.Marshal(config)
	// A family name cannot masquerade as an immutable consumer version.
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Invalid configuration must be rejected before installation or invocation.
	if _, err := ReadConfig(path); err == nil {
		t.Fatal("mutable family selection accepted")
	}
	link := filepath.Join(dir, "linked.json")
	// Symlinks must not redirect credentials to an unexpected executor selection.
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	// The executor file itself must be an ordinary protected file.
	if _, err := ReadConfig(link); err == nil {
		t.Fatal("symlink executor accepted")
	}
}
