package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"hostlier/internal/model"
)

const K3sVersion = "v1.34.12+k3s1"
const previousK3sVersion = "v1.34.11+k3s1"

var k3sChecksums = map[string]string{"amd64": "ef1df26b26276c0f6d962391c94e12f9362d1f1e30ec7a7926e9ddb84b0d66ce", "arm64": "67e3e242ba23480634caecc18939feff3a558756b340e4ef964552d748b0271e"}

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
	// Replace only a missing binary or the previously owned pinned patch release.
	upgrade := p.Snapshot.K3s.Installed && p.Snapshot.K3s.Version != K3sVersion
	if !p.Snapshot.K3s.Installed || upgrade {
		// Do not install services until the pinned binary is verified.
		if err := downloadK3s(ctx); err != nil {
			return err
		}
	}
	// Commit every approved cluster asset before starting systemd services.
	for path, data := range k3sAssets() {
		mode := os.FileMode(0644)
		// Restrict executable helper scripts to the privileged owner.
		if filepath.Ext(path) == ".sh" {
			mode = 0700
		}
		// An uncommitted unit or guard script must stop installation.
		if e := atomic(path, []byte(data), mode); e != nil {
			return e
		}
	}
	// Reload new unit definitions before enabling them.
	if e := run("systemctl", "daemon-reload"); e != nil {
		return e
	}
	// Start the management guard together with the pinned cluster.
	if e := run("systemctl", "enable", "--now", "hostlier-k3s-firewall.service", "k3s.service"); e != nil {
		return e
	}
	// A running service keeps executing its old binary until an approved patch upgrade restarts it.
	if upgrade {
		if e := run("systemctl", "restart", "k3s.service"); e != nil {
			return e
		}
	}
	// Apply the current guard configuration on an existing installation too.
	if e := run("systemctl", "restart", "hostlier-k3s-firewall.service"); e != nil {
		return e
	}
	return waitK3sReady(ctx)
}

// downloadK3s fetches only the pinned binary and verifies its checksum before activation.
func downloadK3s(ctx context.Context) error {
	asset := "k3s"
	// Select the release artifact matching the supported host architecture.
	if runtime.GOARCH == "arm64" {
		asset = "k3s-arm64"
	}
	target := "https://github.com/k3s-io/k3s/releases/download/" + strings.ReplaceAll(K3sVersion, "+", "%2B") + "/" + asset
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
	return saveK3sDownload(response.Body)
}

// waitK3sReady bounds readiness polling and respects cancellation.
func waitK3sReady(ctx context.Context) error {
	deadline := time.Now().Add(3 * time.Minute)
	// Bound cluster readiness polling to the approved startup window.
	for time.Now().Before(deadline) {
		// Require the pinned local kubectl ready endpoint to report success.
		if out, ok := command(ctx, "/usr/local/bin/k3s", "kubectl", "--kubeconfig=/etc/rancher/k3s/k3s.yaml", "--context=default", "get", "--raw=/readyz"); ok && out == "ok" {
			return nil
		}
		// Wait between readiness attempts while respecting cancellation.
		select {
		// Return cancellation instead of continuing cluster probes.
		case <-ctx.Done():
			return ctx.Err()
		// Retry once per second without busy polling.
		case <-time.After(time.Second):
		}
	}
	return model.Err("K3S_NOT_READY", fmt.Sprintf("Pinned %s did not become locally ready; inspect journalctl -u k3s", K3sVersion))
}

// saveK3sDownload bounds download size and activates only the verified artifact.
func saveK3sDownload(body io.Reader) error {
	temp := "/usr/local/bin/k3s.hostlier-download"
	f, e := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	// Do not stream download data into an unavailable staging file.
	if e != nil {
		return e
	}
	hasher := sha256.New()
	written, e := io.Copy(io.MultiWriter(f, hasher), io.LimitReader(body, 256*1024*1024+1))
	// Sync only a successfully copied binary.
	if e == nil {
		e = f.Sync()
	}
	f.Close()
	// Remove incomplete or oversized artifacts before reporting download failure.
	if e != nil || written > 256*1024*1024 {
		os.Remove(temp)
		return model.Err("K3S_DOWNLOAD", "Pinned k3s download exceeded limit")
	}
	// Compare the complete artifact with the architecture-specific reviewed checksum.
	if hex.EncodeToString(hasher.Sum(nil)) != k3sChecksums[runtime.GOARCH] {
		os.Remove(temp)
		return model.Err("K3S_CHECKSUM", "Pinned k3s checksum verification failed")
	}
	// Make only a checksum-verified artifact executable.
	if e = os.Chmod(temp, 0755); e != nil {
		return e
	}
	// Activate the verified binary atomically at the expected path.
	if e = os.Rename(temp, "/usr/local/bin/k3s"); e != nil {
		return e
	}

	return nil
}
