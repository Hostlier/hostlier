package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hostlier/internal/fleet"
)

// TestCollectorTransportSeparatesAuthority verifies actual TLS before testing the narrow route boundary.
func TestCollectorTransportSeparatesAuthority(t *testing.T) {
	c := newTestController(t, t.TempDir(), &supervisedFixture{}, time.Now)
	defer c.Close(context.Background())
	c.opts.ManagedCollector = true
	peer, err := fleet.Generate()
	// Require the expected identity or route boundary before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := fleet.Fingerprint(peer.Certificate)
	before, _ := c.FleetFingerprint()
	c.opts.CollectorEndpoint = freeCollectorOrigin(t)
	// Require the expected identity or route boundary before proceeding.
	if err = c.Listen(ListenOptions{Host: "127.0.0.1", Port: 0, TCP: true, ControllerFingerprint: pin}); err != nil {
		t.Fatal(err)
	}
	c.collectorTransport.mu.Lock()
	identity := c.collectorTransport.identity
	c.collectorTransport.mu.Unlock()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(identity.CA))
	cert, err := tls.X509KeyPair([]byte(identity.ClientCertificate), []byte(identity.ClientKey))
	// Require the expected identity or route boundary before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	good := &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	token := collectorTransportToken(t, c)
	c.mu.Lock()
	admin := c.credentials("fixture")["token"].(string)
	c.mu.Unlock()
	verifyCollectorRoutes(t, c.opts.CollectorEndpoint, good, token, admin)
	unrelated, _ := tls.X509KeyPair([]byte(peer.Certificate), []byte(peer.PrivateKey))
	// Require the expected identity or route boundary before proceeding.
	for _, clientCert := range []tls.Certificate{{}, unrelated, expiredCollectorClient(t, identity)} {
		cfg := good.Clone()
		cfg.Certificates = nil
		// Require the expected identity or route boundary before proceeding.
		if len(clientCert.Certificate) > 0 {
			cfg.Certificates = []tls.Certificate{clientCert}
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: time.Second}
		response, err := client.Get(c.opts.CollectorEndpoint + "/v2/")
		// Require the expected identity or route boundary before proceeding.
		if response != nil {
			response.Body.Close()
		}
		// Require the expected identity or route boundary before proceeding.
		if err == nil {
			t.Fatal("untrusted client reached collector HTTP handler")
		}
		client.CloseIdleConnections()
	}
	pinAfter, _ := c.FleetFingerprint()
	// Require the expected identity or route boundary before proceeding.
	if pinAfter != before {
		t.Fatal("collector transport changed fleet identity")
	}
}

// freeCollectorOrigin reserves a concrete ephemeral address for the test's dedicated listener.
func freeCollectorOrigin(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	// Require the expected identity or route boundary before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return "https://" + address
}

// collectorTransportToken establishes real scoped authority without runtime image deployment.
func collectorTransportToken(t *testing.T, c *Controller) string {
	t.Helper()
	credentials, err := c.prepareManagedCredential()
	// Require the expected identity or route boundary before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	return credentials.Pending.Token
}

// verifyCollectorRoutes checks token gating and management denial after successful mutual TLS.
func verifyCollectorRoutes(t *testing.T, origin string, config *tls.Config, token, admin string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: config}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	// Require the expected identity or route boundary before proceeding.
	for _, test := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/v2/", "", 401},
		{"GET", "/v2/", token, 503},
		{"GET", "/v1/status", admin, 403},
		{"GET", "/v1/telemetry/history", token, 403},
		{"POST", "/v1/telemetry/otlp/logs", "", 401},
	} {
		request, _ := http.NewRequest(test.method, origin+test.path, nil)
		request.Header.Set("Authorization", "Bearer "+test.token)
		response, err := client.Do(request)
		// Require the expected identity or route boundary before proceeding.
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		// Require the expected identity or route boundary before proceeding.
		if response.StatusCode != test.status {
			t.Fatalf("%s %s: %d", test.method, test.path, response.StatusCode)
		}
	}
}

// expiredCollectorClient signs a formerly valid role against the same issuer to exercise TLS expiry checks.
func expiredCollectorClient(t *testing.T, identity *collectorTransportIdentity) tls.Certificate {
	t.Helper()
	issuer, key, err := collectorIssuer(identity, time.Now())
	// Require the expected identity or route boundary before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	cert, keyPEM, err := collectorCertificate("", issuer, key, false, true, time.Now().Add(-366*24*time.Hour))
	// Require the expected identity or route boundary before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(keyPEM))
	// Require the expected identity or route boundary before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

// TestCollectorTransportRenewsLeavesAndReusesIssuer verifies restart reuse and safe overlapping rotation.
func TestCollectorTransportRenewsLeavesAndReusesIssuer(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer c.Close(context.Background())
	first, err := c.loadCollectorIdentity("127.0.0.1")
	// Require the expected identity or route boundary before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.loadCollectorIdentity("127.0.0.1")
	// Require the expected identity or route boundary before proceeding.
	if err != nil || first.ClientCertificate != second.ClientCertificate || first.ServerCertificate != second.ServerCertificate {
		t.Fatal("restart did not reuse credentials", err)
	}
	original := *first
	changed, err := refreshCollectorIdentity(first, "127.0.0.1", time.Now().Add(340*24*time.Hour))
	// Require the expected identity or route boundary before proceeding.
	if err != nil || !changed || first.CA != original.CA || first.ClientCertificate == original.ClientCertificate {
		t.Fatal("leaf renewal failed", err)
	}
	// Require the expected identity or route boundary before proceeding.
	if !collectorLeafFresh(original.ClientCertificate, original.ClientKey, first.CA, "", x509.ExtKeyUsageClientAuth, time.Now()) {
		t.Fatal("prior client lost trust before rollout")
	}
	verifyCollectorMaterial(t, c, first)
}

// verifyCollectorMaterial checks private disk permissions and the exact public runtime credential surface.
func verifyCollectorMaterial(t *testing.T, c *Controller, first *collectorTransportIdentity) {
	t.Helper()
	info, err := os.Stat(filepath.Join(c.opts.StateDir, "collector-transport.json"))
	// Require the expected identity or route boundary before proceeding.
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("identity permissions", err)
	}
	material, _ := json.Marshal(first.collectorTransportMaterial())
	var delivered map[string]string
	json.Unmarshal(material, &delivered)
	// Require the expected identity or route boundary before proceeding.
	if len(delivered) != 3 || delivered["PrivateKey"] == first.CAKey || delivered["PrivateKey"] == first.ServerKey {
		t.Fatal("management or issuer key exposed")
	}
}
