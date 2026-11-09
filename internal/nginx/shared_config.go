package nginx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hostlier/internal/config"
)

// CertificateReference leaves certificate files and their renewal under their existing owner's control.
type CertificateReference struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

// externalCertificates loads optional exact-host references; wildcard certificates can be referenced repeatedly.
func externalCertificates(o SharedOptions) (map[string]CertificateReference, error) {
	data, err := os.ReadFile(o.CertificatesFile)
	if errors.Is(err, os.ErrNotExist) { // Validate explicit references without modifying external certificate files.
		return map[string]CertificateReference{}, nil
	}
	if err != nil { // Validate explicit references without modifying external certificate files.
		return nil, err
	}
	var certificates map[string]CertificateReference
	if len(data) > 65536 || json.Unmarshal(data, &certificates) != nil { // Validate explicit references without modifying external certificate files.
		return nil, errors.New("invalid nginx-existing TLS reference file")
	}
	for host, cert := range certificates { // Validate explicit references without modifying external certificate files.
		normalized, err := config.NormalizeDomain(host)
		if err != nil || normalized != host { // Validate explicit references without modifying external certificate files.
			return nil, errors.New("TLS references require canonical exact hostnames")
		}
		if !safePath(cert.Certificate) || !safePath(cert.Key) { // Validate explicit references without modifying external certificate files.
			return nil, errors.New("unsafe external certificate path")
		}
	}
	return certificates, nil
}

// validateCertificate checks hostname coverage, validity and matching key without copying private material.
func validateCertificate(host string, ref CertificateReference) error {
	pair, err := tls.LoadX509KeyPair(ref.Certificate, ref.Key)
	if err != nil { // Require a usable key pair that covers the requested hostname.
		return fmt.Errorf("cannot load certificate and key for %s", host)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil { // Require a usable key pair that covers the requested hostname.
		return fmt.Errorf("invalid certificate for %s", host)
	}
	if err = cert.VerifyHostname(host); err != nil { // Require a usable key pair that covers the requested hostname.
		return fmt.Errorf("certificate does not cover %s", host)
	}
	if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) { // Require a usable key pair that covers the requested hostname.
		return fmt.Errorf("certificate for %s is not currently valid", host)
	}
	return nil
}

