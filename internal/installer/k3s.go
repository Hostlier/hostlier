package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"hostlier/internal/model"
)

const K3sVersion = "v1.37.2+k3s1"

type k3sRelease struct{ version, amd64, arm64 string }

var k3sReleases = []k3sRelease{
	{"v1.34.13+k3s1", "b9a7c47aab81db66d0253378ddea0d06f8a6dca4dace7a26db3f00fa2f0e3717", "bad32c0c798bce0e883216ed755bbfb3c6a9fbd9b9f1dee32a6be16d256ad033"},
	{"v1.35.10+k3s1", "5cd49f0faca5b2ea4355d4761e7b1445b8aa05cd50e612fd9c370551db49e5e2", "0c780ef4b6275bd27c77ed64a647fa1d859b1bd60597ad420f3243c261e5ffb6"},
	{"v1.36.6+k3s1", "66ceb8e67edfa547195135d74783bfadc2ecc04a91059e90ae5f70a3dbc98c15", "49dcecdb4f64f5e92bdc52d204d407fb39729e492f09074488125ee58b6f2895"},
	{K3sVersion, "265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda", "a779ccf1e5e2e33a62f5a29c2f1c0bbf99431e100579d4c10f0aa0742a6aa84b"},
}

// k3sUpgradePath also admits versions left on disk by an interrupted upgrade.
func k3sUpgradePath(version string) ([]k3sRelease, bool) {
	if version == "v1.34.11+k3s1" || version == "v1.34.12+k3s1" {
		return k3sReleases, true
	}
	for index, release := range k3sReleases {
		if release.version == version {
			return k3sReleases[index+1:], true
		}
	}
	return nil, false
}

func describeK3sVersion(installed Component) string {
	if !installed.Installed {
		return "Install SHA256-verified K3s " + K3sVersion + " with bundled containerd"
	}
	steps, known := k3sUpgradePath(installed.Version)
	if !known {
		return "Require a supported version of Hostlier-managed K3s before proceeding"
	}
	if len(steps) == 0 {
		return "Keep K3s " + installed.Version + " and verify its readiness"
	}
	versions := []string{installed.Version}
	for _, step := range steps {
		versions = append(versions, step.version)
	}
	return "Upgrade Hostlier-managed K3s through " + strings.Join(versions, " -> ") + ". Each step verifies the binary, restarts K3s, and waits for the server and node"
}

// k3sAssets renders the reviewed single-node units and narrow management firewall guard.
func k3sAssets() map[string]string {
	return map[string]string{
		"/etc/systemd/system/k3s.service": `[Unit]
Description=Hostlier pinned single-node k3s
After=network-online.target hostlier-k3s-firewall.service
Wants=network-online.target
Requires=hostlier-k3s-firewall.service

[Service]
Type=notify
ExecStart=/usr/local/bin/k3s server --disable=traefik --disable=servicelb --flannel-backend=host-gw --write-kubeconfig-mode=0600 --secrets-encryption --kube-proxy-arg=nodeport-addresses=127.0.0.0/8
KillMode=process
Delegate=yes
Restart=always
RestartSec=5
LimitNOFILE=1048576
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
TimeoutStartSec=300

[Install]
WantedBy=multi-user.target
`,
		"/etc/systemd/system/hostlier-k3s-firewall.service": `[Unit]
Description=Hostlier private k3s management listeners
Before=k3s.service
After=network-pre.target ufw.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/etc/hostlier/k3s-firewall.sh

[Install]
WantedBy=multi-user.target
`,
		"/etc/hostlier/k3s-firewall.sh": `#!/bin/sh
set -eu
# Never flush chains or change policies. These rules own only private k3s ports.
for tool in /usr/sbin/iptables /usr/sbin/ip6tables; do
 "$tool" -C INPUT -p tcp -m multiport --dports 6443,10250 -m comment --comment hostlier-k3s-private -j REJECT 2>/dev/null || "$tool" -I INPUT 1 -p tcp -m multiport --dports 6443,10250 -m comment --comment hostlier-k3s-private -j REJECT
 "$tool" -C INPUT -p tcp -m multiport --dports 6443,10250 -m addrtype --src-type LOCAL -m comment --comment hostlier-k3s-local -j ACCEPT 2>/dev/null || "$tool" -I INPUT 1 -p tcp -m multiport --dports 6443,10250 -m addrtype --src-type LOCAL -m comment --comment hostlier-k3s-local -j ACCEPT
done
/usr/sbin/iptables -C INPUT -s 10.42.0.0/16 -p tcp -m multiport --dports 6443,10250 -m comment --comment hostlier-k3s-pods -j ACCEPT 2>/dev/null || /usr/sbin/iptables -I INPUT 1 -s 10.42.0.0/16 -p tcp -m multiport --dports 6443,10250 -m comment --comment hostlier-k3s-pods -j ACCEPT
`,
	}
}

