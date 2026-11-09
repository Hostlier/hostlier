package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type collectorProbeRoundTripper func(*http.Request) (*http.Response, error)

func (f collectorProbeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// TestCollectorRegistryProblem identifies the failed readiness prerequisite without exposing transport text.
func TestCollectorRegistryProblem(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header http.Header
		err    error
		want   string
	}{
		{name: "DNS", err: &net.DNSError{Err: "lookup failed", Name: "hostlier.example.com"}, want: "controller hostname DNS lookup failed"},
		{name: "TLS", err: x509.UnknownAuthorityError{}, want: "controller HTTPS certificate is not trusted"},
		{name: "proxy", status: http.StatusBadGateway, want: "collector registry returned HTTP 502 instead of 401"},
		{name: "registry header", status: http.StatusUnauthorized, want: "collector registry response lacks its registry/2.0 header"},
		{name: "ready", status: http.StatusUnauthorized, header: http.Header{"Docker-Distribution-Api-Version": []string{"registry/2.0"}, "Www-Authenticate": []string{`Basic realm="Hostlier collector"`}}, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: collectorProbeRoundTripper(func(*http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Header: tc.header, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			if got := collectorRegistryProblem(context.Background(), client, "https://hostlier.example.com/v2/"); got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// TestManagedCollectorWaitsForHTTPSAndRegistry runs real supervision through delayed TLS and routing readiness.
func TestManagedCollectorWaitsForHTTPSAndRegistry(t *testing.T) {
	c, runtime, _ := managedFixture(t)
	var certificateReady, routeReady atomic.Bool
	var tlsAttempts, routeAttempts atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // Model proxy activation separately from certificate issuance.
		routeAttempts.Add(1)
		// A valid certificate does not imply the registry has been routed to the controller yet.
		if !routeReady.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		c.Handler().ServeHTTP(w, r)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { // Delay the certificate without ever disabling client verification.
		tlsAttempts.Add(1)
		// Simulate ACME still provisioning the public certificate.
		if !certificateReady.Load() {
			return nil, errors.New("certificate pending")
		}
		return nil, nil
	}}
	server.StartTLS()
	defer server.Close()
	c.opts.CollectorEndpoint = server.URL
	c.opts.CollectorCAFile = collectorProbeCA(t, server.Certificate().Raw)
	c.opts.CollectorInterval = 10 * time.Millisecond
	c.wg.Add(1)
	go c.runManagedCollector()
	awaitCollectorProbe(t, func() bool { // Observe a failed TLS handshake and its nonfailure bootstrap status.
		return tlsAttempts.Load() > 0 && strings.HasPrefix(c.managedCollectorHealth().Reason, errCollectorHTTPSPending.Error())
	})
	assertCollectorNotProvisioned(t, c, runtime)
	certificateReady.Store(true)
	awaitCollectorProbe(t, func() bool { // Wait until trusted TLS reaches the still-unavailable registry route.
		return routeAttempts.Load() > 0
	})
	assertCollectorNotProvisioned(t, c, runtime)
	routeReady.Store(true)
	awaitCollectorProbe(t, func() bool { // Successful routing must trigger provisioning automatically.
		return c.managedCollectorHealth().State == "ready"
	})
	runtime.guard.Lock()
	defer runtime.guard.Unlock()
	// The first provisioning attempt happens only after both prerequisites succeed.
	if len(runtime.attempts) == 0 {
		t.Fatal("ready endpoint never triggered provisioning")
	}
}

// assertCollectorNotProvisioned checks that prerequisite waits produce no pull authority or runtime changes.
func assertCollectorNotProvisioned(t *testing.T, c *Controller, runtime *supervisedFixture) {
	t.Helper()
	runtime.guard.Lock()
	defer runtime.guard.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	// Pending HTTPS must leave both runtime downloads and credential allocation untouched.
	if len(runtime.attempts) != 0 || len(c.state.TelemetryCollectors) != 0 {
		t.Fatal("collector provisioned before HTTPS readiness")
	}
	// Expected bootstrap waiting is visible without reporting a failed installation.
	if c.managedCollectorHealth().State != "starting" {
		t.Fatal("certificate wait reported as degraded")
	}
}

// collectorProbeCA writes only public trust material for the local HTTPS fixture.
func collectorProbeCA(t *testing.T, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	// Use actual certificate validation rather than a test-only verification bypass.
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// awaitCollectorProbe bounds the asynchronous supervision test without assuming scheduling order.
func awaitCollectorProbe(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	// Poll only until the required observable transition occurs.
	for time.Now().Before(deadline) {
		// Return only after the requested observable transition.
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("collector readiness transition timed out")
}

// TestCollectorHTTPSRejectsUntrustedCertificateAndRedirects prevents fallback to insecure or unrelated routes.
func TestCollectorHTTPSRejectsUntrustedCertificateAndRedirects(t *testing.T) {
	c, runtime, _ := managedFixture(t)
	var redirect atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // Mirror the registry challenge unless redirect behavior is under test.
		if redirect.Load() {
			http.Redirect(w, r, "/v2/elsewhere", http.StatusTemporaryRedirect)
			return
		}
		c.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	c.opts.CollectorEndpoint = server.URL
	// A self-signed endpoint without its explicit CA must remain pending.
	if err := c.reconcileReadyManagedCollector(t.Context()); !errors.Is(err, errCollectorHTTPSPending) {
		t.Fatal(err)
	}
	c.opts.CollectorCAFile = collectorProbeCA(t, server.Certificate().Raw)
	redirect.Store(true)
	// Even trusted HTTPS cannot authorize a redirect to a different registry route.
	if err := c.reconcileReadyManagedCollector(t.Context()); !errors.Is(err, errCollectorHTTPSPending) {
		t.Fatal(err)
	}
	// Neither rejected probe is permitted to invoke runtime installation.
	if len(runtime.attempts) != 0 {
		t.Fatal("invalid endpoint invoked collector runtime")
	}
	redirect.Store(false)
	// A correctly routed endpoint with the configured CA now permits installation.
	if err := c.reconcileReadyManagedCollector(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestCollectorReadinessUsesFleetMutualTLS verifies the isolated collector identity on the actual probe.
func TestCollectorReadinessUsesFleetMutualTLS(t *testing.T) {
	c, runtime, _ := managedFixture(t)
	identity, err := c.loadCollectorIdentity("127.0.0.1")
	// The fixture uses the production issuer and collector-only client role.
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := identity.collectorServerCertificate()
	// Serving certificate generation must succeed before starting TLS.
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(identity.CA))
	server := httptest.NewUnstartedServer(http.HandlerFunc(c.serveCollectorTransport))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	defer server.Close()
	c.collectorTransport.identity = identity
	c.collectorTransport.endpoint = server.URL
	// Readiness must authenticate with the dedicated collector certificate and issuer.
	if err := c.reconcileReadyManagedCollector(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The runtime must receive the same dedicated TLS identity checked by readiness.
	if len(runtime.attempts) != 1 || runtime.attempts[0].TLS == nil {
		t.Fatal("fleet collector not provisioned with isolated TLS")
	}
}
