package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	edge "hostlier/internal/nginx"
)

// sharedNginxAssets retains the external service definitions and isolates certificate renewal.
func sharedNginxAssets(o Options, a Assets) (Assets, error) {
	a.Service = strings.ReplaceAll(a.Service, "caddy.service", "nginx.service")
	a.Service = strings.Replace(a.Service, " --runtime ", " --proxy nginx-existing --runtime ", 1)
	a.Caddy, a.CaddyService = "", ""
	service, timer, err := edge.SharedRenewalAssets(edge.SharedOptions{})
	a.NginxRenewalService, a.NginxRenewalTimer = service, timer
	if err != nil {
		return a, err
	}
	a.NginxCertificateService, err = edge.SharedCertificateServiceAssets(edge.SharedOptions{})
	return a, err
}

// inspectSharedInfrastructure requires existing services and fingerprints their stable identities.
func inspectSharedInfrastructure(ctx context.Context, p *Plan) {
	p.Snapshot.Firewall = "external"
	for _, service := range []string{"nginx.service"} { // Only the edge service is required here; runtime selection is independent.
		if value, ok := command(ctx, "systemctl", "is-active", service); !ok || value != "active" { // Require compatible existing services without installing or replacing them.
			p.Errors = append(p.Errors, "nginx-existing requires an already running "+service)
		}
	}
	// This first integration supports the distribution master with its standard main configuration.
	if !standardNginxService(ctx) {
		p.Errors = append(p.Errors, "nginx-existing requires system nginx.service running /usr/sbin/nginx with /etc/nginx/nginx.conf and no custom prefix")
	}
	if _, ok := command(ctx, "/usr/bin/certbot", "--version"); !ok { // Require compatible existing services without installing or replacing them.
		p.Errors = append(p.Errors, "Install Certbot before using nginx-existing; Hostlier will not change existing packages")
	}
	digest, err := edge.InspectShared(ctx, edge.SharedOptions{}, []string{p.ControllerHostname})
	if err != nil { // Require compatible existing services without installing or replacing them.
		p.Errors = append(p.Errors, err.Error())
	}
	p.Snapshot.ExistingNginxDigest = digest
	p.Warnings = append(p.Warnings, "Existing firewall, DNS, Nginx configuration and external certificate renewal remain operator-managed. Only new, unclaimed hostnames can be deployed.")
}

// standardNginxService verifies the running master when a unit expands its config path.
func standardNginxService(ctx context.Context) bool {
	unit, ok := command(ctx, "systemctl", "show", "nginx.service", "--property=ExecStart", "--value")
	if !ok || !strings.Contains(unit, "/usr/sbin/nginx") || strings.Contains(unit, " -p") {
		return false
	}
	if !strings.Contains(unit, " -c") {
		return true
	}
	pidText, ok := command(ctx, "systemctl", "show", "nginx.service", "--property=MainPID", "--value")
	pid, err := strconv.Atoi(pidText)
	if !ok || err != nil || pid <= 1 {
		return false
	}
	process := filepath.Join("/proc", pidText)
	executable, err := os.Readlink(filepath.Join(process, "exe"))
	if err != nil || executable != "/usr/sbin/nginx" {
		return false
	}
	commandLine, err := os.ReadFile(filepath.Join(process, "cmdline"))
	return err == nil && standardNginxMasterCommand(commandLine)
}

// standardNginxMasterCommand accepts only the resolved standard config and no prefix override.
func standardNginxMasterCommand(commandLine []byte) bool {
	fields := strings.Fields(strings.ReplaceAll(string(commandLine), "\x00", " "))
	standardConfig, standardBinary := false, false
	for i, field := range fields {
		if field == "/usr/sbin/nginx" {
			standardBinary = true
		}
		if field == "-p" || strings.HasPrefix(field, "-p") && len(field) > 2 {
			return false
		}
		if field == "-c" {
			if standardConfig || i+1 >= len(fields) || fields[i+1] != "/etc/nginx/nginx.conf" {
				return false
			}
			standardConfig = true
		}
	}
	return standardBinary && standardConfig
}

