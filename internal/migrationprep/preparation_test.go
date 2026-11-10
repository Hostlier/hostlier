package migrationprep

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureHost exposes deterministic read-only host facts without invoking privileged tools.
type fixtureHost struct {
	files                                         map[string][]byte
	active, processes, mounts, manager, unitState string
	broken                                        bool
}

// platform keeps the isolated fixture independent of its developer workstation.
func (*fixtureHost) platform(string) error { return nil }

// read serves only known kernel/configuration fixtures and fails unknown paths closed.
func (h *fixtureHost) read(path string) ([]byte, error) {
	// Mount inspection uses a separate mutable fixture for orphan detection.
	if path == "/proc/self/mountinfo" {
		return []byte(h.mounts), nil
	}
	raw, ok := h.files[path]
	// Missing files retain ordinary absent-file semantics.
	if !ok {
		return nil, os.ErrNotExist
	}
	return raw, nil
}

// command returns bounded fixed-tool responses and rejects every unexpected invocation.
func (h *fixtureHost) command(_ context.Context, file string, args ...string) ([]byte, error) {
	// Uncertain process inspection must remain a blocker.
	if h.broken {
		return nil, fmt.Errorf("fixture unavailable")
	}
	// Kernel swap mappings resolve to a valid systemd unit.
	if file == "systemd-escape" {
		return []byte("swapfile.swap\n"), nil
	}
	// Process snapshots never execute workload binaries.
	if file == "ps" {
		return []byte(h.processes), nil
	}
	// Only fixed systemd read operations are part of this fixture contract.
	if file != "systemctl" {
		return nil, fmt.Errorf("unexpected tool: %s", file)
	}
	// No native swap definitions exist unless the test explicitly adds one.
	if args[0] == "list-units" || args[0] == "list-unit-files" {
		return nil, nil
	}
	return h.systemdShow(args[1])
}

// systemdShow supplies one fixed service or swap-unit observation.
func (h *fixtureHost) systemdShow(unit string) ([]byte, error) {
	// Existing swap-manager services require a separate persistent-policy review.
	if unit == h.manager {
		return []byte("LoadState=loaded\n"), nil
	}
	// Simulate an existing or externally masked swap mapping.
	if strings.HasSuffix(unit, ".swap") {
		return []byte("UnitFileState=" + h.unitState + "\n"), nil
	}
	// k3s may be live while all other Kubernetes units are absent.
	if unit == "k3s.service" && h.active != "" {
		return []byte("LoadState=loaded\nActiveState=" + h.active + "\nFragmentPath=/etc/systemd/system/k3s.service\nUnitFileState=disabled\n"), nil
	}
	return []byte("LoadState=not-found\nActiveState=inactive\nFragmentPath=\nUnitFileState=\n"), nil
}

// asset makes the read-only fixture an empty standard-layout host.
func (*fixtureHost) asset(path string) (Asset, error) { return Asset{Path: path}, nil }

// capacity gives the deterministic fixture adequate backup storage.
func (*fixtureHost) capacity(string, uint64) error { return nil }

// newFixture supplies enough memory and a conventional fstab swap mapping.
func newFixture() *fixtureHost {
	return &fixtureHost{files: map[string][]byte{
		"/proc/swaps":   []byte("Filename Type Size Used Priority\n/swapfile file 1048576 12 -2\n"),
		"/proc/meminfo": []byte("MemTotal: 8388608 kB\nMemAvailable: 4194304 kB\n"),
		"/etc/fstab":    []byte("UUID=root / ext4 defaults 0 1\n/swapfile none swap sw 0 0\n"),
	}}
}

