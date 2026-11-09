package installer

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	edge "hostlier/internal/nginx"
)

// nginxOptions binds the standalone edge to stable owned installation paths.
func nginxOptions() edge.Options {
	return edge.Options{Directory: "/etc/hostlier/nginx", ChallengeDirectory: "/var/lib/hostlier-acme"}
}

const NginxSecurityVersion = "1.24.0-2ubuntu7.19"
const NginxSecurityVersion2604 = "1.28.3-2ubuntu1.12"

// nginxSecurityVersion selects the reviewed security floor for the host release.
func nginxSecurityVersion(p Plan) string {
	if p.Snapshot.UbuntuVersion == "26.04" {
		return NginxSecurityVersion2604
	}
	return NginxSecurityVersion
}

// nginxAssets renders the owned service and automatic renewal units without adopting distro configuration.
func nginxAssets(o Options, a Assets) (Assets, error) {
	bootstrap, err := edge.Bootstrap(nginxOptions())
	// Stop activation when the required local operation fails.
	if err != nil {
		return a, err
	}
	service, timer, err := edge.RenewalAssets(nginxOptions())
	a.Nginx = string(bootstrap)
	a.NginxRenewalService = service
	a.NginxRenewalTimer = timer
	a.NginxService = "[Unit]\nDescription=Hostlier owned Nginx HTTPS edge\nAfter=network-online.target\nWants=network-online.target\n[Service]\nType=forking\nPIDFile=/etc/hostlier/nginx/nginx.pid\nExecStartPre=/usr/sbin/nginx -t -c /etc/hostlier/nginx/nginx.conf\nExecStart=/usr/sbin/nginx -c /etc/hostlier/nginx/nginx.conf\nExecReload=/usr/sbin/nginx -t -c /etc/hostlier/nginx/nginx.conf\nExecReload=/usr/sbin/nginx -s reload -c /etc/hostlier/nginx/nginx.conf\nExecStop=/usr/sbin/nginx -s quit -c /etc/hostlier/nginx/nginx.conf\nRestart=on-failure\n[Install]\nWantedBy=multi-user.target\n"
	a.Service = strings.ReplaceAll(a.Service, "caddy.service", "hostlier-nginx.service")
	a.Service = strings.Replace(a.Service, " --runtime ", " --proxy nginx --runtime ", 1)
	return a, err
}

// nginxPackages installs only the approved optional edge and ACME client packages.
func (a *installationRunner) nginxPackages() error {
	packages := []string{"nginx"}
	// Validate the selected edge ownership and configuration before proceeding.
	if !a.plan.Snapshot.Nginx.Installed {
		packages[0] = "nginx=" + nginxSecurityVersion(a.plan)
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if !a.plan.Snapshot.Certbot.Installed {
		packages = append(packages, "certbot")
	}
	// Stop activation when the required local operation fails.
	if err := a.run("apt-get", "update"); err != nil {
		return err
	}
	if err := a.run("apt-get", append([]string{"install", "-y", "--no-remove", "--no-install-recommends", "logrotate", "ca-certificates"}, packages...)...); err != nil {
		return err
	}
	version, ok := command(a.ctx, "dpkg-query", "-W", "-f=${Version}", "nginx")
	if !ok || exec.CommandContext(a.ctx, "dpkg", "--compare-versions", version, "ge", nginxSecurityVersion(a.plan)).Run() != nil {
		return fmt.Errorf("Nginx security release %s or newer is required", nginxSecurityVersion(a.plan))
	}
	return nil
}

// nginxConfiguration writes bootstrap assets while preserving an existing owned route configuration.
func (a *installationRunner) nginxConfiguration() error {
	// Stop activation when the required local operation fails.
	if err := os.MkdirAll("/var/lib/hostlier-acme/.well-known/acme-challenge", 0755); err != nil {
		return err
	}
	path := "/etc/hostlier/nginx/nginx.conf"
	// Validate the selected edge ownership and configuration before proceeding.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Stop activation when the required local operation fails.
		if err = atomic(path, []byte(a.assets.Nginx), 0600); err != nil {
			return err
		}
	}
	files := map[string]string{"/etc/systemd/system/hostlier-nginx.service": a.assets.NginxService, "/etc/systemd/system/hostlier-nginx-renew.service": a.assets.NginxRenewalService, "/etc/systemd/system/hostlier-nginx-renew.timer": a.assets.NginxRenewalTimer}
	// Apply the same validation to every owned edge record.
	for path, data := range files {
		// Stop activation when the required local operation fails.
		if err := atomic(path, []byte(data), 0644); err != nil {
			return err
		}
	}
	a.progress.OwnedNginx = true
	return nil
}

