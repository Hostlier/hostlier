package nginx

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"hostlier/internal/config"
)

// ensureCertificates uses the already-running HTTP challenge listener before adding TLS hosts.
func ensureCertificates(ctx context.Context, o Options, controller string, routes []Route) error {
	hosts := map[string]bool{}
	// Validate the selected edge ownership and configuration before proceeding.
	if controller != "" {
		hosts[controller] = true
	}
	// Apply the same validation to every owned edge record.
	for _, route := range routes {
		parsed, err := config.ParseRoute(route.Project.Route, nil, "")
		// Stop activation when the required local operation fails.
		if err != nil {
			return err
		}
		hosts[parsed.Hostname] = true
	}
	names := make([]string, 0, len(hosts))
	// Apply the same validation to every owned edge record.
	for host := range hosts {
		names = append(names, host)
	}
	sort.Strings(names)
	// Apply the same validation to every owned edge record.
	for _, host := range names {
		// Stop activation when the required local operation fails.
		if err := ensureCertificate(ctx, o, host); err != nil {
			return err
		}
	}
	return nil
}

// ensureCertificate reuses existing protected certificates or issues through ACME HTTP-01.
func ensureCertificate(ctx context.Context, o Options, host string) error {
	cert := filepath.Join(o.CertificateDirectory, "live", host, "fullchain.pem")
	key := filepath.Join(o.CertificateDirectory, "live", host, "privkey.pem")
	// Validate the selected edge ownership and configuration before proceeding.
	if _, err := os.Stat(cert); err == nil {
		// Validate the selected edge ownership and configuration before proceeding.
		if _, err = os.Stat(key); err == nil {
			return nil
		}
	}
	// The exact cert name keeps renewals and routing tied to the same host ownership.
	extra, err := localACMEArguments(o.ACMEDirectoryURL)
	// Validate this fixture boundary before continuing.
	if err != nil {
		return err
	}
	args := []string{"certonly", "--non-interactive", "--agree-tos", "--register-unsafely-without-email", "--webroot", "--webroot-path", o.ChallengeDirectory, "--config-dir", o.CertificateDirectory, "--cert-name", host, "-d", host}
	// Coexistence never shares Certbot work or log directories with the operator's renewal jobs.
	if o.IsolatedACME {
		args = append(args, "--config", "/dev/null", "--work-dir", filepath.Join(o.CertificateDirectory, "work"), "--logs-dir", filepath.Join(o.CertificateDirectory, "logs"))
	}
	// A Snap Certbot launcher cannot run with the controller service's
	// NoNewPrivileges and ProtectHome settings. The owned template contains the
	// fixed Certbot command; only a validated DNS hostname selects its instance.
	if o.CertificateUnit != "" && o.ACMEDirectoryURL == "" {
		if normalized, err := config.NormalizeDomain(host); err != nil {
			return err
		} else if normalized != host {
			return fmt.Errorf("certificate hostname must be canonical")
		}
		if err := o.Run(ctx, "systemctl", "start", o.CertificateUnit+host+".service"); err != nil {
			return fmt.Errorf("HTTPS certificate issuance unit failed for %s (unit: %s, Certbot log: %s): %w", host, o.CertificateUnit+host+".service", filepath.Join(o.CertificateDirectory, "logs", "letsencrypt.log"), err)
		}
		return nil
	}
	// Validate this fixture boundary before continuing.
	if err := o.Run(ctx, "certbot", append(args, extra...)...); err != nil {
		// The isolated log holds the complete ACME response when Certbot cannot summarize it.
		if o.IsolatedACME {
			return fmt.Errorf("HTTPS certificate issuance failed for %s (Certbot log: %s): %w", host, filepath.Join(o.CertificateDirectory, "logs", "letsencrypt.log"), err)
		}
		return fmt.Errorf("HTTPS certificate issuance failed for %s: %w", host, err)
	}
	return nil
}

// RenewalAssets supplies an owned timer whose deploy hook verifies configuration before reloading.
func RenewalAssets(o Options) (string, string, error) {
	o = defaults(o)
	// Validate the selected edge ownership and configuration before proceeding.
	if !safePath(o.Directory) || !safePath(o.CertificateDirectory) {
		return "", "", fmt.Errorf("unsafe renewal path")
	}
	service := fmt.Sprintf("[Unit]\nDescription=Renew Hostlier Nginx certificates\nAfter=network-online.target hostlier-nginx.service\n[Service]\nType=oneshot\nExecStart=/usr/bin/certbot renew --non-interactive --config-dir %s --deploy-hook '/usr/sbin/nginx -t -c %s/nginx.conf && /usr/sbin/nginx -s reload -c %s/nginx.conf'\n", o.CertificateDirectory, o.Directory, o.Directory)
	timer := "[Unit]\nDescription=Renew Hostlier TLS certificates twice daily\n[Timer]\nOnCalendar=*-*-* 00,12:00:00\nRandomizedDelaySec=3600\nPersistent=true\n[Install]\nWantedBy=timers.target\n"
	return service, timer, nil
}

// localACMEArguments permits a private fixture without changing trust or production defaults.
func localACMEArguments(directory string) ([]string, error) {
	// Validate this fixture boundary before continuing.
	if directory == "" {
		return nil, nil
	}
	u, err := url.Parse(directory)
	// Validate this fixture boundary before continuing.
	if err != nil {
		return nil, fmt.Errorf("invalid local ACME directory")
	}
	ip := net.ParseIP(u.Hostname())
	// Validate this fixture boundary before continuing.
	if u.Scheme != "https" || ip == nil || !ip.IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("test ACME directory must be an HTTPS loopback URL")
	}
	return []string{"--server", directory}, nil
}
