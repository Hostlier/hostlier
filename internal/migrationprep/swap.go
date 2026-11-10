package migrationprep

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// swapUnitName admits only generated systemd unit identifiers, never argument injection.
var swapUnitName = regexp.MustCompile(`^[A-Za-z0-9_.@\\-]+\.swap$`)

// inspectSwap records exact activation paths and persistent configuration without exposing contents.
func inspectSwap(ctx context.Context, h host, p *Plan) {
	raw, err := h.read("/proc/swaps")
	// Unknown swap state cannot be treated as disabled.
	if err != nil {
		p.Errors = append(p.Errors, "Cannot inspect active swap")
		return
	}
	swaps, used, err := parseSwaps(raw)
	// Ambiguous kernel paths cannot be passed to privileged tools.
	if err != nil {
		p.Errors = append(p.Errors, err.Error())
		return
	}
	// Associate each active path with the exact systemd unit that could re-enable it.
	for i := range swaps {
		unit, err := h.command(ctx, "systemd-escape", "--path", "--suffix=swap", swaps[i].Path)
		name := strings.TrimSpace(string(unit))
		// Only a complete, valid unit name may be masked or restored.
		if err != nil || !swapUnitName.MatchString(name) {
			p.Errors = append(p.Errors, "Cannot resolve active swap unit")
			return
		}
		swaps[i].Unit = name
	}
	p.Swaps = swaps
	fstab, err := h.read("/etc/fstab")
	// Preserve a complete persistent configuration before proposing any edit.
	if err != nil {
		p.Errors = append(p.Errors, "Cannot inspect /etc/fstab")
		return
	}
	p.FstabDigest = digest(fstab)
	// Capture all loaded units, including inactive generated and native swap units.
	if err := inspectSwapUnits(ctx, h, p); err != nil {
		p.Errors = append(p.Errors, err.Error())
	}
	// Require spare RAM before moving any existing swapped pages back into memory.
	if err := memoryHeadroom(h, used); err != nil {
		p.Errors = append(p.Errors, err.Error())
	}
	p.Changes = append(p.Changes, "Back up /etc/fstab and swap activation metadata, comment only swap entries, and mask reviewed swap units across reboot", "Check available RAM exceeds used swap plus max(1 GiB, 10% of RAM), then disable only the reviewed active swap paths; retain swap files and devices", "Rollback restores exact fstab bytes and only preparation-owned masks; re-enable only previously active swap with its recorded priority")
}

// parseSwaps validates kernel output and rejects escaped filenames that tools could interpret differently.
func parseSwaps(raw []byte) ([]Swap, uint64, error) {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	result := []Swap{}
	var used uint64
	// A missing header is an inspection error rather than proof of zero swap.
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Filename") {
		return nil, 0, fmt.Errorf("Invalid /proc/swaps response")
	}
	// Every active mapping must have an unambiguous absolute path and numeric priority.
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		// Reject malformed, escaped or whitespace-containing kernel paths.
		if len(f) != 5 || !cleanAbsolute(f[0]) || strings.Contains(f[0], "\\") {
			return nil, 0, fmt.Errorf("Swap paths require manual review")
		}
		kb, e := strconv.ParseUint(f[3], 10, 64)
		// Bound memory arithmetic and reject invalid priorities.
		if _, priorityErr := strconv.Atoi(f[4]); e != nil || priorityErr != nil || kb > 1<<50 {
			return nil, 0, fmt.Errorf("Invalid swap activation metadata")
		}
		used += kb * 1024
		result = append(result, Swap{Path: f[0], Priority: f[4]})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path }) // Stable path order binds the same set of activations.
	return result, used, nil
}

// inspectSwapUnits refuses generators that can bypass systemd swap-unit masks.
func inspectSwapUnits(ctx context.Context, h host, p *Plan) error {
	// Out-of-band swap generators require a separately reviewed policy.
	if err := swapManagersAbsent(ctx, h); err != nil {
		return err
	}
	raw, err := h.command(ctx, "systemctl", "list-units", "--all", "--type=swap", "--no-legend", "--plain", "--no-pager")
	// Unknown native or generated units cannot safely be left enabled.
	if err != nil {
		return fmt.Errorf("Cannot inspect persistent swap units")
	}
	units := map[string]bool{}
	// Preserve the kernel-observed unit even if it was activated outside systemd.
	for _, s := range p.Swaps {
		units[s.Unit] = true
	}
	// Parse each loaded unit before combining the persistent definitions.
	if err := loadedSwapUnits(raw, units); err != nil {
		return err
	}
	// Include disabled native definitions which have never been loaded into systemd.
	if err := nativeSwapUnits(ctx, h, units); err != nil {
		return err
	}
	// Every observed unit must have a safely maskable persistent definition.
	if err := reviewSwapUnits(ctx, h, p, units); err != nil {
		return err
	}
	sort.Strings(p.SwapUnits)
	return nil
}

