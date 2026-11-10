package migrationprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"hostlier/internal/store"
)

// host limits inspection to fixed file reads and bounded host commands.
type host interface {
	platform(string) error
	read(string) ([]byte, error)
	command(context.Context, string, ...string) ([]byte, error)
	asset(string) (Asset, error)
	capacity(string, uint64) error
}

// localHost supplies real privileged Linux inspection without shell evaluation.
type localHost struct{}

// platform requires an installed Docker source and refuses unresolved runtime changes.
func (localHost) platform(state string) error {
	// Other systems may render help but cannot authorize host preparation.
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return fmt.Errorf("Preparation requires root on the Linux Docker controller")
	}
	// State and its ancestry must remain protected from non-root path replacement.
	if err := protectedParent(state); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(state, "installation.json"))
	// Installation ownership must exist before changing host prerequisites.
	if err != nil {
		return err
	}
	var installed struct{ Runtime string }
	// Kubernetes hosts cannot prepare their own live runtime for quarantine.
	if json.Unmarshal(raw, &installed) != nil || installed.Runtime != "docker" {
		return fmt.Errorf("Preparation requires an installed Docker controller")
	}
	// An unresolved migration owns all cluster and source data boundaries.
	for _, name := range []string{"migration-active", "migration-preparation-active"} {
		// Unknown marker state cannot authorize another preparation.
		if _, err := os.Lstat(filepath.Join(state, name)); !os.IsNotExist(err) {
			return fmt.Errorf("Resolve %s before starting preparation", name)
		}
	}
	return dockerSourceIdle(state)
}

// dockerSourceIdle refuses an accepted controller restart before reserving host prerequisites.
func dockerSourceIdle(state string) error {
	var source struct {
		Version            int    `json:"version"`
		Runtime            string `json:"runtime"`
		ControllerUpdateID string `json:"controllerUpdateId"`
	}
	// Read only the committed state; preparation never opens a controller writer store.
	if err := store.Snapshot(state, &source); err != nil {
		return fmt.Errorf("Cannot inspect committed Docker controller state")
	}
	// A source runtime transition or accepted update must settle before host preparation.
	if source.Version != 2 || source.Runtime != "docker" || source.ControllerUpdateID != "" {
		return fmt.Errorf("Preparation requires a settled Docker controller without a pending controller update")
	}
	return nil
}

// read returns exact bytes without mutating the inspected file.
func (localHost) read(p string) ([]byte, error) { return os.ReadFile(p) }

// command prevents hanging probes and never invokes a shell or user-supplied executable.
func (localHost) command(ctx context.Context, file string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, file, args...).Output()
}

// asset fingerprints files, metadata and symbolic-link targets without following links.
func (localHost) asset(p string) (Asset, error) {
	a := Asset{Path: p}
	info, err := os.Lstat(p)
	// Absent assets do not authorize an overwrite or imply a probe failure.
	if os.IsNotExist(err) {
		return a, nil
	}
	// Top-level links could redirect a root archive or later rename.
	if err != nil {
		return a, err
	}
	// A real final object must also have protected, nonsymlinked ancestry.
	if err := protectedParent(filepath.Dir(p)); err != nil {
		return a, err
	}
	// Only real files and directories belong to the quarantine contract.
	if !info.IsDir() && !info.Mode().IsRegular() {
		return a, fmt.Errorf("Unsupported cluster asset type: %s", p)
	}
	h := sha256.New()
	err = filepath.WalkDir(p, func(name string, entry os.DirEntry, err error) error { // Hash every retained object, including sparse file contents.
		// An unreadable subtree must never appear empty in a review.
		if err != nil {
			return err
		}
		return hashEntry(h, p, name, entry, &a)
	})
	a.Digest = hex.EncodeToString(h.Sum(nil))
	return a, err
}