// installK3s installs approved cluster assets and verifies local readiness.
func installK3s(ctx context.Context, p Plan, run func(string, ...string) error) error {
	lock, err := os.OpenFile("/run/hostlier-k3s-install.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return model.Err("K3S_BUSY", "Another Hostlier K3s installation or upgrade is in progress")
	}
	installed := p.Snapshot.K3s
	// Recheck after taking the lock so another installer cannot advance the binary under a stale plan.
	if observed := installedK3sBinary(ctx); observed != installed {
		return model.Err("STALE_PLAN", "Installed K3s changed. Review a new installation plan")
	}
	if !installed.Installed {
		if err := downloadK3s(ctx, k3sReleases[len(k3sReleases)-1]); err != nil {
			return err
		}
		installed.Version = K3sVersion
	}
	// Node security flags are retained when regenerating cluster assets.
	if err := writeK3sAssets(p); err != nil {
		return err
	}

	// Reload new unit definitions before enabling them.
	if e := run("systemctl", "daemon-reload"); e != nil {
		return e
	}
	// Start the management guard together with the pinned cluster.
	if e := run("systemctl", "enable", "--now", "hostlier-k3s-firewall.service", "k3s.service"); e != nil {
		return e
	}
	// Apply the current guard configuration on an existing installation too.
	if e := run("systemctl", "restart", "hostlier-k3s-firewall.service"); e != nil {
		return e
	}
	return upgradeK3s(installed.Version, k3sServerVersion(ctx),
		func(release k3sRelease) error { return downloadK3s(ctx, release) },
		func() error { return run("systemctl", "restart", "k3s.service") },
		func(version string) error { return waitK3sReady(ctx, version) })
}

func installedK3sBinary(ctx context.Context) Component {
	out, ok := command(ctx, "/usr/local/bin/k3s", "--version")
	fields := strings.Fields(out)
	if ok && len(fields) > 2 {
		return Component{Installed: true, Version: fields[2]}
	}
	return Component{Installed: ok}
}

// upgradeK3s starts each permitted release in order and stops at the first failed readiness check.
func upgradeK3s(installed, running string, install func(k3sRelease) error, restart func() error, ready func(string) error) error {
	steps, known := k3sUpgradePath(installed)
	if !known {
		return model.Err("K3S_VERSION", "Unsupported installed K3s version: "+installed)
	}
	if running != "" && running != installed {
		// An interrupted restart can leave one verified step on disk ahead of the running server.
		next, _ := k3sUpgradePath(running)
		if len(next) == 0 || next[0].version != installed {
			return model.Err("K3S_VERSION", "Running K3s "+running+" does not match installed K3s "+installed)
		}
		if err := restart(); err != nil {
			return err
		}
	}
	if err := ready(installed); err != nil {
		return err
	}
	for _, release := range steps {
		if err := install(release); err != nil {
			return err
		}
		if err := restart(); err != nil {
			return err
		}
		if err := ready(release.version); err != nil {
			return err
		}
	}
	return nil
}

// writeK3sAssets publishes the reviewed cluster unit and private firewall guard.
func writeK3sAssets(p Plan) error {
	// Every asset must be durable before any service is started.
	for path, body := range k3sAssets() {
		data, err := expectedMigrationAsset(p.StateDir, path, body)
		// Invalid node policy cannot produce an unenforced service definition.
		if err != nil {
			return err
		}
		mode := os.FileMode(0644)
		// Helpers remain executable only by the privileged owner.
		if filepath.Ext(path) == ".sh" {
			mode = 0700
		}
		// Never activate a partial unit or script.
		if err := atomic(path, []byte(data), mode); err != nil {
			return err
		}
	}
	return nil
}

