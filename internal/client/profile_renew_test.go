package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRenewUsesAtomicCredentialReference checks successful renewal and cleanup of the old token.
func TestRenewUsesAtomicCredentialReference(t *testing.T) {
	s := NewStore(t.TempDir())
	// The original profile uses the legacy name-based credential format.
	if err := s.Save("test", "https://controller.example.com", "old", "old-token"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.List()
	// A replacement credential must publish through the profile's atomic reference.
	if err := s.Renew("test", before.Servers["test"], "new", "new-token"); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Get("test")
	// Ordinary requests must resolve the replacement token transparently.
	if err != nil || saved.Token != "new-token" {
		t.Fatal("replacement missing", err)
	}
	data, _ := os.ReadFile(filepath.Join(s.Home, "credentials.json"))
	var secrets map[string]string
	_ = json.Unmarshal(data, &secrets)
	// Completed cleanup removes the prior token and retains only the new reference.
	if len(secrets) != 1 || secrets["device:new"] != "new-token" {
		t.Fatal("old credential retained")
	}
	// Concurrently stale enrollment cannot replace an already renewed identity.
	if err := s.Renew("test", before.Servers["test"], "later", "later-token"); err == nil {
		t.Fatal("stale profile replaced")
	}
}

// TestRenewFailedCredentialStagePreservesProfile checks local storage failures before publication.
func TestRenewFailedCredentialStagePreservesProfile(t *testing.T) {
	s := NewStore(t.TempDir())
	// Seed a valid public profile before simulating an unreadable credential document.
	if err := s.Save("test", "https://controller.example.com", "old", "old-token"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.List()
	// Corruption must be reported rather than silently discarding other credentials.
	if err := os.WriteFile(filepath.Join(s.Home, "credentials.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	err := s.Renew("test", before.Servers["test"], "new", "new-token")
	after, _ := s.List()
	// Publication must not happen if preserving existing credentials fails.
	if err == nil || before.Servers["test"] != after.Servers["test"] {
		t.Fatal("failed staging changed identity")
	}
}
