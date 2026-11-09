package installer

import (
	"context"
	"fmt"
	"hostlier/internal/model"
	edge "hostlier/internal/nginx"
	"os"
	"os/exec"
	"strings"
)

// nginxGatewayAssets explicitly combines the owned Caddy frontend and private Nginx service.
func nginxGatewayAssets(o Options, a Assets) (Assets, error) {
	unit, bootstrap, err := edge.GatewayServiceAssets("")
	// Invalid backend asset generation cannot become an installation plan.
	if err != nil {
		return a, err
	}
	a.NginxService, a.Nginx = unit, string(bootstrap)
	a.Service = strings.ReplaceAll(a.Service, "caddy.service", "caddy.service hostlier-nginx-gateway.service")
	a.Service = strings.Replace(a.Service, " --runtime ", " --proxy nginx-gateway --runtime ", 1)
	return a, nil
}

// inspectNginxGateway permits only explicit opt-in on an already owned, compatible Caddy frontend.
func inspectNginxGateway(ctx context.Context, p *Plan) {
	installed := p.Snapshot.Installation
	// A gateway selection cannot silently take ownership of another application's TLS edge.
	if installed == nil || !installed.OwnedCaddy || !p.Snapshot.Caddy.Installed {
		p.Errors = append(p.Errors, "nginx-gateway requires an existing Hostlier-managed Caddy installation; initialize Caddy mode first")
	}
	version, ok := command(ctx, model.CaddyBinaryPath, "version")
	if !ok {
		version, ok = command(ctx, "caddy", "version")
	}
	// Older distro defaults do not implement the required H2/H3 bridge.
	if !ok || !gatewayCaddyVersion(version) {
		p.Errors = append(p.Errors, "nginx-gateway requires a reviewed upgrade to Caddy 2.11.7 or newer and GODEBUG=http2xconnect=1")
	}
	p.Snapshot.Nginx = component(ctx, "nginx")
	if p.Snapshot.Nginx.Installed && exec.CommandContext(ctx, "dpkg", "--compare-versions", p.Snapshot.Nginx.Version, "ge", nginxSecurityVersion(*p)).Run() != nil {
		p.Errors = append(p.Errors, "Existing Nginx requires Ubuntu security release "+nginxSecurityVersion(*p)+" or newer before gateway use")
	}
	// New private service assets require recorded ownership before they may be replaced.
	if installed == nil || installed.Proxy != "nginx-gateway" {
		// Existing public or operator Nginx is neither reconfigured nor stopped.
		for _, path := range []string{"/etc/hostlier/nginx-gateway", "/etc/systemd/system/hostlier-nginx-gateway.service"} {
			// Preexisting unowned files are evidence of a separate installation.
			if _, err := os.Lstat(path); err == nil {
				p.Errors = append(p.Errors, "Unowned Nginx gateway asset requires review: "+path)
			}
		}
	}
	inspectCaddyOwnership(p)
}

// gatewayCaddyVersion accepts only the tested bridge release or a later compatible version.
func gatewayCaddyVersion(value string) bool {
	var major, minor, patch int
	_, err := fmt.Sscanf(strings.TrimSpace(value), "v%d.%d.%d", &major, &minor, &patch)
	return err == nil && (major > 2 || major == 2 && (minor > 11 || minor == 11 && patch >= 7))
}

// nginxGatewayConfiguration installs private assets while preserving Caddy's certificate and route state.
func (a *installationRunner) nginxGatewayConfiguration() error {
	directory := "/etc/hostlier/nginx-gateway"
	// Only this explicitly owned directory receives the backend's configuration and pid file.
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	// Retries retain every application route previously published by the controller.
	if _, err := os.Stat(directory + "/nginx.conf"); os.IsNotExist(err) {
		// Bootstrap is private and fail-closed until an approved deployment publishes real routes.
		if err := atomic(directory+"/nginx.conf", []byte(a.assets.Nginx), 0600); err != nil {
			return err
		}
	}
	// The separate backend unit never changes the operator's nginx.service.
	if err := atomic("/etc/systemd/system/hostlier-nginx-gateway.service", []byte(a.assets.NginxService), 0644); err != nil {
		return err
	}
	return a.caddyConfiguration()
}