// hashEntry binds archive input paths, ownership, permissions, links and complete regular bytes.
func hashEntry(h io.Writer, root, name string, entry os.DirEntry, a *Asset) error {
	info, err := entry.Info()
	// A changing or unreadable object invalidates the review.
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(root, name)
	stat, ok := info.Sys().(*syscall.Stat_t)
	// Portable test fixtures still require native ownership information.
	if !ok {
		return fmt.Errorf("Cannot inspect cluster asset ownership")
	}
	fmt.Fprintf(h, "%q %v %d %d %d\n", rel, info.Mode(), info.Size(), stat.Uid, stat.Gid)
	// Links are retained as links without reading external targets.
	if info.Mode()&os.ModeSymlink != 0 {
		target, e := os.Readlink(name)
		fmt.Fprintf(h, "%q\n", target)
		return e
	}
	// Directories contribute metadata and are traversed by WalkDir.
	if info.IsDir() {
		return nil
	}
	// Stale Unix socket names carry no recoverable bytes; originals remain in quarantine,
	// while GNU tar deliberately omits sockets from the independent cold archive.
	if info.Mode()&os.ModeSocket != 0 {
		return nil
	}
	// Devices and FIFOs are outside the persistent cluster backup contract.
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Cluster asset contains special files: %s", name)
	}
	f, err := os.Open(name)
	// Open failure cannot be replaced with a partial fingerprint.
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(h, f)
	a.Bytes += uint64(n)
	return err
}

// capacity reserves independent backup space plus one GiB of operating headroom.
func (localHost) capacity(dir string, bytes uint64) error {
	var fs syscall.Statfs_t
	// Unknown free space cannot authorize a cold backup.
	if err := syscall.Statfs(filepath.Dir(dir), &fs); err != nil {
		return err
	}
	// Prevent both overflow and consuming the host's last free blocks.
	if bytes > 1<<60 || uint64(fs.Bavail)*uint64(fs.Bsize) < bytes+(1<<30) {
		return fmt.Errorf("Insufficient free space for verified cluster backups plus 1 GiB headroom")
	}
	return nil
}

// inspectCluster refuses service, process or mount ownership that preparation cannot safely replace.
func inspectCluster(ctx context.Context, h host, p *Plan) {
	// Unknown service state remains a blocker; preparation never stops an existing cluster.
	if err := clusterIdle(ctx, h); err != nil {
		p.Errors = append(p.Errors, err.Error())
	}
	var total uint64
	// Inventory whole standard paths rather than selected database files.
	for _, path := range clusterPaths {
		a, err := h.asset(path)
		// Incomplete asset reads cannot authorize quarantine.
		if err != nil {
			p.Errors = append(p.Errors, err.Error())
			continue
		}
		// Missing files are omitted so new arrivals invalidate the next review.
		if a.Digest != "" {
			p.Assets = append(p.Assets, a)
			total += a.Bytes
		}
	}
	// Backup storage must accommodate complete independent source copies.
	if err := h.capacity(p.BackupDir, total); err != nil {
		p.Errors = append(p.Errors, err.Error())
	}
	p.Changes = append(p.Changes, "Require administrator review of external cluster workloads and backups; refuse active services, Kubernetes processes and mounts", "Create private cold archives, verify full checksums and compare archives with their sources before changing any cluster asset", "Rename reviewed cluster assets into same-filesystem quarantine; retain all original bytes and independent backups; never invoke k3s-uninstall.sh or delete data")
}

// clusterIdle requires positive service-state evidence and fixed standard asset ownership.
func clusterIdle(ctx context.Context, h host) error {
	// Agent and other Kubernetes services are outside this standalone workflow.
	for _, unit := range []string{"k3s.service", "k3s-agent.service", "kubelet.service", "rke2-server.service", "rke2-agent.service"} {
		raw, err := h.command(ctx, "systemctl", "show", unit, "--property=ActiveState,LoadState,FragmentPath,UnitFileState", "--no-pager")
		// A failed query is not evidence of an inactive cluster.
		if err != nil {
			return fmt.Errorf("Cannot verify external Kubernetes service state")
		}
		v := properties(raw)
		// Validate one complete service observation before moving to process inspection.
		if err := inactiveClusterUnit(unit, v); err != nil {
			return err
		}
	}
	// Custom data locations cannot be represented as complete standard-path cold backups.
	if err := standardClusterStorage(h); err != nil {
		return err
	}
	return clusterProcesses(ctx, h)
}

// inactiveClusterUnit accepts only inactive, disabled standard-path service definitions.
func inactiveClusterUnit(unit string, v map[string]string) error {
	// Transitional, failed or masked states need explicit administrator resolution.
	if v["ActiveState"] != "inactive" || (v["LoadState"] != "loaded" && v["LoadState"] != "not-found") {
		return fmt.Errorf("Review and stop external Kubernetes services before quarantine: %s", unit)
	}
	// Vendor units or aliases would remain loadable after standard-path quarantine.
	if v["FragmentPath"] != "" && (unit != "k3s.service" || v["FragmentPath"] != "/etc/systemd/system/k3s.service") {
		return fmt.Errorf("External Kubernetes unit requires separate review: %s", unit)
	}
	// Boot-enabled external services require a deliberate administrator disable before quarantine.
	if v["UnitFileState"] == "enabled" || v["UnitFileState"] == "enabled-runtime" {
		return fmt.Errorf("Disable external Kubernetes service before quarantine: %s", unit)
	}
	return nil
}

