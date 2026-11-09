package nginx

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveACMEIssuanceAndRenewal uses Pebble's real HTTP-01 validator and system Certbot.
func TestLiveACMEIssuanceAndRenewal(t *testing.T) {
	fixture := os.Getenv("HOSTLIER_ACME_E2E")
	// Validate this fixture boundary before continuing.
	if fixture == "" {
		t.Skip("run test/e2e/acme_vm.py in a disposable VM")
	}
	o := startACMEEdge(t, fixture)

	// Validate this fixture boundary before continuing.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "acme-http01-ok") }))
	defer upstream.Close()
	manager := Manager{Options: o}
	// Validate this fixture boundary before continuing.
	if err := manager.Apply(context.Background(), "acme.hostlier.test", strings.TrimPrefix(upstream.URL, "http://"), nil); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	pem, err := os.ReadFile(filepath.Join(fixture, "issuer.crt"))
	// Validate this fixture boundary before continuing.
	if err != nil || !roots.AppendCertsFromPEM(pem) {
		t.Fatal("missing fixture issuer root", err)
	}
	time.Sleep(300 * time.Millisecond)
	before := acmeSerial(t, o, roots)
	hook := generatedACMEHook(t, o)
	// Validate this fixture boundary before continuing.
	if err := o.Run(context.Background(), "certbot", "renew", "--no-random-sleep-on-renew", "--force-renewal", "--non-interactive", "--config-dir", o.CertificateDirectory, "--deploy-hook", hook); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	// Validate this fixture boundary before continuing.
	for time.Now().Before(deadline) {
		after := acmeSerial(t, o, roots)
		// Validate this fixture boundary before continuing.
		if after != before {
			t.Logf("real HTTP-01 issuance and renewal: serial %s -> %s; trusted HTTPS and validated reload passed", before, after)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("renewal did not activate a new certificate")
}

// startACMEEdge starts an isolated real Nginx master with a readable challenge root.
func startACMEEdge(t *testing.T, fixture string) Options {
	t.Helper()
	challenge, err := os.MkdirTemp("", "hostlier-acme-webroot-")
	// Validate this fixture boundary before continuing.
	if err != nil {
		t.Fatal(err)
	}
	// Remove the isolated public challenge directory after the owned master exits.
	t.Cleanup(func() { os.RemoveAll(challenge) })
	os.Chmod(challenge, 0755)
	o := Options{Directory: filepath.Join(t.TempDir(), "edge"), CertificateDirectory: filepath.Join(t.TempDir(), "certs"), ChallengeDirectory: challenge, HTTPAddress: "127.0.0.1:5002", HTTPSAddress: freeAddress(t), ACMEDirectoryURL: "https://127.0.0.1:14000/dir"}
	// Validate this fixture boundary before continuing.
	o.Run = func(ctx context.Context, file string, args ...string) error {
		command := exec.CommandContext(ctx, file, args...)
		command.Env = append(os.Environ(), "REQUESTS_CA_BUNDLE="+filepath.Join(fixture, "server.crt"))
		command.Stdout = os.Stderr
		command.Stderr = os.Stderr
		err := command.Run()
		// Validate this fixture boundary before continuing.
		if err != nil {
			return fmt.Errorf("%s failed: %w", file, err)
		}
		return nil
	}
	os.MkdirAll(o.Directory, 0700)
	os.MkdirAll(o.ChallengeDirectory, 0755)
	data, err := Bootstrap(o)
	// Validate this fixture boundary before continuing.
	if err != nil {
		t.Fatal(err)
	}
	// Validate this fixture boundary before continuing.
	if err = writeConfig(filepath.Join(o.Directory, "nginx.conf"), data); err != nil {
		t.Fatal(err)
	}
	// Validate this fixture boundary before continuing.
	if err = o.Run(context.Background(), "nginx", "-c", filepath.Join(o.Directory, "nginx.conf")); err != nil {
		t.Fatal(err)
	}
	// Validate this fixture boundary before continuing.
	t.Cleanup(func() {
		o.Run(context.Background(), "nginx", "-s", "quit", "-c", filepath.Join(o.Directory, "nginx.conf"))
	})
	return o
}

// acmeSerial checks hostname and CA verification against a new connection after each reload.
func acmeSerial(t *testing.T, o Options, roots *x509.CertPool) string {
	t.Helper()
	connection, err := tls.Dial("tcp", o.HTTPSAddress, &tls.Config{RootCAs: roots, ServerName: "acme.hostlier.test", MinVersion: tls.VersionTLS12})
	// Validate this fixture boundary before continuing.
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(connection, "GET / HTTP/1.1\r\nHost: acme.hostlier.test\r\nConnection: close\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	// A verified certificate must also reach the actual application through the edge.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	// Renewal must preserve the trusted HTTP route as well as changing the certificate.
	if err != nil || response.StatusCode != 200 || string(body) != "acme-http01-ok" {
		t.Fatal("trusted application route failed", err)
	}
	return connection.ConnectionState().PeerCertificates[0].SerialNumber.String()
}

// generatedACMEHook obtains the deploy hook from the actual installed renewal service asset.
func generatedACMEHook(t *testing.T, o Options) string {
	t.Helper()
	service, _, err := RenewalAssets(o)
	// Invalid service paths cannot be used to manufacture a shell hook.
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(service, "--deploy-hook '", 2)
	// A missing deploy hook is a regression even when manual reload would succeed.
	if len(parts) != 2 {
		t.Fatal("renewal asset omitted deploy hook")
	}
	return strings.TrimSuffix(parts[1], "'\n")
}
