package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"hostlier/internal/model"
)

var errCollectorHTTPSPending = errors.New("Waiting for controller HTTPS and collector registry readiness")

// reconcileReadyManagedCollector gates runtime pulls and credential creation on verified public readiness.
func (c *Controller) reconcileReadyManagedCollector(ctx context.Context) error {
	endpoint, material, err := c.collectorDeploymentTLS()
	// Transport identity failures must not lead to an unauthenticated fallback.
	if err != nil {
		return errors.New("Collector transport identity refresh failed")
	}
	origin, err := c.managedCollectorOrigin(endpoint)
	// Invalid configuration is distinct from a certificate still being provisioned.
	if err != nil {
		return err
	}
	config, err := collectorReadinessTLS(material, c.opts.CollectorCAFile)
	// Trust configuration cannot be bypassed while waiting for HTTPS.
	if err != nil {
		return errors.New("Collector HTTPS trust configuration is invalid")
	}
	transport := &http.Transport{TLSClientConfig: config, TLSHandshakeTimeout: 3 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { // Keep readiness confined to the configured origin.
		return http.ErrUseLastResponse // Only the configured origin may satisfy readiness.
	}}
	// Probe metadata without a token or an image download before invoking the runtime.
	if problem := collectorRegistryProblem(ctx, client, origin.String()+"/v2/"); problem != "" {
		return fmt.Errorf("%w: %s", errCollectorHTTPSPending, problem)
	}
	return c.reconcileManagedCollector(ctx)
}

// collectorRegistryProblem requires verified TLS and the exact protected registry challenge.
func collectorRegistryProblem(ctx context.Context, client *http.Client, address string) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, address, nil)
	// Request errors cannot authorize runtime side effects.
	if err != nil {
		return "invalid collector registry URL"
	}
	response, err := client.Do(request)
	// Listener availability alone cannot satisfy certificate and routing readiness.
	if err != nil {
		var dns *net.DNSError
		var authority x509.UnknownAuthorityError
		var hostname x509.HostnameError
		var network net.Error
		switch {
		case errors.As(err, &dns):
			return "controller hostname DNS lookup failed"
		case errors.As(err, &authority):
			return "controller HTTPS certificate is not trusted"
		case errors.As(err, &hostname):
			return "controller HTTPS certificate does not match its hostname"
		case errors.As(err, &network) && network.Timeout():
			return "controller HTTPS connection timed out"
		default:
			return "controller HTTPS request failed; inspect DNS, port 443 and certificate trust"
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		return fmt.Sprintf("collector registry returned HTTP %d instead of 401", response.StatusCode)
	}
	if response.Header.Get("Docker-Distribution-Api-Version") != "registry/2.0" {
		return "collector registry response lacks its registry/2.0 header"
	}
	if response.Header.Get("WWW-Authenticate") != `Basic realm="Hostlier collector"` {
		return "collector registry response lacks its expected authentication challenge"
	}
	return ""
}

// collectorReadinessTLS uses the collector's normal trust, including its isolated fleet identity.
func collectorReadinessTLS(material *model.CollectorTLS, caFile string) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	// Fleet transport requires its dedicated issuer and client certificate on the probe too.
	if material != nil {
		cert, err := tls.X509KeyPair([]byte(material.Certificate), []byte(material.PrivateKey))
		// An invalid client identity must not be silently omitted.
		if err != nil {
			return nil, err
		}
		config.Certificates = []tls.Certificate{cert}
		config.RootCAs = x509.NewCertPool()
		// The isolated collector issuer is the only trusted fleet certificate authority.
		if !config.RootCAs.AppendCertsFromPEM([]byte(material.CA)) {
			return nil, errors.New("invalid collector issuer")
		}
		return config, nil
	}
	// Public HTTPS uses standard system roots without any verification override.
	if caFile == "" {
		return config, nil
	}
	data, err := os.ReadFile(caFile)
	// A configured private CA must be readable before any request is attempted.
	if err != nil {
		return nil, err
	}
	config.RootCAs, err = x509.SystemCertPool()
	// Systems without an available trust bundle can still use the explicit private issuer.
	if err != nil {
		config.RootCAs = x509.NewCertPool()
	}
	// Invalid CA bytes cannot become implicit permission to skip verification.
	if !config.RootCAs.AppendCertsFromPEM(data) {
		return nil, errors.New("invalid collector CA")
	}
	return config, nil
}
