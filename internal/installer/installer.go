// Package installer inspects before approval and installs only reviewed owned infrastructure.
package installer

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/fleet"
)

const DockerVersion = "29.1.3-0ubuntu3~24.04.2"
const CaddyVersion = "2.6.2-6ubuntu0.24.04.3"
const DockerVersion2604 = "29.1.3-0ubuntu4.1"
const CaddyVersion2604 = "2.6.2-14"

// dockerPackageVersion selects the reviewed Ubuntu archive pin for this host.
func dockerPackageVersion(p Plan) string {
	if p.Snapshot.UbuntuVersion == "26.04" {
		return DockerVersion2604
	}
	return DockerVersion
}

// caddyPackageVersion selects the reviewed Ubuntu archive pin for this host.
func caddyPackageVersion(p Plan) string {
	if p.Snapshot.UbuntuVersion == "26.04" {
		return CaddyVersion2604
	}
	return CaddyVersion
}

// command bounds read-only host inspection and returns captured output with success status.
func command(ctx context.Context, file string, args ...string) (string, bool) {
	probe, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, e := exec.CommandContext(probe, file, args...).Output()
	return strings.TrimSpace(string(out)), e == nil
}

// read supplies optional host-file text; missing files are represented as empty content.
func read(path string) string { data, _ := os.ReadFile(path); return string(data) }

// component inspects Ubuntu package installation and version without changing packages.
func component(ctx context.Context, name string) Component {
	v, ok := command(ctx, "dpkg-query", "-W", "-f=${Version}", name)
	return Component{Installed: ok && v != "", Version: v}
}

// occupied checks whether a public TCP port can be exclusively bound.
func occupied(port string) bool {
	listener, e := net.Listen("tcp4", "0.0.0.0:"+port)
	// Treat bind failure as occupied or unavailable rather than proposing ownership.
	if e != nil {
		return true
	}
	listener.Close()
	return false
}

// Inspect builds an approval preview from supported paths, host conditions and ownership evidence.
func Inspect(ctx context.Context, o Options) (Plan, error) {
	o, detected, detectionErr := detectInstallation(ctx, o)
	// Resolve ownership before rendering assets or proposing host changes.
	if detectionErr != nil {
		return Plan{}, detectionErr
	}
	domain, e := config.NormalizeDomain(o.Domain)
	// Require a valid supported domain before deriving installation assets.
	if e != nil {
		return Plan{}, e
	}
	o.Domain = domain
	// Normalize the reserved controller hostname before any host inspection.
	if o.ControllerHostname, e = config.NormalizeDomain(o.ControllerHostname); e != nil {
		return Plan{}, e
	}
	// Validate generated-asset inputs before proposing filesystem changes.
	if _, e = InstallationAssets(o); e != nil {
		return Plan{}, e
	}
	p := Plan{Options: o, Changes: []string{}, Warnings: []string{"DNS and upstream firewall/NAT must permit public TCP 80 and 443; local setup cannot prove external reachability."}, Errors: []string{}, Snapshot: Snapshot{Rules: []int{}, Ports: []bool{}}}
	p.Snapshot.DetectedDocker, p.Snapshot.DetectedNginx = detected.Docker, detected.Nginx
	inspectPlatform(&p)
	// Run Linux-specific probes only on the supported OS family.
	if runtime.GOOS == "linux" {
		inspectHostInfrastructure(ctx, &p)
	}
	inspectCollector(&p)
	describeInstallation(&p)
	if o.MasterFingerprint != "" {
		p.Changes = append(p.Changes, "Serve fleet workload requests on TCP "+fleet.Port+" using mutual TLS pinned to master "+o.MasterFingerprint)
		p.Warnings = append(p.Warnings, "Allow the master to reach TCP "+fleet.Port+" through host and provider firewalls.")
	}
	p.Changes = append(p.Changes, "Install the bundled minimal telemetry image and automatically manage one collector per Docker host or Kubernetes node")
	// Make local credential sharing and read-only home access explicit in the installation preview.
	if p.DockerLogin != nil {
		p.Changes = append(p.Changes, "Read Docker login from "+p.DockerLogin.Directory+"; run credential helpers as "+p.DockerLogin.Username+" with read-only access to that user’s home and runtime directory; never rewrite Docker config")
	}
	p.Supported = len(p.Errors) == 0
	p.Fingerprint = fingerprint(p)
	return p, nil
}

// atomic commits a protected installation file and syncs its parent directory.
func atomic(path string, data []byte, mode os.FileMode) error {
	// Create the parent before opening the staging file.
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(path+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	// Return staging-open failures without replacing the target.
	if e != nil {
		return e
	}
	// Set the approved permissions before writing file contents.
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(data)
	}
	// Sync only a fully written staging file.
	if e == nil {
		e = f.Sync()
	}
	f.Close()
	// Do not rename a file whose write or sync failed.
	if e != nil {
		return e
	}
	// Replace the destination only after the staging data is durable.
	if e = os.Rename(path+".tmp", path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	// A failed parent-directory open prevents confirming rename durability.
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Apply applies only the unchanged installation plan explicitly approved by the caller.
func Apply(ctx context.Context, p Plan, o ApplyOptions) error {
	report := func(status string) {
		if o.OnProgress != nil {
			o.OnProgress("validation", status)
		}
	}
	report("started")
	// Reject missing approval, altered plans or changed host conditions before mutations.
	if err := validateApprovedInstallation(ctx, p, o); err != nil {
		report("failed")
		return err
	}
	assets, e := InstallationAssets(p.Options)
	// Do not apply a plan whose assets can no longer be generated.
	if e != nil {
		report("failed")
		return e
	}
	report("completed")
	a := &installationRunner{ctx: ctx, plan: p, options: o, assets: assets, progress: Installation{MasterFingerprint: p.MasterFingerprint, DockerLogin: p.DockerLogin, DockerMode: p.DockerMode, Proxy: p.Proxy, Runtime: p.Runtime, Hostname: p.ControllerHostname, ControllerHostname: p.ControllerHostname, Completed: []string{}, FirewallRules: []int{}}}
	// Persist ownership and retry metadata before package services can start.
	a.report("preparation", "started")
	if err := a.prepare(); err != nil {
		a.report("preparation", "failed")
		return err
	}
	a.report("preparation", "completed")
	// Execute stages in dependency order and persist each successful completion.
	for _, step := range []struct {
		name string
		run  func() error
	}{{"collector-image", a.collectorImage}, {"packages", a.packages}, {"configuration", a.configuration}, {"firewall", a.firewall}, {"k3s", a.k3s}, {"services", a.services}, {"edge-readiness", a.edgeReadiness}, {"controller-readiness", a.controllerReadiness}, {"collector-readiness", a.collectorReadiness}} {
		// Do not record a cluster stage for a Docker-only installation.
		if step.name == "k3s" && p.Runtime != "k3s" {
			continue
		}
		// Stop at the first failed stage while preserving prior completion metadata.
		if err := a.step(step.name, step.run); err != nil {
			return err
		}
	}
	a.progress.Ready = true
	a.report("finalization", "started")
	if err := a.persist(); err != nil {
		a.report("finalization", "failed")
		return err
	}
	a.report("finalization", "completed")
	return nil
}
