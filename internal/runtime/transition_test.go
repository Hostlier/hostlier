package runtime

import (
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"testing"
)

// TestDeploymentIntentOwnership rejects malformed or externally redirected cleanup authority.
func TestDeploymentIntentOwnership(t *testing.T) {
	r := &Runtime{options: Options{StateDir: t.TempDir()}, owner: "fixture"}
	p := model.Project{Version: 1, Name: "example"}
	// A valid durable record retains the exact operation identity and candidate name.
	if err := r.beginIntent(p, "approved"); err != nil {
		t.Fatal(err)
	}
	intent, err := r.readIntent(p.Name)
	// Correctly written identity survives reopening without mutation.
	if err != nil || intent.Operation != "approved" || intent.Candidate != r.intentCandidate(p.Name, "approved") {
		t.Fatalf("intent=%+v error=%v", intent, err)
	}
	path := r.intentPath(p.Name)
	// A world-readable journal must not authorize recovery even if its identity is unchanged.
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	assertRejectedIntent(t, r, p.Name)
	// Replace only test-owned evidence with an invalid cross-application candidate.
	if err = os.WriteFile(path, []byte(`{"application":"example","operation":"approved","candidate":"foreign"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Existing file permissions need explicit restoration after WriteFile.
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	assertRejectedIntent(t, r, p.Name)
	checkIntentSymlink(t, r, p.Name)
}

// assertRejectedIntent requires invalid evidence to fail closed instead of becoming an empty transaction.
func assertRejectedIntent(t *testing.T, r *Runtime, name string) {
	t.Helper()
	// Recovery must surface malformed ownership evidence as a typed, inspectable error.
	if _, err := r.readIntent(name); !hasCode(err, "RECOVERY_STATE") {
		t.Fatalf("invalid recovery evidence accepted: %v", err)
	}
}

// checkIntentSymlink proves recovery metadata cannot redirect reads to another state object.
func checkIntentSymlink(t *testing.T, r *Runtime, name string) {
	t.Helper()
	path := r.intentPath(name)
	other := filepath.Join(r.options.StateDir, "unrelated")
	// Keep the referenced file inside the disposable test directory.
	if err := os.WriteFile(other, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	// Remove only the fixture journal before replacing it with a malicious link.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// Linked metadata must be rejected even when its filesystem target is readable.
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	assertRejectedIntent(t, r, name)
}

// TestDeploymentIntentRetainsPendingDrain preserves recovery authority until the retained source is retired.
func TestDeploymentIntentRetainsPendingDrain(t *testing.T) {
	r := &Runtime{options: Options{StateDir: t.TempDir()}, owner: "fixture"}
	p := model.Project{Version: 1, Name: "example"}
	// Record intent before simulating a successful cutover with incomplete retirement.
	if err := r.beginIntent(p, "approved"); err != nil {
		t.Fatal(err)
	}
	s := state{Apps: map[string]appRecord{p.Name: {PreviousRuntimeID: "retained-source"}}}
	// A cleanup attempt cannot erase a still-needed predecessor marker.
	if err := r.finishIntent(p, s); err != nil {
		t.Fatal(err)
	}
	intent, err := r.readIntent(p.Name)
	// Journal evidence must remain independently readable after incomplete source cleanup.
	if err != nil || intent == nil {
		t.Fatalf("pending drain lost evidence: %v", err)
	}
	s.Apps[p.Name] = appRecord{}
	// Once source retirement is durable the auxiliary journal can disappear.
	if err = r.finishIntent(p, s); err != nil {
		t.Fatal(err)
	}
	intent, err = r.readIntent(p.Name)
	// A completed transition does not accumulate recovery authority indefinitely.
	if err != nil || intent != nil {
		t.Fatalf("resolved intent retained: %+v %v", intent, err)
	}
}
