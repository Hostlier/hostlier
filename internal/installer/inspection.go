package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"hostlier/internal/model"
)

// inspectPackages records installed Ubuntu Docker and Caddy package versions.
func inspectPackages(ctx context.Context, p *Plan) {
	p.Snapshot.Docker = component(ctx, "docker.io")
	p.Snapshot.Caddy = component(ctx, "caddy")
}

// inspectExternalPackages flags untracked binaries that package installation must not replace.
func inspectExternalPackages(ctx context.Context, p *Plan) {
	o := p.Options

	// Check external Docker binaries only when the approved runtime requires Docker.
	if o.Runtime == "docker" && !p.Snapshot.Docker.Installed && !externalDocker(o) {
		// An untracked binary must not be overwritten by Ubuntu package installation.
		if _, found := exec.LookPath("docker"); found == nil {
			p.Errors = append(p.Errors, "An externally installed Docker engine requires a separately reviewed integration; Ubuntu package installation will not replace it.")
		}
	}
	// Detect externally installed Caddy before proposing package ownership.
	if p.Proxy == "caddy" && !p.Snapshot.Caddy.Installed {
		// Require explicit review before adopting an untracked edge binary.
		if _, found := exec.LookPath("caddy"); found == nil {
			p.Errors = append(p.Errors, "An externally installed Caddy binary requires a separately reviewed integration.")
		}
	}
}

// inspectSystemd checks that host service management is available.
func inspectSystemd(ctx context.Context, p *Plan) {
	systemd, ok := command(ctx, "systemctl", "is-system-running")
	// Accept only systemd states in which installation services can be managed.
	if !ok && systemd != "degraded" && systemd != "starting" {
		p.Errors = append(p.Errors, "systemd must be running.")
	}
}

// inspectPackageVersions flags installed versions outside the supported runtime pins.
func inspectPackageVersions(ctx context.Context, p *Plan) {
	o := p.Options

	// Compare installed packages with the supported pinned versions.
	for name, c := range map[string]Component{"Docker": p.Snapshot.Docker, "Caddy": p.Snapshot.Caddy} {
		expected := dockerPackageVersion(*p)
		// Use the Caddy version pin for the edge package.
		if name == "Caddy" {
			expected = caddyPackageVersion(*p)
		}
		// Do not require Docker compatibility for a k3s-only installation.
		if c.Installed && c.Version != expected && selectedPackage(o, name) {
			p.Errors = append(p.Errors, fmt.Sprintf("Existing %s %s is unsupported; expected %s.", name, c.Version, expected))
		}
	}
}

// inspectInstallationRecord loads prior ownership and rejects backend migration.
func inspectInstallationRecord(ctx context.Context, p *Plan) {
	o := p.Options

	// Load prior ownership only from an existing installation record.
	if data, e := os.ReadFile(filepath.Join(o.StateDir, "installation.json")); e == nil {
		var installation Installation
		// Unreadable ownership state cannot authorize reuse.
		if json.Unmarshal(data, &installation) != nil {
			p.Errors = append(p.Errors, "Existing installation state is unreadable.")
		} else {
			p.Snapshot.Installation = &installation
			inspectProxyMigration(p, installation)
			if installation.MasterFingerprint != "" && installation.MasterFingerprint != o.MasterFingerprint {
				p.Errors = append(p.Errors, "This controller is already pinned to a different master.")
			}
			// Reject automatic migration between runtime backends.
			if installation.Runtime != "" && installation.Runtime != o.Runtime {
				p.Errors = append(p.Errors, "Runtime migration is unsupported.")
			}
		}
	}
}