// TestPreparationPreviewIsReadOnly proves a complete review neither creates backups nor rewrites fstab.
func TestPreparationPreviewIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	h := newFixture()
	o := Options{StateDir: filepath.Join(dir, "state"), BackupDir: filepath.Join(dir, "backup"), DisableSwap: true, QuarantineK3s: true}
	p, err := inspect(t.Context(), o, h)
	// Both action scopes must be visible without an apply boundary.
	if err != nil || len(p.Errors) > 0 || len(p.Swaps) != 1 || p.Fingerprint == "" {
		t.Fatal(p, err)
	}
	// Even the backup directory remains absent after inspection.
	if _, err := os.Lstat(o.BackupDir); !os.IsNotExist(err) {
		t.Fatal("preview created backup directory")
	}
	// Editing source configuration invalidates the exact reviewed fingerprint.
	h.files["/etc/fstab"] = append(h.files["/etc/fstab"], []byte("# changed\n")...)
	changed, err := inspect(t.Context(), o, h)
	// Review identity is bound to persistent host policy bytes.
	if err != nil || changed.Fingerprint == p.Fingerprint {
		t.Fatal("stale fstab review accepted", err)
	}
}

// TestPreparationRefusesLiveOrUncertainClusters covers service, orphan, mount and probe failures.
func TestPreparationRefusesLiveOrUncertainClusters(t *testing.T) {
	cases := []*fixtureHost{newFixture(), newFixture(), newFixture(), newFixture()}
	cases[0].active = "active"
	cases[1].processes = "containerd-shim-runc-v2 /run/k3s/containerd/x\n"
	cases[2].mounts = "1 0 0:1 / /var/lib/kubelet/pods/x rw - tmpfs tmpfs rw\n"
	cases[3].broken = true
	// Each independent ownership signal must block quarantine.
	for i, h := range cases {
		// Failure must remain a blocker rather than become evidence of absence.
		if err := clusterIdle(t.Context(), h); err == nil {
			t.Fatalf("unsafe cluster case %d accepted", i)
		}
	} // Failures cannot be treated as absence.
}

// TestSwapSafety checks memory reserve, malformed activation paths, preexisting masks and managers.
func TestSwapSafety(t *testing.T) {
	h := newFixture()
	h.files["/proc/meminfo"] = []byte("MemTotal: 8388608 kB\nMemAvailable: 1024 kB\n")
	// Unknown or inadequate RAM prevents swapoff regardless of approval.
	if err := memoryHeadroom(h, 12<<10); err == nil {
		t.Fatal("unsafe swapoff admitted")
	}
	// Escaped kernel filenames must not be handed to privileged utilities.
	if _, _, err := parseSwaps([]byte("Filename Type Size Used Priority\n/a\\040b file 1024 0 -2\n")); err == nil {
		t.Fatal("ambiguous path admitted")
	}
	h = newFixture()
	h.unitState = "masked"
	p := Plan{Swaps: []Swap{{Path: "/swapfile", Unit: "swapfile.swap"}}}
	// Preparation may never adopt a mask that belongs to an administrator.
	if err := inspectSwapUnits(t.Context(), h, &p); err == nil {
		t.Fatal("preexisting mask adopted")
	}
	h = newFixture()
	h.manager = "dphys-swapfile.service"
	// A generator which calls swapon itself needs a separate policy change.
	if err := swapManagersAbsent(t.Context(), h); err == nil {
		t.Fatal("swap manager ignored")
	}
}

// TestFstabEditingPreservesUnrelatedConfiguration covers exact comments, whitespace and missing final newline.
func TestFstabEditingPreservesUnrelatedConfiguration(t *testing.T) {
	raw := []byte("# /old none swap sw 0 0\nUUID=x / ext4 defaults 0 1\n  /swapfile none swap sw 0 0")
	want := "# /old none swap sw 0 0\nUUID=x / ext4 defaults 0 1\n# hostlier-preparation:   /swapfile none swap sw 0 0"
	// Only the active swap entry receives a marker; the original bytes remain recoverable.
	if string(disabledFstab(raw)) != want {
		t.Fatal(string(disabledFstab(raw)))
	}
}