// loadedSwapUnits requires complete, loaded systemd records before accepting their names.
func loadedSwapUnits(raw []byte, units map[string]bool) error {
	// Native swap units need a mask even when not listed in fstab.
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		// Empty trailing output does not describe a unit.
		if len(f) == 0 {
			continue
		}
		// Reject truncated or non-loaded units instead of guessing their prior state.
		if len(f) < 4 || !swapUnitName.MatchString(f[0]) || f[1] != "loaded" {
			return fmt.Errorf("Swap unit state requires manual review")
		}
		units[f[0]] = true
	}
	return nil
}

// nativeSwapUnits inventories persistent swap definitions independently of current activation.
func nativeSwapUnits(ctx context.Context, h host, units map[string]bool) error {
	raw, err := h.command(ctx, "systemctl", "list-unit-files", "--type=swap", "--no-legend", "--no-pager")
	// An unavailable definition inventory cannot authorize a reboot-safe policy.
	if err != nil {
		return fmt.Errorf("Cannot inspect native swap definitions")
	}
	// Every observed native unit must join the same explicit mask review.
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		// Empty output represents no native units.
		if len(f) == 0 {
			continue
		}
		// Unexpected output remains a blocker rather than a guessed unit name.
		if len(f) < 2 || !swapUnitName.MatchString(f[0]) {
			return fmt.Errorf("Cannot parse native swap definition")
		}
		units[f[0]] = true
	}
	return nil
}

// reviewSwapUnits records only overrides which this preparation can later own and remove.
func reviewSwapUnits(ctx context.Context, h host, p *Plan, units map[string]bool) error {
	// A pre-existing mask must never become owned by this preparation.
	for unit := range units {
		raw, err := h.command(ctx, "systemctl", "show", unit, "--property=UnitFileState,FragmentPath", "--no-pager")
		// Only an absent or unmasked unit can receive our reversible mask.
		if err != nil || strings.Contains(properties(raw)["UnitFileState"], "masked") {
			return fmt.Errorf("Existing swap mask requires manual review: %s", unit)
		}
		// A native /etc unit cannot be masked without replacing its original definition.
		if strings.HasPrefix(properties(raw)["FragmentPath"], "/etc/systemd/system/") {
			return fmt.Errorf("Native swap unit requires separate persistent-policy review: %s", unit)
		}
		p.SwapUnits = append(p.SwapUnits, unit)
	}
	return nil
}

// swapManagersAbsent refuses services that can bypass per-unit swap masks.
func swapManagersAbsent(ctx context.Context, h host) error {
	// These services can call swapon directly after an otherwise successful preparation.
	for _, unit := range []string{"dphys-swapfile.service", "systemd-swap.service", "zramswap.service", "zram-config.service"} {
		raw, err := h.command(ctx, "systemctl", "show", unit, "--property=LoadState", "--no-pager")
		// Installed swap managers require a separately reviewed persistent policy.
		if err != nil || properties(raw)["LoadState"] != "not-found" {
			return fmt.Errorf("External swap manager requires manual review: %s", unit)
		}
	}
	return nil
}

// memoryHeadroom checks current capacity again immediately before disabling swap.
func memoryHeadroom(h host, used uint64) error {
	raw, err := h.read("/proc/meminfo")
	// Unknown memory capacity cannot authorize potentially disruptive swapoff.
	if err != nil {
		return fmt.Errorf("Cannot inspect swapoff memory headroom")
	}
	v := map[string]uint64{}
	// Read only kernel-reported totals, never estimates from swap device size.
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		// Ignore unrelated or incomplete memory fields.
		if len(f) != 3 {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		// Require bounded numeric kB values for arithmetic.
		if err == nil && f[2] == "kB" && n < 1<<50 {
			v[f[0]] = n * 1024
		}
	}
	reserve := max(uint64(1<<30), v["MemTotal:"]/10)
	// Missing MemAvailable remains zero and therefore fails closed.
	if v["MemAvailable:"] < used+reserve {
		return fmt.Errorf("Insufficient RAM for safe swapoff: need used swap plus max(1 GiB, 10%% of RAM) available")
	}
	return nil
}

// disabledFstab preserves every non-swap line and every original byte in the journal backup.
func disabledFstab(raw []byte) []byte {
	lines := strings.Split(string(raw), "\n")
	// Only active fstab swap entries receive a preparation marker.
	for i, line := range lines {
		f := strings.Fields(line)
		// Comments and all other filesystem definitions remain untouched.
		if len(f) >= 3 && !strings.HasPrefix(f[0], "#") && f[2] == "swap" {
			lines[i] = "# hostlier-preparation: " + line
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