// inspectUnownedAssets flags existing installation files without recorded ownership.
func inspectUnownedAssets(ctx context.Context, p *Plan) {
	// Shared-edge assets cannot be claimed solely because their names start with Hostlier.
	if p.Proxy == "nginx-existing" && p.Snapshot.Installation == nil {
		for _, path := range []string{"/etc/hostlier/nginx-existing", "/etc/hostlier/nginx-existing-certificates", "/etc/nginx/conf.d/zz-hostlier.conf", "/etc/systemd/system/hostlier-nginx-existing-renew.service", "/etc/systemd/system/hostlier-nginx-existing-renew.timer", "/etc/systemd/system/hostlier-nginx-existing-cert@.service"} { // Refuse pre-existing installation assets without recorded ownership.
			if _, err := os.Lstat(path); err == nil { // Refuse pre-existing installation assets without recorded ownership.
				p.Errors = append(p.Errors, "Existing unowned coexistence asset requires review: "+path)
			}
		}
	}
	// Without recorded ownership, existing installation assets require review.
	if p.Snapshot.Installation == nil {
		// Inspect every asset that would otherwise be replaced.
		for _, path := range []string{"/etc/hostlier/caddy.json", "/etc/systemd/system/hostlier.service", "/etc/systemd/system/caddy.service.d/hostlier.conf", model.CaddyBinaryPath} {
			// Refuse silent adoption of an existing service or edge configuration.
			if _, statErr := os.Stat(path); statErr == nil {
				p.Errors = append(p.Errors, "Existing unowned installation asset requires review: "+path)
			}
		}
	}
}

// inspectExternalK3s flags external cluster configuration requiring explicit review.
func inspectExternalK3s(ctx context.Context, p *Plan) {
	o := p.Options

	// Check cluster configuration only for k3s installation plans.
	if o.Runtime == "k3s" {
		// Inspect both the primary configuration and override directory.
		for _, path := range []string{"/etc/rancher/k3s/config.yaml", "/etc/rancher/k3s/config.yaml.d"} {
			// External cluster configuration may alter security assumptions and requires review.
			if _, e := os.Stat(path); e == nil {
				p.Errors = append(p.Errors, "Existing external k3s configuration requires explicit review: "+path)
			}
		}
	}
}

// inspectK3sVersion records the local cluster version and checks adoption eligibility.
func inspectK3sVersion(ctx context.Context, p *Plan) {
	o := p.Options

	// Inspect a cluster binary only when k3s is selected.
	if o.Runtime == "k3s" {
		v, ok := command(ctx, "/usr/local/bin/k3s", "--version")
		fields := strings.Fields(v)
		version := ""
		// Read the version field only from a complete version response.
		if len(fields) > 2 {
			version = fields[2]
		}
		p.Snapshot.K3s = Component{Installed: ok, Version: version}
		// Permit a patch upgrade only from the previous Hostlier-owned release.
		if ok && (version != K3sVersion && version != previousK3sVersion || p.Snapshot.Installation == nil || p.Snapshot.Installation.Runtime != "k3s") {
			p.Errors = append(p.Errors, "Existing k3s installation cannot be safely adopted or upgraded")
		}
	}
}

// inspectEdgeOwnership checks occupied public ports and existing Caddy configuration.
func inspectEdgeOwnership(ctx context.Context, p *Plan) {
	p.Snapshot.Ports = []bool{occupied("80"), occupied("443")}
	// Opt-in gateway mode requires ownership of the existing public frontend.
	if p.Proxy == "nginx-gateway" {
		inspectNginxGateway(ctx, p)
		return
	}
	// Nginx retains separate ownership and never adopts existing Caddy.
	if p.Proxy == "nginx" {
		inspectNginxEdge(ctx, p)
		return
	}
	inspectCaddyOwnership(p)
}

// inspectFirewall records active UFW web rules or inspects independent ingress views.
func inspectFirewall(ctx context.Context, p *Plan) {
	ufw, ufwOK := command(ctx, "ufw", "status")
	p.Snapshot.Firewall = "none"
	// Propose scoped web allow rules only when UFW is active.
	if ufwOK && strings.Contains(ufw, "Status: active") {
		p.Snapshot.Firewall = "ufw"
		// Check both HTTP certificate issuance and HTTPS application ports.
		for _, port := range []int{80, 443} {
			// Add only missing unrestricted TCP web allow rules.
			if !regexp.MustCompile(fmt.Sprintf(`(?m)^%d(/tcp)?\s+ALLOW\s+Anywhere`, port)).MatchString(ufw) {
				p.Snapshot.Rules = append(p.Snapshot.Rules, port)
			}
		}
	} else {
		inspectPacketFiltering(ctx, p)
	}
}

