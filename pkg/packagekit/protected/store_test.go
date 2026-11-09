package protected

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestArtifacts verifies encryption, immutable handles and operation binding.
func TestArtifacts(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.Chmod(root, 0700)
	s, err := Open(root)
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{Owner: "app/step", Operation: "op", Config: "config", Bundle: "bundle", State: "revision"}
	h, err := s.Save(b, []byte("sentinel-private-plan"), time.Hour)
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, h.ID+".plan"))
	// Verify private artifact isolation and the expected failure boundary.
	if strings.Contains(string(raw), "sentinel") {
		t.Fatal("unencrypted artifact")
	}
	reopened, err := Open(root)
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil {
		t.Fatal(err)
	}
	data, err := reopened.Load(h, b)
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil || string(data) != "sentinel-private-plan" {
		t.Fatal("restart recovery", err)
	}
	changed := b
	changed.Operation = "other"
	// Verify private artifact isolation and the expected failure boundary.
	if _, err = s.Load(h, changed); err == nil {
		t.Fatal("cross-operation artifact")
	}
	altered := h
	altered.Expires = altered.Expires.Add(time.Hour)
	// Verify private artifact isolation and the expected failure boundary.
	if _, err = s.Load(altered, b); err == nil {
		t.Fatal("mutable expiry")
	}
	raw[len(raw)-1] ^= 1
	os.WriteFile(filepath.Join(root, h.ID+".plan"), raw, 0600)
	// Verify private artifact isolation and the expected failure boundary.
	if _, err = s.Load(h, b); err == nil {
		t.Fatal("tampered ciphertext")
	}
}

// TestWorkspaceAndLock verifies isolated directories and fail-closed ownership locks.
func TestWorkspaceAndLock(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.Chmod(root, 0700)
	s, err := Open(root)
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil {
		t.Fatal(err)
	}
	a, cleanA, err := s.Workspace("owner", "op")
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil {
		t.Fatal(err)
	}
	defer cleanA()
	b, cleanB, err := s.Workspace("owner", "op")
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil {
		t.Fatal(err)
	}
	defer cleanB()
	// Verify private artifact isolation and the expected failure boundary.
	if a == b {
		t.Fatal("shared workspace")
	}
	unlock, err := s.Lock("owner")
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil {
		t.Fatal(err)
	}
	// Verify private artifact isolation and the expected failure boundary.
	if _, err = s.Lock("owner"); err == nil {
		t.Fatal("overlapping operation")
	}
	unlock()
	again, err := s.Lock("owner")
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil {
		t.Fatal(err)
	}
	again()
}

// TestExpiredArtifact rejects an authenticated handle after its retention deadline.
func TestExpiredArtifact(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.Chmod(root, 0700)
	s, err := Open(root)
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{Owner: "owner", Operation: "op"}
	h, err := s.Save(b, []byte("plan"), time.Nanosecond)
	// Verify private artifact isolation and the expected failure boundary.
	if err != nil {
		t.Fatal(err)
	}
	// Verify private artifact isolation and the expected failure boundary.
	if _, err = s.Load(h, b); err == nil {
		t.Fatal("expired plan accepted")
	}
}