// downloadK3s fetches only the pinned binary and verifies its checksum before activation.
func downloadK3s(ctx context.Context, release k3sRelease) error {
	asset, checksum := "k3s", release.amd64
	// Select the release artifact matching the supported host architecture.
	if runtime.GOARCH == "arm64" {
		asset, checksum = "k3s-arm64", release.arm64
	}
	target := "https://github.com/k3s-io/k3s/releases/download/" + strings.ReplaceAll(release.version, "+", "%2B") + "/" + asset
	request, e := http.NewRequestWithContext(ctx, "GET", target, nil)
	// Do not start a download with an invalid release request.
	if e != nil {
		return e
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	response, e := client.Do(request)
	// Normalize transport failure without exposing response data.
	if e != nil {
		return model.Err("K3S_DOWNLOAD", "Cannot download pinned k3s binary")
	}
	defer response.Body.Close()
	// Require a successful release response before staging executable bytes.
	if response.StatusCode != 200 {
		return model.Err("K3S_DOWNLOAD", "Pinned k3s binary is unavailable")
	}
	return saveK3sDownload(response.Body, checksum)
}

func k3sKubectl(ctx context.Context, args ...string) (string, bool) {
	return command(ctx, "/usr/local/bin/k3s", append([]string{"kubectl", "--kubeconfig=/etc/rancher/k3s/k3s.yaml", "--context=default", "--request-timeout=5s"}, args...)...)
}

func k3sServerVersion(ctx context.Context) string {
	var response struct{ GitVersion string }
	if out, ok := k3sKubectl(ctx, "get", "--raw=/version"); ok {
		_ = json.Unmarshal([]byte(out), &response)
	}
	return response.GitVersion
}

func k3sNodeReady(ctx context.Context, version string) bool {
	out, ok := k3sKubectl(ctx, "get", "nodes", "-o", "json")
	var nodes struct {
		Items []struct {
			Status struct {
				NodeInfo   struct{ KubeletVersion string }
				Conditions []struct{ Type, Status string }
			}
		}
	}
	if !ok || json.Unmarshal([]byte(out), &nodes) != nil || len(nodes.Items) != 1 {
		return false
	}
	status := nodes.Items[0].Status
	if status.NodeInfo.KubeletVersion == version {
		for _, condition := range status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				return true
			}
		}
	}
	return false
}

// waitK3sReady bounds readiness polling and respects cancellation.
func waitK3sReady(ctx context.Context, version string) error {
	probeContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for probeContext.Err() == nil {
		if out, ok := k3sKubectl(probeContext, "get", "--raw=/readyz"); ok && out == "ok" &&
			k3sServerVersion(probeContext) == version && k3sNodeReady(probeContext, version) {
			return nil
		}
		// Wait between readiness attempts while respecting cancellation.
		select {
		// Return cancellation instead of continuing cluster probes.
		case <-probeContext.Done():
		// Retry once per second without busy polling.
		case <-time.After(time.Second):
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return model.Err("K3S_NOT_READY", fmt.Sprintf("K3s %s did not become ready on the server and node. Inspect journalctl -u k3s", version))
}

// saveK3sDownload bounds download size and activates only the verified artifact.
func saveK3sDownload(body io.Reader, checksum string) error {
	dir, err := os.Open("/usr/local/bin")
	if err != nil {
		return err
	}
	defer dir.Close()
	f, e := os.CreateTemp(dir.Name(), ".k3s-hostlier-*")
	// Do not stream download data into an unavailable staging file.
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hasher := sha256.New()
	written, e := io.Copy(io.MultiWriter(f, hasher), io.LimitReader(body, 256*1024*1024+1))
	// Remove incomplete or oversized artifacts before reporting download failure.
	if e != nil || written > 256*1024*1024 {
		return model.Err("K3S_DOWNLOAD", "Pinned k3s download exceeded limit")
	}
	// Compare the complete artifact with the architecture-specific reviewed checksum.
	if hex.EncodeToString(hasher.Sum(nil)) != checksum {
		return model.Err("K3S_CHECKSUM", "Pinned k3s checksum verification failed")
	}
	// Make only a checksum-verified artifact executable.
	if e = f.Chmod(0755); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	// Activate the verified binary atomically at the expected path.
	if e = os.Rename(f.Name(), "/usr/local/bin/k3s"); e != nil {
		return e
	}

	return dir.Sync()
}