// inspectPlatform rejects unsupported hosts before installation approval.
func inspectPlatform(p *Plan) {
	o := p.Options

	release := read("/etc/os-release")
	p.Snapshot.UbuntuVersion = ubuntuVersion(release)
	// Reject unreviewed distributions and architectures before package installation.
	if problem := platformProblem(release, runtime.GOOS, runtime.GOARCH, o); problem != "" {
		p.Errors = append(p.Errors, problem)
	}
	// Preview the missing privilege requirement instead of attempting mutations.
	if os.Geteuid() != 0 {
		p.Errors = append(p.Errors, "Run init as root on the target server.")
	}
	// Reject runtime names outside the two supported backends.
	if o.Runtime != "docker" && o.Runtime != "k3s" {
		p.Errors = append(p.Errors, "Unsupported installation runtime.")
	}
}

// ubuntuVersion reads the supported version identifier without trusting display names.
func ubuntuVersion(release string) string {
	for _, version := range []string{"24.04", "26.04"} {
		if regexp.MustCompile(`(?m)^VERSION_ID="?` + regexp.QuoteMeta(version) + `"?$`).MatchString(release) {
			return version
		}
	}
	return ""
}

// platformProblem accepts only reviewed Ubuntu LTS releases and CPU architectures.
func platformProblem(release, goos, goarch string, options Options) string {
	if goos != "linux" || goarch != "amd64" && goarch != "arm64" || !regexp.MustCompile(`(?m)^ID=ubuntu$`).MatchString(release) {
		return "Only Ubuntu 24.04 LTS or Ubuntu 26.04 LTS on amd64 or arm64 is supported."
	}
	if ubuntuVersion(release) == "26.04" && options.Runtime != "docker" {
		return "Ubuntu 26.04 LTS currently supports the Docker runtime; k3s installation still requires Ubuntu 24.04 LTS."
	}
	if ubuntuVersion(release) != "" {
		return ""
	}
	return "Only Ubuntu 24.04 LTS or Ubuntu 26.04 LTS on amd64 or arm64 is supported."
}

// describeInstallation renders the inspected changes for interactive approval.
func describeInstallation(p *Plan) {
	// The managed gateway must be visible as a two-layer routing change in the approval preview.
	if p.Proxy == "nginx-gateway" {
		describeNginxGateway(p)
		p.Changes = append(p.Changes, "Install or verify SHA256-pinned Caddy "+CaddyBinaryVersion+" as the Hostlier-owned public edge executable")
		return
	}
	// Existing infrastructure is attached without taking over packages, services or firewall policy.
	if p.Proxy == "nginx-existing" {
		describeSharedInstallation(p)
		return
	}
	// Nginx preview names certificate issuance and renewal as approved actions.
	if p.Proxy == "nginx" {
		describeNginx(p)
		return
	}
	describeCaddyInstallation(p)
	p.Changes = append(p.Changes, "Install or verify SHA256-pinned Caddy "+CaddyBinaryVersion+" as the Hostlier-owned public edge executable")
}

// inspectPacketFiltering cross-checks nft and both textual compatibility views.
func inspectPacketFiltering(ctx context.Context, p *Plan) {
	nft, nftOK := command(ctx, "nft", "-j", "list", "ruleset")
	iptables, iptablesOK := firewallView(ctx, "iptables")
	ip6tables, ip6tablesOK := firewallView(ctx, "ip6tables")
	ownedCluster := p.Snapshot.Installation != nil && p.Snapshot.Installation.Runtime == "k3s" && p.Snapshot.K3s.Version == K3sVersion
	ipv4Blocks := iptablesOK && iptablesInputFiltering(iptables, ownedCluster)
	ipv6Blocks := ip6tablesOK && iptablesInputFiltering(ip6tables, ownedCluster)
	// nft JSON omits xtables compatibility match details, including
	// comments and port lists. Trust those INPUT representations only
	// after both textual views independently match the owned rules.
	verifiedCluster := verifiedCompatibility(ownedCluster, iptablesOK, ip6tablesOK, ipv4Blocks, ipv6Blocks, nft, iptables, ip6tables)
	ownedDocker := ownedDockerInstallation(p)
	nftBlocks, nftErr := nftInputFiltering([]byte(nft), verifiedCluster, ownedDocker)
	// Any blocking or undecodable ingress view requires a separately reviewed firewall.
	if nftOK && (nftErr != nil || nftBlocks) || ipv4Blocks || ipv6Blocks {
		p.Errors = append(p.Errors, "Unsupported active nftables/iptables firewall; configure reviewed web rules before retrying.")
	}
}

