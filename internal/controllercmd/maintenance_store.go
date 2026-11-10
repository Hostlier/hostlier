package controllercmd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"hostlier/internal/model"
)

// serverMaintenance owns private release staging and receipts outside daemon state.
type serverMaintenance struct {
	stateDir, configPath, executable string
	mu                               sync.Mutex
	now                              func() time.Time
	prepare                          func(context.Context, *controllerUpdateRecord, string) error
	schedule                         func(context.Context, string, string, string) error
	checkHost                        func(string) error
	active                           func(context.Context, string) (bool, error)
}

// controllerUpdateRecord retains server-only execution coordinates and exact review meaning.
type controllerUpdateRecord struct {
	Plan                 model.ControllerUpdatePlan `json:"plan"`
	Actor                string                     `json:"actor"`
	Generation           int64                      `json:"generation"`
	InstallerFingerprint string                     `json:"installerFingerprint"`
	TargetSHA256         string                     `json:"targetSha256"`
	SourceSHA256         string                     `json:"sourceSha256"`
	RequestID            string                     `json:"requestId,omitempty"`
	Job                  model.ControllerUpdateJob  `json:"job"`
	QueuedAt             int64                      `json:"queuedAt,omitempty"`
	ConfigPath           string                     `json:"configPath"`
	Executable           string                     `json:"executable"`
	InitArgs             []string                   `json:"initArgs"`
}

// maintenanceID confines root-owned records, unit names and stage directories.
var maintenanceID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// newServerMaintenance binds all privileged paths to the installed daemon configuration.
func newServerMaintenance(stateDir, configPath string) *serverMaintenance {
	executable, _ := os.Executable()
	return &serverMaintenance{stateDir: stateDir, configPath: configPath, executable: executable, now: time.Now, prepare: prepareControllerRelease, schedule: scheduleControllerUpdate, checkHost: requireUpdateHost, active: controllerUpdateActive}
}

// stagePath never accepts a caller-supplied root execution path.
func (m *serverMaintenance) stagePath(id string) (string, error) {
	// The opaque server-generated identity is the only variable path component.
	if !maintenanceID.MatchString(id) {
		return "", model.Err("INVALID_REQUEST", "Invalid controller update identifier")
	}
	return filepath.Join(m.stateDir, "controller-updates", id), nil
}

// readRecord refuses links, public permissions and unexpectedly large private records.
func readControllerUpdate(dir string) (controllerUpdateRecord, error) {
	var record controllerUpdateRecord
	path := filepath.Join(dir, "record.json")
	info, err := os.Lstat(path)
	// Missing records are not authority to create or adopt a stage.
	if err != nil {
		return record, err
	}
	// Refuse links, shared records and unexpected executable file types.
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 2<<20 {
		return record, model.Err("UPDATE_STATE", "Unsafe controller update record")
	}
	raw, err := os.ReadFile(path)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return record, err
	}
	// Reject malformed retained or target data before treating it as executable authority.
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, model.Err("UPDATE_STATE", "Invalid controller update record")
	}
	// A moved record cannot acquire a different job or unit identity.
	if filepath.Base(dir) != record.Plan.ID {
		return record, model.Err("UPDATE_STATE", "Controller update identity mismatch")
	}
	return record, nil
}

// saveControllerUpdate commits a complete record before scheduling external work.
func saveControllerUpdate(dir string, record controllerUpdateRecord) error {
	raw, err := json.Marshal(record)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	return maintenanceAtomic(filepath.Join(dir, "record.json"), raw)
}

// maintenanceAtomic publishes only flushed owner-only metadata.
func maintenanceAtomic(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".receipt-")
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	// Complete and flush the private temporary receipt before publishing it.
	if err := writeMaintenanceFile(f, raw); err != nil {
		return err
	}
	// Publish the complete receipt atomically after all private writes succeed.
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeMaintenanceFile separates fallible data writes from the publication boundary.
func writeMaintenanceFile(f *os.File, raw []byte) error {
	defer f.Close()
	// Retained update metadata is visible only to its owner.
	if err := f.Chmod(0600); err != nil {
		return err
	}
	// Do not publish a receipt containing incomplete JSON bytes.
	if _, err := f.Write(raw); err != nil {
		return err
	}
	// Flush receipt bytes before replacing the previously committed record.
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// maintenanceHash verifies actual staged or installed executable bytes.
func maintenanceHash(path string) (string, error) {
	info, err := os.Lstat(path)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return "", err
	}
	// Refuse links, shared records and unexpected executable file types.
	if !info.Mode().IsRegular() {
		return "", model.Err("UPDATE_STATE", "Controller executable must be a regular file")
	}
	f, err := os.Open(path)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	// Hash or copy every executable byte before accepting its retained identity.
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyMaintenanceRunner keeps the initiating executable available after replacement.
func copyMaintenanceRunner(source, dir string) error {
	input, err := os.Open(source)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(filepath.Join(dir, "runner"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	defer output.Close()
	// Hash or copy every executable byte before accepting its retained identity.
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
}

// newMaintenanceID uses server entropy rather than accepting execution identifiers.
func newMaintenanceID() (string, error) {
	raw := make([]byte, 16)
	// Generate opaque stage identifiers only from operating-system entropy.
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// updateFingerprint binds every public review field except the derived fingerprint.
func updateFingerprint(plan model.ControllerUpdatePlan) string {
	plan.Fingerprint = ""
	raw, _ := json.Marshal(plan)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// maintenanceUnavailable reports a concise public installation prerequisite.
func maintenanceUnavailable(err error) error {
	// Preserve supported validation codes; hide subprocess output and filesystem details.
	if typed, ok := err.(*model.Error); ok {
		return typed
	}
	return model.Err("MAINTENANCE_FAILED", "Cannot prepare controller maintenance; inspect the controller installation and official release availability")
}
