package nginx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hostlier/internal/model"
)

// liveEdge owns a real Nginx master, private upstream and trusted local certificate fixture.
type liveEdge struct {
	options  Options
	upstream *httptest.Server
	client   *http.Client
	address  string
	routes   []Route
}

// TestLiveNginxRoutingTLSAndReload validates a real proxy without public ACME or external credentials.
func TestLiveNginxRoutingTLSAndReload(t *testing.T) {
	// Real process tests are explicit because the host need not have Nginx installed.
	if os.Getenv("HOSTLIER_NGINX_E2E") != "1" {
		t.Skip("set HOSTLIER_NGINX_E2E=1 with nginx installed")
	}
	edge := startLiveEdge(t)
	assertLiveRequest(t, edge, "app.example.test", "/api/hello?value=yes", 200, "/hello?value=yes")
	assertLiveRequest(t, edge, "app.example.test", "/apix/hello", 404, "")
	assertLiveRequest(t, edge, "controller.example.test", "/health", 200, "/health")
	assertLiveStream(t, edge)
	assertLiveUpgrade(t, edge)
	// A syntax error must fail before replacing the active configuration.
	if err := Activate(context.Background(), edge.options, []byte("invalid nginx syntax;")); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	assertLiveRequest(t, edge, "app.example.test", "/api/after-rejection", 200, "/after-rejection")
	assertLiveRenewal(t, edge)
	edge.routes[0].Unavailable = true
	manager := Manager{Options: edge.options}
	// Scaling to zero retains the owned host/path and presents an explicit unavailable response.
	if err := manager.Apply(context.Background(), "controller.example.test", strings.TrimPrefix(edge.upstream.URL, "http://"), edge.routes); err != nil {
		t.Fatal(err)
	}
	assertLiveRequest(t, edge, "app.example.test", "/api/", 503, "")
}

// startLiveEdge creates the same bootstrap-then-TLS sequence used by approved installation.
func startLiveEdge(t *testing.T) *liveEdge {
	t.Helper()
	directory := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(liveUpstream))
	t.Cleanup(upstream.Close)
	o := Options{Directory: filepath.Join(directory, "edge"), CertificateDirectory: filepath.Join(directory, "certs"), ChallengeDirectory: filepath.Join(directory, "acme"), HTTPAddress: freeAddress(t), HTTPSAddress: freeAddress(t)}
	roots := writeLiveCertificates(t, o, 1)
	data, err := Bootstrap(o)
	// Initial HTTP challenge configuration requires no pre-existing TLS certificate.
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(o.Directory, 0700)
	writeConfig(filepath.Join(o.Directory, "nginx.conf"), data)
	// Start a separate master using high loopback ports without changing system Nginx.
	if err := exec.Command("nginx", "-c", filepath.Join(o.Directory, "nginx.conf")).Run(); err != nil {
		t.Fatalf("nginx: %v", err)
	}
	// Order specific routes before their parent locations.
	t.Cleanup(func() { exec.Command("nginx", "-s", "quit", "-c", filepath.Join(o.Directory, "nginx.conf")).Run() })
	route := Route{Project: model.Project{Name: "app", Route: "app.example.test/api", Proxy: &model.Proxy{StripPrefix: true}}, Upstreams: []string{strings.TrimPrefix(upstream.URL, "http://")}}
	manager := Manager{Options: o}
	// Local certificates prove TLS routing while production invokes the ACME webroot client.
	if err := manager.Apply(context.Background(), "controller.example.test", strings.TrimPrefix(upstream.URL, "http://"), []Route{route}); err != nil {
		t.Fatal(err)
	}
	// Order specific routes before their parent locations.
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", o.HTTPSAddress)
	}}
	return &liveEdge{options: o, upstream: upstream, client: &http.Client{Transport: transport, Timeout: 3 * time.Second}, address: o.HTTPSAddress, routes: []Route{route}}
}

// liveUpstream exposes normal, streamed and upgraded traffic through the proxy.
func liveUpstream(writer http.ResponseWriter, request *http.Request) {
	// Upgrade handling verifies the headers passed by Nginx without a WebSocket dependency.
	if request.Header.Get("Upgrade") == "websocket" {
		connection, buffer, _ := writer.(http.Hijacker).Hijack()
		defer connection.Close()
		buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		buffer.Flush()
		return
	}
	// Flush an event immediately so client-side streaming does not depend on response completion.
	if request.URL.Path == "/stream" {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: first\n\n")
		writer.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		return
	}
	fmt.Fprint(writer, request.URL.RequestURI())
}