// nginxServices stops only the newly approved distro-default service and starts the owned standalone edge.
func (a *installationRunner) nginxServices() error {
	// Installation inspection forbids pre-existing unowned Nginx services.
	if err := a.run("systemctl", "disable", "--now", "nginx.service"); err != nil {
		return err
	}
	// Stop activation when the required local operation fails.
	if err := a.run("systemctl", "enable", "--now", "hostlier-nginx.service", "hostlier-nginx-renew.timer", "hostlier.service"); err != nil {
		return err
	}
	return a.run("systemctl", "restart", "hostlier.service")
}

// nginxReadiness issues initial TLS and verifies that the owned HTTPS listener accepts connections.
func (a *installationRunner) nginxReadiness() error {
	path := "/etc/hostlier/nginx/nginx.conf"
	data, err := os.ReadFile(path)
	// A retry must preserve every previously activated application route.
	if err != nil {
		return err
	}
	// Only the initial HTTP-only bootstrap may be replaced with controller-only TLS.
	if !strings.Contains(string(data), "ssl_certificate ") {
		manager := edge.Manager{Options: nginxOptions()}
		// Stop activation when the required local operation fails.
		if err = manager.Apply(a.ctx, a.plan.ControllerHostname, "127.0.0.1:7463", nil); err != nil {
			return err
		}
	} else {
		// Validate the selected edge ownership and configuration before proceeding.
		if !strings.Contains(string(data), "server_name "+a.plan.ControllerHostname+";") {
			return fmt.Errorf("owned Nginx controller route is absent; inspect configuration drift")
		}
		// Stop activation when the required local operation fails.
		if err = edge.Check(a.ctx, nginxOptions()); err != nil {
			return err
		}
	}

	return waitNginxListener(a.ctx, "127.0.0.1:443")
}