// gatewayEdgeConfiguration installs the two-layer edge and the same shared controller assets as other modes.
func (a *installationRunner) gatewayEdgeConfiguration() error {
	// No controller service is switched before its private backend assets exist.
	if err := a.nginxGatewayConfiguration(); err != nil {
		return err
	}
	// Keep shared controller and rotation assets consistent with the reviewed gateway service dependencies.
	for path, data := range map[string]string{"/etc/systemd/system/hostlier.service": a.assets.Service, "/etc/logrotate.d/hostlier": a.assets.Logrotate} {
		// An incomplete write leaves installation progress retryable.
		if err := atomic(path, []byte(data), 0644); err != nil {
			return err
		}
	}
	return nil
}

// nginxGatewayServices starts the private backend before the public frontend and controller.
func (a *installationRunner) nginxGatewayServices() error {
	// Public/operator Nginx services remain outside the gateway's lifecycle ownership.
	if err := a.run("systemctl", "enable", "--now", "hostlier-nginx-gateway.service", "caddy.service", "hostlier.service"); err != nil {
		return err
	}
	// Caddy must advertise extended CONNECT before deploying policies that require it.
	if err := a.run("systemctl", "restart", "caddy.service"); err != nil {
		return err
	}
	return a.run("systemctl", "restart", "hostlier.service")
}

// nginxGatewayReadiness verifies both private backend sockets and the certificate-valid public frontend.
func (a *installationRunner) nginxGatewayReadiness() error {
	// Reachability checks never send application requests or manufacture an approval.
	for _, address := range []string{"127.0.0.1:14443", "127.0.0.1:14444"} {
		// Both HTTP/1 upgrades and native gRPC must have a live private backend.
		if err := waitNginxListener(a.ctx, address); err != nil {
			return err
		}
	}
	return ensureEdge(a.ctx, a.plan, a.assets.Caddy)
}

// describeNginxGateway makes the extra gateway and trust boundary explicit in the installation review.
func describeNginxGateway(p *Plan) {
	describeCaddyInstallation(p)
	// Replace the ordinary distro-pin description with the explicitly required compatible gateway version.
	for i, change := range p.Changes {
		// Gateway installation reuses its already reviewed managed Caddy release.
		if strings.Contains(change, "Caddy "+caddyPackageVersion(*p)) {
			p.Changes[i] = "Reuse the existing Hostlier-managed Caddy 2.11.7+ frontend; enable HTTP/2 extended CONNECT"
		}
	}
	p.Changes = append(p.Changes, "Install or reuse Nginx; create hostlier-nginx-gateway.service on loopback TCP 14443/14444 only", "Keep public TLS, certificate renewal and QUIC in Caddy; proxy all owned application/controller routes through Nginx", "Overwrite original-method/protocol metadata at the gateway; enforce the same approved permissions again inside Nginx", "Leave external nginx.service and its configuration unchanged")
}

// gatewayProxyIdentity requires recorded ownership rather than adopting an externally configured Caddy.
func gatewayProxyIdentity(p *Plan) bool {
	return p.Proxy == "nginx-gateway" && p.Snapshot.Installation != nil && p.Snapshot.Installation.OwnedCaddy
}

// ownedCaddyInstallation requires retained ownership and a supported package or explicitly validated gateway.
func ownedCaddyInstallation(p *Plan) bool {
	return p.Snapshot.Installation != nil && p.Snapshot.Installation.OwnedCaddy && p.Snapshot.Caddy.Installed && (p.Snapshot.Caddy.Version == caddyPackageVersion(*p) || gatewayProxyIdentity(p))
}

// nginxGatewayPackages adds only a missing Nginx binary, leaving public service ownership unchanged.
func nginxGatewayPackages(p Plan) []string {
	// Existing Nginx is reused without stopping or rewriting its external service.
	if p.Proxy == "nginx-gateway" && !p.Snapshot.Nginx.Installed {
		return []string{"nginx=" + nginxSecurityVersion(p)}
	}
	return []string{}
}

// supportedGatewayProxyError provides an actionable installation error for unsupported selections.
func supportedGatewayProxyError() error {
	return model.Err("PROXY", "Supported proxies are caddy, nginx, nginx-existing and nginx-gateway")
}