// freeAddress obtains a high loopback port for a standalone proxy fixture.
func freeAddress(t *testing.T) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	// Allocate only available local ports and release immediately before fixture startup.
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

// assertLiveRequest retries brief reload propagation and checks routing over verified HTTPS.
func assertLiveRequest(t *testing.T, edge *liveEdge, host, path string, status int, body string) {
	t.Helper()
	var failure string
	// A HUP reload may require a short interval before new workers accept connections.
	for attempt := 0; attempt < 30; attempt++ {
		response, err := edge.client.Get("https://" + host + path)
		// TLS or connection readiness failures can be transient during worker replacement.
		if err != nil {
			failure = err.Error()
			time.Sleep(30 * time.Millisecond)
			continue
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		// A matching response proves the host, segment boundary and optional rewrite together.
		if response.StatusCode == status && (body == "" || string(data) == body) {
			return
		}
		failure = fmt.Sprintf("status=%d body=%q", response.StatusCode, data)
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal(failure)
}

// assertLiveStream proves the first SSE event crosses Nginx before upstream completion.
func assertLiveStream(t *testing.T, edge *liveEdge) {
	response, err := edge.client.Get("https://app.example.test/api/stream")
	// A valid certificate and successful proxy response are required before reading the event.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data := make([]byte, len("data: first\n\n"))
	// Exact event framing survives unbuffered proxying.
	if _, err = io.ReadFull(response.Body, data); err != nil || string(data) != "data: first\n\n" {
		t.Fatal(string(data), err)
	}
}

// assertLiveUpgrade confirms a real HTTP upgrade handshake reaches the private upstream.
func assertLiveUpgrade(t *testing.T, edge *liveEdge) {
	request, _ := http.NewRequest("GET", "https://app.example.test/api/upgrade", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	response, err := edge.client.Do(request)
	// A 101 response confirms both upgrade headers were preserved.
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	// The upstream fixture never returns 101 for ordinary proxied requests.
	if response.StatusCode != 101 {
		t.Fatal(response.StatusCode)
	}
}

// writeLiveCertificates creates a trusted local CA certificate for every owned hostname.
func writeLiveCertificates(t *testing.T, o Options, serial int64) *x509.CertPool {
	return writeCertificatesForHosts(t, o, serial, []string{"app.example.test", "controller.example.test"})
}

// writeCertificatesForHosts creates independent certificate stores for coexistence fixtures.
func writeCertificatesForHosts(t *testing.T, o Options, serial int64, hosts []string) *x509.CertPool {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// Fixture identity generation must use secure randomness just like production certificates.
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "Hostlier local test CA"}, DNSNames: hosts, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	// The certificate must be structurally valid before configuring Nginx.
	if err != nil {
		t.Fatal(err)
	}
	private, _ := x509.MarshalECPrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})
	// Both server blocks intentionally share this local fixture certificate.
	for _, host := range template.DNSNames {
		dir := filepath.Join(o.CertificateDirectory, "live", host)
		os.MkdirAll(dir, 0700)
		// Replace inodes like Certbot's renewed lineage so Nginx's SSL object cache observes rotation.
		writeConfig(filepath.Join(dir, "fullchain.pem"), certPEM)
		writeConfig(filepath.Join(dir, "privkey.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}))
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return roots
}

// assertLiveRenewal rotates the certificate fixture and reloads through the production activation path.
func assertLiveRenewal(t *testing.T, edge *liveEdge) {
	roots := writeLiveCertificates(t, edge.options, 2)
	manager := Manager{Options: edge.options}
	// Renewal activation uses the same config test and reload mechanism as route changes.
	if err := manager.Apply(context.Background(), "controller.example.test", strings.TrimPrefix(edge.upstream.URL, "http://"), edge.routes); err != nil {
		t.Fatal(err)
	}
	transport := edge.client.Transport.(*http.Transport)
	transport.CloseIdleConnections()
	transport.TLSClientConfig.RootCAs = roots
	assertLiveRequest(t, edge, "app.example.test", "/api/renewed", 200, "/renewed")
}
