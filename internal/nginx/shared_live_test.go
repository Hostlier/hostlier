package nginx

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveSharedNginxPreservesSitesAndTLS exercises coexistence against a real already-running master.
func TestLiveSharedNginxPreservesSitesAndTLS(t *testing.T) {
	if os.Getenv("HOSTLIER_NGINX_E2E") != "1" { // Require this fixture step to succeed before checking coexistence behavior.
		t.Skip("set HOSTLIER_NGINX_E2E=1 with nginx installed")
	}
	o := sharedFixture(t)
	o.Dump, o.Run = dumpConfiguration, runCommand
	o.HTTPAddress, o.HTTPSAddress = freeAddress(t), freeAddress(t)
	external := Options{CertificateDirectory: filepath.Join(t.TempDir(), "external-certs")}
	roots := writeCertificatesForHosts(t, external, 11, []string{"legacy.example.test"})
	writeLiveCertificates(t, o.Options, 12)
	certDir := filepath.Join(external.CertificateDirectory, "live", "legacy.example.test")
	newCert, _ := os.ReadFile(filepath.Join(o.CertificateDirectory, "live", "app.example.test", "fullchain.pem"))
	roots.AppendCertsFromPEM(newCert)
	main := fmt.Sprintf("pid %s/nginx.pid; error_log stderr; events {} http { access_log off; include %s/*.conf; server { listen %s default_server; listen %s ssl default_server; server_name legacy.example.test; ssl_certificate %s/fullchain.pem; ssl_certificate_key %s/privkey.pem; return 200 'existing site'; } }", filepath.Dir(o.MainConfig), filepath.Dir(o.Fragment), o.HTTPAddress, o.HTTPSAddress, certDir, certDir)
	if err := os.WriteFile(o.MainConfig, []byte(main), 0600); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	if err := exec.Command("nginx", "-c", o.MainConfig).Run(); err != nil { // Fail if nginx: .
		t.Fatalf("nginx: %v", err)
	}
	t.Cleanup(func() { exec.Command("nginx", "-s", "quit", "-c", o.MainConfig).Run() })                                                                // Capture this fixture operation independently of real host service changes.
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { // Capture this fixture operation independently of real host service changes.
		return (&net.Dialer{}).DialContext(ctx, "tcp", o.HTTPSAddress)
	}}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	t.Cleanup(transport.CloseIdleConnections)
	fixture := &liveEdge{client: client, address: o.HTTPSAddress}
	assertLiveRequest(t, fixture, "legacy.example.test", "/before", 200, "existing site")
	upstream := httptest.NewServer(http.HandlerFunc(liveUpstream))
	t.Cleanup(upstream.Close)
	routes := sharedRoute()
	routes[0].Upstreams = []string{strings.TrimPrefix(upstream.URL, "http://")}
	if err := ApplyShared(context.Background(), o, "controller.example.test", routes[0].Upstreams[0], routes); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	assertLiveRequest(t, fixture, "app.example.test", "/api/new", 200, "/api/new")
	assertLiveRequest(t, fixture, "legacy.example.test", "/after", 200, "existing site")
	// A conflict must be rejected while both existing and Hostlier-managed traffic continue working.
	routes[0].Project.Route = "legacy.example.test/api"
	if err := ApplyShared(context.Background(), o, "controller.example.test", routes[0].Upstreams[0], routes); err == nil { // Fail if external hostname taken over.
		t.Fatal("external hostname taken over")
	}
	assertLiveRequest(t, fixture, "legacy.example.test", "/still-running", 200, "existing site")
	assertLiveRequest(t, fixture, "app.example.test", "/api/still-running", 200, "/api/still-running")
	if err := ApplyShared(context.Background(), o, "controller.example.test", routes[0].Upstreams[0], nil); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	assertLiveRequest(t, fixture, "legacy.example.test", "/after-delete", 200, "existing site")
	data, _ := os.ReadFile(o.MainConfig)
	if string(data) != main { // Fail if existing configuration modified.
		t.Fatal("existing configuration modified")
	}
	// External renewal stays independent: a rotated certificate is picked up by the operator's reload.
	roots = writeCertificatesForHosts(t, external, 13, []string{"legacy.example.test"})
	roots.AppendCertsFromPEM(newCert)
	transport.CloseIdleConnections()
	transport.TLSClientConfig.RootCAs = roots
	if err := runCommand(context.Background(), "nginx", "-s", "reload", "-c", o.MainConfig); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	assertLiveRequest(t, fixture, "legacy.example.test", "/renewed", 200, "existing site")
}