// inspectNginx inspects optional edge dependencies and refuses silent ownership changes.
func inspectNginx(ctx context.Context, p *Plan) {
	p.Snapshot.Nginx = component(ctx, "nginx")
	p.Snapshot.Certbot = component(ctx, "certbot")
	installed := p.Snapshot.Installation
	owned := installed != nil && installed.Proxy == "nginx" && installed.OwnedNginx
	// Validate the selected edge ownership and configuration before proceeding.
	if !owned && p.Snapshot.Nginx.Installed {
		p.Errors = append(p.Errors, "Existing Nginx installation requires explicit review; Hostlier will not replace its configuration.")
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if p.Snapshot.Caddy.Installed {
		p.Errors = append(p.Errors, "Nginx selection cannot replace an installed Caddy edge; migrate it explicitly before initialization.")
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if installed != nil && installed.Proxy != "" && installed.Proxy != "nginx" {
		p.Errors = append(p.Errors, "Proxy migration is unsupported.")
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if !owned {
		inspectNginxAssets(p)
	}
}

// inspectNginxAssets rejects unmanaged standalone configuration and binary installations.
func inspectNginxAssets(p *Plan) {
	// Apply the same validation to every owned edge record.
	for _, path := range []string{"/etc/hostlier/nginx", "/etc/systemd/system/hostlier-nginx.service"} {
		// Validate the selected edge ownership and configuration before proceeding.
		if _, err := os.Lstat(path); err == nil {
			p.Errors = append(p.Errors, "Existing unowned Nginx asset requires review: "+path)
		}
	}
	// Apply the same validation to every owned edge record.
	for _, name := range []string{"nginx", "certbot"} {
		// Validate the selected edge ownership and configuration before proceeding.
		if path, err := exec.LookPath(name); err == nil {
			// Validate the selected edge ownership and configuration before proceeding.
			if !strings.HasPrefix(path, "/usr/") {
				p.Errors = append(p.Errors, fmt.Sprintf("External %s binary requires review", name))
			}
		}
	}
}

// edgeConfiguration writes shared assets and only the selected proxy's owned files.
func (a *installationRunner) edgeConfiguration() error {
	// Gateway opt-in owns a distinct private Nginx service and the existing managed Caddy frontend.
	if a.plan.Proxy == "nginx-gateway" {
		return a.gatewayEdgeConfiguration()
	}
	// Shared Nginx gets only Hostlier service assets; its main configuration and unit stay untouched.
	if a.plan.Proxy == "nginx-existing" {
		return a.sharedNginxConfiguration()
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if a.plan.Proxy == "nginx" {
		// Stop activation when the required local operation fails.
		if err := a.nginxConfiguration(); err != nil {
			return err
		}
	} else {
		// Stop activation when the required local operation fails.
		if err := a.caddyConfiguration(); err != nil {
			return err
		}
	}
	// Apply the same validation to every owned edge record.
	for path, data := range map[string]string{"/etc/systemd/system/hostlier.service": a.assets.Service, "/etc/logrotate.d/hostlier": a.assets.Logrotate} {
		// Stop activation when the required local operation fails.
		if err := atomic(path, []byte(data), 0644); err != nil {
			return err
		}
	}
	return nil
}

// caddyConfiguration preserves the default edge's existing ownership behavior.
func (a *installationRunner) caddyConfiguration() error {
	// Validate the selected edge ownership and configuration before proceeding.
	if _, err := os.Stat("/etc/hostlier/caddy.json"); !a.progress.OwnedCaddy || os.IsNotExist(err) {
		// Stop activation when the required local operation fails.
		if err = atomic("/etc/hostlier/caddy.json", []byte(a.assets.Caddy), 0644); err != nil {
			return err
		}
	}
	return atomic("/etc/systemd/system/caddy.service.d/hostlier.conf", []byte(a.assets.CaddyService), 0644)
}

// selectedPackage selects compatibility pins only for packages the plan intends to use.
func selectedPackage(o Options, name string) bool {
	return name == "Docker" && o.Runtime == "docker" && !externalDocker(o) || name == "Caddy" && o.Proxy == "caddy"
}

// inspectNginxEdge permits occupied ports only for the recorded owned standalone edge.
func inspectNginxEdge(ctx context.Context, p *Plan) {
	inspectNginx(ctx, p)
	installed := p.Snapshot.Installation
	owned := installed != nil && installed.Proxy == "nginx" && installed.OwnedNginx
	// Validate the selected edge ownership and configuration before proceeding.
	if (p.Snapshot.Ports[0] || p.Snapshot.Ports[1]) && !owned {
		p.Errors = append(p.Errors, "TCP 80/443 is occupied; existing listeners require explicit review.")
	}
}

// describeNginx shares the runtime preview while substituting the approved edge actions.
func describeNginx(p *Plan) {
	copy := *p
	copy.Proxy = "caddy"
	describeInstallation(&copy)
	p.Changes = copy.Changes
	// Apply the same validation to every owned edge record.
	for i, text := range p.Changes {
		p.Changes[i] = strings.ReplaceAll(strings.ReplaceAll(text, "Caddy "+caddyPackageVersion(*p), "Nginx and Certbot"), "caddy.service", "hostlier-nginx.service")
	}
	p.Changes = append(p.Changes, "Issue HTTPS certificates using Certbot HTTP-01 on TCP 80; enable twice-daily renewal with configuration verification before reload")
	p.Changes = append(p.Changes, "Install or upgrade Hostlier-owned Nginx to Ubuntu security release "+nginxSecurityVersion(*p)+" or newer")
}

// inspectProxyMigration preserves the recorded edge identity across repeated initialization.
func inspectProxyMigration(p *Plan, installed Installation) {
	previous := installed.Proxy
	// Legacy installation records always refer to the original Caddy backend.
	if previous == "" {
		previous = "caddy"
	}
	// Explicit gateway opt-in retains the existing public Caddy owner and adds only a private backend.
	if previous == "caddy" && installed.OwnedCaddy && p.Proxy == "nginx-gateway" {
		return
	}
	// Moving public edge ownership requires an explicitly reviewed migration outside init.
	if previous != p.Proxy {
		p.Errors = append(p.Errors, "Proxy migration is unsupported; preserve the existing edge until an explicit migration is reviewed.")
	}
}

// waitNginxListener allows asynchronous master reload to open the first TLS listener.
func waitNginxListener(ctx context.Context, address string) error {
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	// Reload acknowledgement precedes worker readiness, so connection refusal is transient.
	for {
		connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(wait, "tcp", address)
		// Stop the check when its prerequisite operation fails.
		if err == nil {
			return connection.Close()
		}
		// Cancellation and timeout bound installation failure when the listener never appears.
		select {
		// Handle this readiness outcome without extending the deadline.
		case <-wait.Done():
			return fmt.Errorf("Nginx HTTPS listener is not ready: %w", wait.Err())
		// Handle this readiness outcome without extending the deadline.
		case <-ticker.C:
		}
	}
}