// sharedHosts produces stable exact-name reservations for all active public routes.
func sharedHosts(controller string, routes []Route) ([]string, error) {
	groups, err := groupRoutes(controller, routes)
	if err != nil { // Collect only validated Hostlier hostname reservations.
		return nil, err
	}
	hosts := make([]string, 0, len(groups))
	for host := range groups { // Collect only validated Hostlier hostname reservations.
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts, nil
}

// sharedCertificates combines explicit external references with Hostlier's isolated certificate store.
func sharedCertificates(o SharedOptions, hosts []string) (map[string]CertificateReference, error) {
	certificates, err := externalCertificates(o)
	if err != nil { // Preserve explicit references and allocate isolated paths for new certificates.
		return nil, err
	}
	for _, host := range hosts { // Preserve explicit references and allocate isolated paths for new certificates.
		if _, exists := certificates[host]; !exists { // Preserve explicit references and allocate isolated paths for new certificates.
			dir := filepath.Join(o.CertificateDirectory, "live", host)
			certificates[host] = CertificateReference{Certificate: filepath.Join(dir, "fullchain.pem"), Key: filepath.Join(dir, "privkey.pem")}
		}
	}
	return certificates, nil
}

// issueSharedCertificates never invokes Certbot for a reference owned by the operator.
func issueSharedCertificates(ctx context.Context, o SharedOptions, hosts []string, certificates map[string]CertificateReference) error {
	external, err := externalCertificates(o)
	if err != nil { // Keep external certificate renewal outside Hostlier management.
		return err
	}
	if err = os.MkdirAll(filepath.Join(o.ChallengeDirectory, ".well-known/acme-challenge"), 0755); err != nil { // Keep external certificate renewal outside Hostlier management.
		return err
	}
	for _, host := range hosts { // Keep external certificate renewal outside Hostlier management.
		if _, exists := external[host]; !exists { // Keep external certificate renewal outside Hostlier management.
			if err = ensureCertificate(ctx, o.Options, host); err != nil { // Keep external certificate renewal outside Hostlier management.
				return err
			}
		}
		if err = validateCertificate(host, certificates[host]); err != nil { // Keep external certificate renewal outside Hostlier management.
			return err
		}
	}
	return nil
}

// renderShared emits only named virtual hosts; process, default-server and unrelated site settings stay external.
func renderShared(o SharedOptions, controller, upstream string, routes []Route, certificates map[string]CertificateReference, bootstrap bool) ([]byte, error) {
	groups, err := groupRoutes(controller, routes)
	if err != nil { // Generate only named Hostlier virtual hosts with valid routes.
		return nil, err
	}
	hosts, err := sharedHosts(controller, routes)
	if err != nil { // Generate only named Hostlier virtual hosts with valid routes.
		return nil, err
	}
	if !validListener(o.HTTPAddress) || !validListener(o.HTTPSAddress) { // Generate only named Hostlier virtual hosts with valid routes.
		return nil, errors.New("invalid shared Nginx listener")
	}
	var body strings.Builder
	body.WriteString("# Generated by Hostlier. Manage existing sites in their own files.\nmap $http_upgrade $hostlier_connection_upgrade { default upgrade; '' close; }\n")
	for _, host := range hosts { // Generate only named Hostlier virtual hosts with valid routes.
		fmt.Fprintf(&body, "server { listen %s; server_name %s; location ^~ /.well-known/acme-challenge/ { root %s; } location / { return 308 https://$host$request_uri; } }\n", o.HTTPAddress, host, o.ChallengeDirectory)
		ref := certificates[host]
		// A new hostname first receives only HTTP-01; existing TLS hosts remain available during issuance.
		if bootstrap && certificateAbsent(ref) {
			continue
		}
		text, err := hostServer(o.Options, host, controller, upstream, groups[host])
		if err != nil { // Generate only named Hostlier virtual hosts with valid routes.
			return nil, err
		}
		owned := filepath.Join(o.CertificateDirectory, "live", host)
		text = strings.ReplaceAll(text, owned+"/fullchain.pem", ref.Certificate)
		text = strings.ReplaceAll(text, owned+"/privkey.pem", ref.Key)
		body.WriteString(strings.ReplaceAll(text, "$connection_upgrade", "$hostlier_connection_upgrade"))
	}
	return []byte(body.String()), nil
}

// certificateAbsent allows bootstrap only for missing files, not unreadable or malformed certificates.
func certificateAbsent(ref CertificateReference) bool {
	_, certErr := os.Stat(ref.Certificate)
	_, keyErr := os.Stat(ref.Key)
	return errors.Is(certErr, os.ErrNotExist) || errors.Is(keyErr, os.ErrNotExist)
}

// SharedRenewalAssets renews only Hostlier's ACME account and certificate lineages.
func SharedRenewalAssets(options SharedOptions) (string, string, error) {
	o := SharedDefaults(options)
	if err := sharedPaths(o); err != nil { // Validate paths before rendering the isolated renewal unit.
		return "", "", err
	}
	_, timer, err := RenewalAssets(o.Options)
	service := fmt.Sprintf("[Unit]\nDescription=Renew Hostlier coexistence certificates\nAfter=network-online.target nginx.service\n[Service]\nType=oneshot\nExecStart=/usr/bin/certbot renew --non-interactive --config /dev/null --config-dir %s --work-dir %s/work --logs-dir %s/logs --deploy-hook '/usr/sbin/nginx -t -c %s && /usr/sbin/nginx -s reload -c %s'\n", o.CertificateDirectory, o.CertificateDirectory, o.CertificateDirectory, o.MainConfig, o.MainConfig)
	return service, timer, err
}

// SharedCertificateServiceAssets issues one exact hostname in an isolated
// root-owned job. Snap's launcher needs its own privilege and home namespace;
// the long-running controller keeps its existing service protections.
func SharedCertificateServiceAssets(options SharedOptions) (string, error) {
	o := SharedDefaults(options)
	if err := sharedPaths(o); err != nil {
		return "", err
	}
	return fmt.Sprintf(`[Unit]
Description=Issue Hostlier coexistence certificate for %%I
After=network-online.target nginx.service
Wants=network-online.target

[Service]
Type=oneshot
User=root
UMask=0077
PrivateTmp=true
NoNewPrivileges=false
ProtectHome=false
TimeoutStartSec=5min
ExecStart=/usr/bin/certbot certonly --non-interactive --agree-tos --register-unsafely-without-email --webroot --webroot-path %s --config-dir %s --cert-name %%i -d %%i --config /dev/null --work-dir %s/work --logs-dir %s/logs
`, o.ChallengeDirectory, o.CertificateDirectory, o.CertificateDirectory, o.CertificateDirectory), nil
}