// inspectExistingDocker probes the same rootful local socket the controller will use, ignoring CLI contexts.
func inspectExistingDocker(ctx context.Context) (string, string, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { // Inspect the controller socket directly instead of an ambient Docker context.
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://docker/info", nil)
	response, err := client.Do(req)
	if err != nil { // Inspect the controller socket directly instead of an ambient Docker context.
		return "", "", fmt.Errorf("cannot connect to existing Docker at /var/run/docker.sock")
	}
	defer response.Body.Close()
	var info struct {
		ID, ServerVersion, OSType string
		SecurityOptions           []string
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&info) != nil { // Inspect the controller socket directly instead of an ambient Docker context.
		return "", "", fmt.Errorf("cannot inspect existing Docker daemon")
	}
	return info.ServerVersion, info.ID, validateExistingDocker(info.ServerVersion, info.ID, info.OSType, info.SecurityOptions)
}

// validateExistingDocker limits the initial compatibility contract to rootful Linux engines with safe loopback publication.
func validateExistingDocker(version, id, osType string, security []string) error {
	major, err := strconv.Atoi(strings.Split(version, ".")[0])
	if err != nil || major < 28 || major > 29 || osType != "linux" || id == "" { // Reject daemon capabilities outside the initial coexistence contract.
		return fmt.Errorf("Docker reuse requires a rootful Linux Docker Engine 28.x or 29.x")
	}
	for _, option := range security { // Reject daemon capabilities outside the initial coexistence contract.
		if strings.Contains(option, "rootless") { // Reject daemon capabilities outside the initial coexistence contract.
			return fmt.Errorf("rootless Docker is not supported for reuse")
		}
	}
	return nil
}

// describeSharedInstallation exposes only changes belonging to Hostlier in the approval preview.
func describeSharedInstallation(p *Plan) {
	// A new cluster keeps its own management guard while preserving unrelated firewall rules.
	if p.Runtime == "k3s" {
		p.Changes = append(p.Changes, "Install SHA256-verified "+K3sVersion+" and its owned k3s service and management firewall guard; disable Traefik/ServiceLB; retain the existing Nginx edge")
	}
	p.Changes = append(p.Changes,
		describeRuntimeChoice(p),
		"Create or refresh Hostlier-owned /etc/nginx/conf.d/zz-hostlier.conf for Hostlier hostnames only; validate the full Nginx configuration and gracefully reload the existing master",
		"Keep existing sites and certificate renewal unchanged; store new certificates in /etc/hostlier/nginx-existing-certificates with a separate renewal timer",
		"Issue new Hostlier certificates through a dedicated per-host systemd job so Snap Certbot can run while hostlier.service keeps its protections",
		"Read optional existing certificate references from /etc/hostlier/nginx-existing-tls.json without copying or renewing their keys",
		"Install "+p.Executable+", protected controller state and hostlier.service; expose controller at https://"+p.ControllerHostname,
		"Leave existing runtime/proxy package versions and service definitions and host firewall rules unchanged; install only missing selected runtime dependencies")
}

// sharedNginxConfiguration writes only Hostlier's service and renewal assets.
func (a *installationRunner) sharedNginxConfiguration() error {
	files := map[string]string{
		"/etc/systemd/system/hostlier.service":                      a.assets.Service,
		"/etc/logrotate.d/hostlier":                                 a.assets.Logrotate,
		"/etc/systemd/system/hostlier-nginx-existing-renew.service": a.assets.NginxRenewalService,
		"/etc/systemd/system/hostlier-nginx-existing-renew.timer":   a.assets.NginxRenewalTimer,
		"/etc/systemd/system/hostlier-nginx-existing-cert@.service": a.assets.NginxCertificateService,
	}
	for path, content := range files { // Write only the dedicated Hostlier service assets.
		if err := atomic(path, []byte(content), 0644); err != nil { // Write only the dedicated Hostlier service assets.
			return err
		}
	}
	return nil
}

// sharedNginxServices starts only Hostlier units and never enables, stops or restarts external services.
func (a *installationRunner) sharedNginxServices() error {
	if err := a.runtimeService(); err != nil { // Start only a Hostlier-managed runtime; inspect an external daemon without changing it.
		return err
	}
	if err := a.run("systemctl", "is-active", "nginx.service"); err != nil { // Leave external service lifecycle control with the operator.
		return err
	}
	if err := a.run("systemctl", "enable", "--now", "hostlier.service", "hostlier-nginx-existing-renew.timer"); err != nil { // Leave external service lifecycle control with the operator.
		return err
	}
	return a.run("systemctl", "restart", "hostlier.service")
}

// sharedNginxReadiness refreshes the owned controller route while preserving deployed application routes.
func (a *installationRunner) sharedNginxReadiness() error {
	o := edge.SharedDefaults(edge.SharedOptions{})
	if _, err := os.Stat(o.Fragment); err == nil {
		if err := edge.UpgradeSharedController(a.ctx, o, a.plan.ControllerHostname, "127.0.0.1:7463"); err != nil {
			return err
		}
		return edge.CheckSharedController(a.ctx, o, a.plan.ControllerHostname)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := edge.ApplyShared(a.ctx, o, a.plan.ControllerHostname, "127.0.0.1:7463", nil); err != nil {
		return err
	}
	return edge.CheckSharedController(a.ctx, o, a.plan.ControllerHostname)
}
