package runtime

import (
	"encoding/json"
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"testing"
)

// TestRecoveredMissingWriterRequiresOriginalPathAndAuthoritativeAbsence checks the clean-host exception.
func TestRecoveredMissingWriterRequiresOriginalPathAndAuthoritativeAbsence(t *testing.T) {
	dir := t.TempDir()
	r := &Runtime{options: Options{StateDir: dir}}
	absent := model.Err("HTTP_404", "absent")
	// An ordinary deployment must not inherit recovery privileges.
	if r.recoveredMissingWriter(absent) {
		t.Fatal("missing recovery marker accepted")
	}
	data, _ := json.Marshal(model.RecoveryOrigin{StateDirectory: dir})
	os.WriteFile(filepath.Join(dir, "recovery-origin.json"), data, 0600)
	// A validated original path and authoritative daemon absence permit fresh creation.
	if !r.recoveredMissingWriter(absent) {
		t.Fatal("valid recovered absence rejected")
	}
	// Ownership mismatches, daemon errors and successful inspections must fail closed.
	for _, err := range []error{nil, model.Err("OWNERSHIP", "unowned"), model.Err("HTTP_500", "failed")} {
		// Never convert an ambiguous response into authorization to transfer a volume.
		if r.recoveredMissingWriter(err) {
			t.Fatal("unsafe writer bypass")
		}
	}
	data, _ = json.Marshal(model.RecoveryOrigin{StateDirectory: dir + "-original"})
	os.WriteFile(filepath.Join(dir, "recovery-origin.json"), data, 0600)
	// Restaging metadata under a different path cannot acquire a new runtime namespace.
	if r.recoveredMissingWriter(absent) {
		t.Fatal("staging path accepted")
	}
}
