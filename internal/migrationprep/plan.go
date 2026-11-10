// Package migrationprep reviews and reversibly prepares a Docker host without migrating workloads.
package migrationprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
)

// Options selects an independent, new backup location and explicit preparation actions.
type Options struct {
	StateDir      string `json:"stateDirectory"`
	BackupDir     string `json:"backupDirectory"`
	DisableSwap   bool   `json:"disableSwap"`
	QuarantineK3s bool   `json:"quarantineK3s"`
}

// Swap records existing activation parameters without reading swap contents.
type Swap struct {
	Path     string `json:"path"`
	Priority string `json:"priority"`
	Unit     string `json:"unit"`
}

// Asset binds a standard cluster asset to a complete tree digest before quarantine.
type Asset struct {
	Path   string `json:"path"`
	Digest string `json:"sha256"`
	Bytes  uint64 `json:"bytes"`
}

// Plan separates reversible preparation from runtime migration and destructive cleanup.
type Plan struct {
	Options
	Swaps       []Swap   `json:"swaps"`
	SwapUnits   []string `json:"swapUnits"`
	Assets      []Asset  `json:"assets"`
	FstabDigest string   `json:"fstabDigest"`
	Errors      []string `json:"errors"`
	Changes     []string `json:"changes"`
	Fingerprint string   `json:"fingerprint"`
}

// clusterPaths includes whole configuration trees, drop-ins and local persistent data.
var clusterPaths = []string{
	"/etc/systemd/system/k3s.service", "/etc/systemd/system/k3s.service.d",
	"/etc/systemd/system/k3s.service.env", "/usr/local/bin/k3s",
	"/etc/rancher/k3s", "/var/lib/rancher/k3s", "/var/lib/rancher/k3s-agent",
	"/var/lib/kubelet", "/var/lib/cni", "/etc/cni/net.d",
}

// Inspect performs only bounded read operations, including on a running Docker controller.
func Inspect(ctx context.Context, o Options) (Plan, error) {
	return inspect(ctx, o, localHost{})
}

// inspect binds reviewed files while keeping volatile memory observations out of the fingerprint.
func inspect(ctx context.Context, o Options, h host) (Plan, error) {
	p := Plan{Options: o, Swaps: []Swap{}, SwapUnits: []string{}, Assets: []Asset{}, Errors: []string{}, Changes: []string{}}
	// Reject unsafe targets before scanning privileged state.
	if err := validateOptions(o); err != nil {
		return p, err
	}
	// Restrict production preparation to supported root-owned Linux installations.
	if err := h.platform(o.StateDir); err != nil {
		p.Errors = append(p.Errors, err.Error())
	}
	// Collect only the actions explicitly selected by this review.
	if o.DisableSwap {
		inspectSwap(ctx, h, &p)
	}
	// No adoption or stop operation is implied by finding old Kubernetes files.
	if o.QuarantineK3s {
		inspectCluster(ctx, h, &p)
	}
	// Existing journals are never overwritten by another application.
	if _, err := os.Lstat(o.BackupDir); !os.IsNotExist(err) {
		p.Errors = append(p.Errors, "Preparation backup directory must be new and absent")
	}
	sort.Strings(p.Errors)
	p.Fingerprint = fingerprint(p)
	return p, nil
}

// validateOptions confines backup material outside every source and controller state tree.
func validateOptions(o Options) error {
	// Require explicit action selection and absolute, normalized locations.
	if (!o.DisableSwap && !o.QuarantineK3s) || !cleanAbsolute(o.StateDir) || !cleanAbsolute(o.BackupDir) {
		return fmt.Errorf("Select preparation actions and absolute normalized state/backup directories")
	}
	// Prevent backups from being quarantined together with their own sources.
	for _, p := range append(append([]string{}, clusterPaths...), o.StateDir, "/etc", "/usr", "/run", "/proc", "/sys", "/dev") {
		// Related trees cannot safely serve as independent preparation backups.
		if within(o.BackupDir, p) || within(p, o.BackupDir) {
			return fmt.Errorf("Preparation backups must be outside controller, cluster and system configuration trees")
		}
	}
	return protectedParent(filepath.Dir(o.BackupDir))
}

// cleanAbsolute rejects path aliases and characters interpreted by host tools.
func cleanAbsolute(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
}

// within compares path components rather than unsafe textual prefixes.
func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(os.PathSeparator))
}

// protectedParent rejects symlinked backup ancestry; the final parent must already exist.
func protectedParent(p string) error {
	info, err := os.Lstat(p)
	// An unknown or redirected parent cannot receive root backup material.
	if err != nil {
		return err
	}
	// macOS's standard /tmp alias is used only by local tests, never by Linux execution.
	if runtime.GOOS == "darwin" && (p == "/tmp" || p == "/var") {
		return nil
	}
	// Writable or symlinked directories could redirect a privileged archive.
	if !info.IsDir() || !trustedDirectory(info) {
		return fmt.Errorf("Backup ancestors must be real, non-writable directories: %s", p)
	}
	// The filesystem root terminates the ancestor inspection.
	if filepath.Dir(p) == p {
		return nil
	}
	return protectedParent(filepath.Dir(p))
}

// trustedDirectory permits private owned trees and the root-owned sticky temporary ancestor.
func trustedDirectory(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	// An unrecognized owner cannot protect root recovery material.
	if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
		return false
	}
	return info.Mode()&0022 == 0 || (stat.Uid == 0 && info.Mode()&os.ModeSticky != 0)
}

// fingerprint excludes its own value and binds all selected actions and observed source bytes.
func fingerprint(p Plan) string {
	p.Fingerprint = ""
	raw, _ := json.Marshal(p)
	return digest(raw)
}

// digest consistently hashes configuration, archive and journal review bytes.
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
