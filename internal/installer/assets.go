package installer

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/controledge"
	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// Assets groups generated configuration files for one reviewed installation.
type Assets struct {
	Service, Logrotate, Config, Caddy, CaddyService                                      string
	Nginx, NginxService, NginxRenewalService, NginxRenewalTimer, NginxCertificateService string
}

// InstallationAssets renders controller, Caddy and rotation files from validated options.
func InstallationAssets(o Options) (Assets, error) {
	o = defaults(o)
	// Validate executable separation and systemd paths before rendering service assets.
	if err := validateInstallationPaths(o); err != nil {
		return Assets{}, err
	}
	// Require a hostname safe for the generated private reverse-proxy route.
	if _, e := config.NormalizeDomain(o.ControllerHostname); e != nil {
		return Assets{}, e
	}
	fleetArgs := ""
	// An optional fleet pin is supplied only to controllers explicitly enrolled to a master.
	if o.MasterFingerprint != "" {
		// The exact fingerprint is safe for literal systemd interpolation.
		if !fleet.ValidPin(o.MasterFingerprint) {
			return Assets{}, model.Err("INVALID_HOST", "Master fingerprint must be an exact SHA-256 certificate fingerprint")
		}
		fleetArgs = " --master-fingerprint " + o.MasterFingerprint
		if o.MasterURL != "" {
			if !fleet.ValidURL(o.MasterURL) {
				return Assets{}, model.Err("INVALID_HOST", "Master URL must be a fleet HTTPS origin")
			}
			fleetArgs += " --master-url " + o.MasterURL
		}
	} else if o.MasterURL != "" {
		return Assets{}, model.Err("INVALID_HOST", "Master URL requires a fingerprint")
	}
	a := Assets{Service: fmt.Sprintf(`[Unit]
Description=Hostlier controller
After=network-online.target docker.service caddy.service
Wants=network-online.target
Requires=docker.service
StartLimitIntervalSec=60
StartLimitBurst=10

[Service]
Type=simple
User=root
UMask=0077
ExecStart=%s run --state-dir %s --config %s --socket /run/hostlier/controller.sock --host 127.0.0.1 --port 7463%s --hostname %s --runtime %s --log-path /var/log/hostlier/hostlier.log
Restart=on-failure
RestartSec=5
RuntimeDirectory=hostlier
RuntimeDirectoryMode=0700
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true

[Install]
WantedBy=multi-user.target
`, o.Executable, o.StateDir, o.ConfigPath, fleetArgs, o.ControllerHostname, o.Runtime), Logrotate: `/var/log/hostlier/hostlier.log {
 daily
 rotate 7
 size 10M
 missingok
 notifempty
 compress
 delaycompress
 copytruncate
 create 0600 root root
}
`, Config: "version: 1\ndomains:\n  - " + o.Domain + "\n", CaddyService: fmt.Sprintf(`[Service]
Environment=GODEBUG=http2xconnect=1
ExecStart=
ExecStart=%s run --resume --environ --config /etc/hostlier/caddy.json
ExecReload=
ExecReload=%s reload --config /var/lib/caddy/.config/caddy/autosave.json
ReadWritePaths=/var/lib/caddy
`, model.CaddyBinaryPath, model.CaddyBinaryPath)}
	var loginError error
	a, loginError = dockerLoginAssets(o, a)
	// A login source must be safe before emitting any service unit.
	if loginError != nil {
		return Assets{}, loginError
	}
	ordinary, rpc := "127.0.0.1:7463", "127.0.0.1:7463"
	// The shared-gateway mode carries conventional HTTP and RPC on distinct private ports.
	if o.Proxy == "nginx-gateway" {
		ordinary, rpc = "127.0.0.1:14443", "127.0.0.1:14444"
	}
	caddy := map[string]any{"admin": map[string]any{"listen": "127.0.0.1:2019"}, "apps": map[string]any{"http": map[string]any{"servers": map[string]any{"hostlier": map[string]any{"listen": []string{":443"}, "routes": []any{controledge.CaddyRoute(o.ControllerHostname, ordinary, rpc)}}}}}}
	data, e := json.MarshalIndent(caddy, "", "  ")
	a.Caddy = string(data)
	// Bind controller startup to the approved cluster service for k3s plans.
	if o.Runtime == "k3s" {
		a.Service = strings.ReplaceAll(a.Service, "docker.service", "k3s.service")
	}
	// An external Docker service must not be started or stopped through systemd dependencies.
	if externalDocker(o) {
		a.Service = strings.ReplaceAll(a.Service, "Requires=docker.service\n", "")
	}
	return installationProxyAssets(o, a, e)
}

// installationProxyAssets chooses the independently reviewed edge configuration.
func installationProxyAssets(o Options, a Assets, caddyError error) (Assets, error) {
	switch o.Proxy { // Runtime choice does not imply a different edge.
	case "nginx-gateway": // The gateway owns public protocol forwarding and its private listener.
		return nginxGatewayAssets(o, a)
	case "nginx": // Standalone Nginx owns its public TLS listeners.
		return nginxAssets(o, a)
	case "nginx-existing": // Coexistence uses the operator's running service.
		return sharedNginxAssets(o, a)
	case "caddy": // The direct Caddy renderer uses the assets already generated above.
		return a, caddyError
	default: // An unknown proxy selection cannot produce an installation preview.
		return Assets{}, supportedGatewayProxyError()
	}
}

// validateInstallationPaths protects the controller destination and literal systemd arguments.
func validateInstallationPaths(o Options) error {
	// A controller must not replace a separately installed client executable.
	if filepath.Base(o.Executable) == "hostlier" {
		return model.Err("INSTALL_PATH", "Controller executable cannot replace the hostlier CLI")
	}
	// Validate all paths interpolated into unit files.
	for _, path := range []string{o.Executable, o.StateDir, o.ConfigPath} {
		// Exclude whitespace and metacharacters that could change systemd argument interpretation.
		if !regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`).MatchString(path) {
			return model.Err("INSTALL_PATH", "Installation paths must be absolute without whitespace")
		}
	}
	return nil
}
