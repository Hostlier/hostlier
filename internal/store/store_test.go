package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDurabilityEncryptionLockAndMissingKey checks durable encrypted state, exclusive writers and fail-closed key loss.
func TestDurabilityEncryptionLockAndMissingKey(t *testing.T) {
	dir := t.TempDir()
	state := map[string]string{}
	s, err := Open(dir, &state)
	// Require the first writer to open state successfully.
	if err != nil {
		t.Fatal(err)
	}
	// Reject a second writer while the first process holds its lock.
	if second, err := Open(dir, &state); err == nil {
		second.Close()
		t.Fatal("concurrent writer accepted")
	}
	encrypted, err := s.Encrypt("unique private value")
	// Require successful encryption before testing disk contents.
	if err != nil {
		t.Fatal(err)
	}
	state["secret"] = encrypted
	// Persist the encrypted snapshot before reopening it.
	if err = s.Save(state); err != nil {
		t.Fatal(err)
	}
	s.Close()
	data, _ := os.ReadFile(filepath.Join(dir, "state.db"))
	// Ensure plaintext is absent from the durable state document.
	if strings.Contains(string(data), "unique private value") {
		t.Fatal("plaintext persisted")
	}
	s, err = Open(dir, &state)
	// Reopen the state using the retained original key.
	if err != nil {
		t.Fatal(err)
	}
	value, err := s.Decrypt(state["secret"])
	// Verify authenticated decryption restores exactly the original value.
	if err != nil || value != "unique private value" {
		t.Fatal(value, err)
	}
	assertKeyPermissions(t, dir)
	s.Close()
	os.Remove(filepath.Join(dir, "secret.key"))
	// Reject reopening state after its original key has been removed.
	if bad, err := Open(dir, &state); err == nil {
		bad.Close()
		t.Fatal("missing key replaced")
	}
}

// assertKeyPermissions checks the persistent key remains owner-only.
func assertKeyPermissions(t *testing.T, dir string) {
	stat, _ := os.Stat(filepath.Join(dir, "secret.key"))
	// Ensure the persistent encryption key remains owner-only.
	if stat.Mode().Perm() != 0600 {
		t.Fatal(stat.Mode())
	}
}