// standardClusterStorage refuses external storage and configuration indirection requiring manual export.
func standardClusterStorage(h host) error {
	// Inspect only small configuration files; never print their credential-bearing contents.
	for _, path := range []string{"/etc/rancher/k3s/config.yaml", "/etc/systemd/system/k3s.service", "/etc/systemd/system/k3s.service.env"} {
		raw, err := h.read(path)
		// Missing conventional configuration is permitted for orphaned standard assets.
		if os.IsNotExist(err) {
			continue
		}
		// Incomplete configuration cannot authorize an assumption about data placement.
		if err != nil {
			return fmt.Errorf("Cannot inspect external cluster storage configuration")
		}
		// Only Hostlier's exact, token-delimited kubelet path is within the retained asset set.
		raw = regexp.MustCompile(`--kubelet-arg=root-dir=/var/lib/kubelet(?:[ \t\r\n]|$)`).ReplaceAll(raw, nil)
		// These flags can place persistent bytes or credentials outside the retained standard trees.
		for _, key := range []string{"data-dir", "datastore-endpoint", "container-runtime-endpoint", "root-dir", "K3S_DATA_DIR", "K3S_CONFIG_FILE", "--config=", "--config ", "token-file"} {
			// Fail conservatively even for a commented setting; a human can review and simplify it.
			if strings.Contains(string(raw), key) {
				return fmt.Errorf("External cluster uses custom storage or configuration; export and review it separately before quarantine")
			}
		}
	}
	// Configuration snippets and drop-ins can override the inspected main settings.
	for _, path := range []string{"/etc/rancher/k3s/config.yaml.d", "/etc/systemd/system/k3s.service.d"} {
		a, err := h.asset(path)
		// Override trees require explicit review rather than an incomplete flag parser.
		if err != nil || a.Digest != "" {
			return fmt.Errorf("External cluster configuration overrides require separate review: %s", path)
		}
	}
	return nil
}

// clusterProcesses catches orphaned shims and mounts after a service has stopped.
func clusterProcesses(ctx context.Context, h host) error {
	raw, err := h.command(ctx, "ps", "-eo", "comm=,args=")
	// A missing process inventory cannot authorize moving runtime storage.
	if err != nil {
		return fmt.Errorf("Cannot inspect Kubernetes processes")
	}
	// Match process identities and standard cluster runtime paths, not arbitrary substrings.
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		// Blank process records do not represent a process.
		if len(f) == 0 {
			continue
		}
		// Kubernetes children can survive systemctl stop because k3s uses KillMode=process.
		if f[0] == "k3s" || f[0] == "kubelet" || strings.Contains(line, "/run/k3s/") || strings.Contains(line, "/var/lib/rancher/k3s/") {
			return fmt.Errorf("External Kubernetes processes remain; drain and stop them before quarantine")
		}
	}
	return clusterMounts(h)
}

// clusterMounts requires every quarantined tree and its descendants to be unmounted.
func clusterMounts(h host) error {
	raw, err := h.read("/proc/self/mountinfo")
	// A failed mount read cannot be treated as an unmounted source.
	if err != nil {
		return fmt.Errorf("Cannot inspect Kubernetes mounts")
	}
	// No mounted subtree may be archived as an independent cold directory.
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		// Ignore the empty trailing line only.
		if len(f) < 5 {
			continue
		}
		// Check all quarantined trees, including the directory mount itself.
		for _, p := range clusterPaths {
			// Every mounted subtree remains a blocker.
			if within(f[4], p) {
				return fmt.Errorf("External Kubernetes mounts remain below %s", p)
			}
		} // Every mounted subtree remains a blocker.
	}
	return nil
}

// properties parses systemd's fixed key-value output without accepting arbitrary commands.
func properties(raw []byte) map[string]string {
	v := map[string]string{}
	// Preserve empty values while ignoring malformed properties.
	for _, line := range strings.Split(string(raw), "\n") {
		// Only complete property records are meaningful.
		if k, value, ok := strings.Cut(line, "="); ok {
			v[k] = value
		}
	} // Only complete property records are meaningful.
	return v
}