// verifiedCompatibility trusts opaque nft rules only after all independent views agree.
func verifiedCompatibility(owned, ipv4OK, ipv6OK, ipv4Blocks, ipv6Blocks bool, nft, ipv4, ipv6 string) bool {
	return owned && ipv4OK && ipv6OK && !ipv4Blocks && !ipv6Blocks && nftInputViewsMatch([]byte(nft), ipv4, ipv6)
}

// inspectCaddyOwnership retains the default proxy's strict ownership and listener checks.
func inspectCaddyOwnership(p *Plan) {

	owned := ownedCaddyInstallation(p)
	// Occupied web ports are safe only for the recorded supported edge.
	if (p.Snapshot.Ports[0] || p.Snapshot.Ports[1]) && !owned {
		p.Errors = append(p.Errors, "TCP 80/443 is occupied; review the existing service before installing Hostlier.")
	}
	// Do not replace an unowned Caddy configuration containing user content.
	if p.Snapshot.Caddy.Installed && !owned && strings.TrimSpace(read("/etc/caddy/Caddyfile")) != "" {
		p.Errors = append(p.Errors, "Existing Caddy configuration cannot be safely adopted automatically.")
	}
}

// describeCaddyInstallation renders shared runtime actions and the default edge preview.
func describeCaddyInstallation(p *Plan) {

	o := p.Options
	p.Changes = append(p.Changes, describeRuntimeChoice(p))

	// Describe whether each required Ubuntu package will be installed or reused.
	for _, c := range []struct {
		name, version string
		installed     bool
	}{{"Caddy", caddyPackageVersion(*p), p.Snapshot.Caddy.Installed}} {
		verb := "Install"
		// Mark supported installed components as reuse in the approval text.
		if c.installed {
			verb = "Reuse"
		}
		// Exclude Docker package changes from k3s plans.
		if c.name != "Docker" || o.Runtime == "docker" {
			p.Changes = append(p.Changes, fmt.Sprintf("%s %s %s from Ubuntu archive", verb, c.name, c.version))
		}
	}
	// Expose cluster networking, firewall and storage limitations in the approval preview.
	if o.Runtime == "k3s" {
		clusterChange := "Install SHA256-verified " + K3sVersion + " with bundled containerd; no Docker installation"
		if p.Snapshot.K3s.Installed && p.Snapshot.K3s.Version == previousK3sVersion {
			clusterChange = "Upgrade owned k3s from " + previousK3sVersion + " to SHA256-verified " + K3sVersion + " and restart k3s.service"
		}
		p.Changes = append(p.Changes, clusterChange, "Disable Traefik/ServiceLB; single-node host-gw networking; restrict NodePorts to 127/8", "Persist owned IPv4/IPv6 TCP6443/10250 management firewall guards; permit host-local and 10.42/16 pod sources, preserve SSH and unrelated rules", "Start k3s.service and hostlier-k3s-firewall.service at boot; local-path PVC requested capacity has no enforced quota")
	}
	p.Changes = append(p.Changes, "Create protected "+o.StateDir+", /etc/hostlier and /var/log/hostlier", "Configure HTTPS controller "+o.ControllerHostname+" with private loopback administration", "Install "+o.Executable+" and enable hostlier.service and caddy.service; start only Hostlier-managed runtimes", "Write /var/log/hostlier/hostlier.log with bounded rotation")
	// Disclose the extra scoped pod rules required for an active UFW firewall.
	if o.Runtime == "k3s" && p.Snapshot.Firewall == "ufw" {
		p.Changes = append(p.Changes, "Allow UFW cni0 host/forward traffic scoped to pod CIDR 10.42.0.0/16; Kubernetes NetworkPolicy isolates workloads")
	}
	// Tell the approver when no web firewall rule changes are necessary.
	if len(p.Snapshot.Rules) == 0 {
		p.Changes = append(p.Changes, "No host firewall allow-rule changes needed")
	} else {
		// Describe each proposed web allow rule before approval.
		for _, port := range p.Snapshot.Rules {
			p.Changes = append(p.Changes, fmt.Sprintf("Allow inbound TCP %d from Anywhere using UFW IPv4/IPv6; preserve existing rules", port))
		}
	}
}

// ownedDockerInstallation requires recorded runtime ownership and the supported package pin.
func ownedDockerInstallation(p *Plan) bool {
	return p.Snapshot.Installation != nil && p.Snapshot.Installation.Runtime == "docker" && p.Snapshot.Docker.Version == dockerPackageVersion(*p)
}