// TestQuarantineRecoveryRejectsReplacementAndChangedData proves rollback never overwrites new cluster bytes.
func TestQuarantineRecoveryRejectsReplacementAndChangedData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	// A fixed original fixture serves as the cold recovery source.
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := (localHost{}).asset(path)
	// The full source bytes must be fingerprinted before quarantine.
	if err != nil {
		t.Fatal(err)
	}
	r := RetainedAsset{Asset: a, Quarantine: path + ".quarantine"}
	// Same-filesystem rename retains the exact original object.
	if err := os.Rename(path, r.Quarantine); err != nil {
		t.Fatal(err)
	}
	// An untouched quarantine with an absent destination can be restored.
	if err := restorableAsset(r); err != nil {
		t.Fatal(err)
	}
	// A replacement at the old path is always an explicit reconciliation blocker.
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	// Rollback must retain both sides instead of replacing the newer source.
	if err := restorableAsset(r); err == nil {
		t.Fatal("replacement overwritten")
	}
	// Remove only the test-owned replacement to exercise quarantine corruption independently.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A modified retained source cannot be silently accepted as the original.
	if err := os.WriteFile(r.Quarantine, []byte("changed!"), 0600); err != nil {
		t.Fatal(err)
	}
	// Equal-size source changes still invalidate the byte-level digest.
	if err := restorableAsset(r); err == nil {
		t.Fatal("changed quarantine accepted")
	}
}

// TestPreparationRejectsApprovalAndPathBypasses checks guard rails before any host mutation.
func TestPreparationRejectsApprovalAndPathBypasses(t *testing.T) {
	// A broad boolean cannot turn a blank or tampered review into executable authority.
	if err := Apply(t.Context(), Plan{}, true); err == nil {
		t.Fatal("blank plan applied")
	}
	// No approval must fail before taking locks or reading privileged state.
	if err := Apply(t.Context(), Plan{}, false); err == nil {
		t.Fatal("unapproved preparation")
	}
	// Source-adjacent backup locations are outside the independent recovery contract.
	if err := validateOptions(Options{StateDir: "/var/lib/hostlier", BackupDir: "/var/lib/rancher/k3s/backup", QuarantineK3s: true}); err == nil {
		t.Fatal("self-referential backup accepted")
	}
}

// TestClusterAssetRejectsSymlinkAncestor prevents apparent standard paths redirecting an archive elsewhere.
func TestClusterAssetRejectsSymlinkAncestor(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	alias := filepath.Join(dir, "alias")
	// Create only test-owned source data and an ancestor redirection.
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	// Existing regular source bytes do not make their aliased ancestry safe.
	if err := os.WriteFile(filepath.Join(real, "db"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	// Preparation must inspect links rather than following them into another tree.
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	// No archive or quarantine can derive authority through that redirected parent.
	if _, err := (localHost{}).asset(filepath.Join(alias, "db")); err == nil {
		t.Fatal("redirected ancestry accepted")
	}
}

// TestCompletedControllerUpdateReceipt separates durable success from active or uncertain maintenance.
func TestCompletedControllerUpdateReceipt(t *testing.T) {
	id := strings.Repeat("a", 32)
	state := t.TempDir()
	dir := filepath.Join(state, "controller-updates", id)
	// The fixture uses the same protected receipt layout as the real detached runner.
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "record.json")
	// Only exact accepted success may release a retained controller update ID.
	for _, status := range []string{"queued", "running", "failed", "interrupted", "succeeded"} {
		raw := fmt.Sprintf(`{"plan":{"id":%q},"requestId":"accepted","job":{"id":%q,"status":%q,"step":"completed"}}`, id, id, status)
		// Every case is independently published as a private, complete receipt.
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		// Failed and uncertain execution remain blocked even when the source runtime is Docker.
		if completedControllerUpdate(state, id) != (status == "succeeded") {
			t.Fatalf("wrong reservation classification: %s", status)
		}
	}
	// A moved or foreign receipt must never borrow another update's completion.
	if successfulUpdateReceipt([]byte(`{"plan":{"id":"foreign"},"job":{"status":"succeeded"}}`), id) {
		t.Fatal("foreign receipt accepted")
	}
}
