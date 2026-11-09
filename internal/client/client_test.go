package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"hostlier/internal/fleet"
)

// TestHTTPSAndPrivateCA verifies HTTPS origin restrictions, certificate trust and bearer delivery.
func TestHTTPSAndPrivateCA(t *testing.T) {
	// Exercise URL forms that could expose credentials or bypass the HTTPS origin restriction.
	for _, address := range []string{"http://example.com", "https://u:p@example.com", "https://example.com/path", "https://example.com/?token=secret", "https://example.com/#x"} {
		// Fail if an unsafe controller origin is accepted.
		if _, e := ControllerURL(address); e == nil {
			t.Errorf("accepted %s", address)
		}
	}
	// Serve a real TLS response while checking credential delivery.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the enrolled bearer token reaches the intended server.
		if r.Header.Get("Authorization") != "Bearer credential" {
			t.Error("missing credential")
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer server.Close()
	c, e := New(Options{URL: server.URL, Token: "credential"})
	// Require client construction before testing its trust policy.
	if e != nil {
		t.Fatal(e)
	}
	var result map[string]bool
	// An untrusted server certificate must fail verified HTTPS.
	if c.Request(context.Background(), "GET", "/health", nil, &result) == nil {
		t.Fatal("untrusted test certificate accepted")
	}
	file := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600)
	c, e = New(Options{URL: server.URL, CAFile: file, Token: "credential"})
	// Require construction with the explicitly trusted test CA.
	if e != nil {
		t.Fatal(e)
	}
	// Verify the same server succeeds only after its CA is trusted.
	if e = c.Request(context.Background(), "GET", "/health", nil, &result); e != nil || !result["ok"] {
		t.Fatalf("trusted TLS request: %v %#v", e, result)
	}
	// Reject a network-path reference that could target another authority.
	if e = c.Request(context.Background(), "GET", "//attacker.invalid", nil, &result); e == nil {
		t.Fatal("authority path accepted")
	}
}

// TestForgedControllerTLSRejectsCredentials simulates a network route to a different TLS server.
func TestForgedControllerTLSRejectsCredentials(t *testing.T) {
	legitimate := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer legitimate.Close()
	var received atomic.Bool
	identity, err := fleet.Generate()
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair([]byte(identity.Certificate), []byte(identity.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	forged := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			received.Store(true)
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	forged.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
	forged.StartTLS()
	defer forged.Close()
	ca := filepath.Join(t.TempDir(), "controller.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: legitimate.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{URL: legitimate.URL, CAFile: ca, Token: "credential"})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the URL and trusted CA fixed while an attacker redirects only the network connection.
	transport := c.HTTP.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", forged.Listener.Addr().String())
	}
	var result map[string]bool
	if err := c.Request(context.Background(), "GET", "/health", nil, &result); err == nil {
		t.Fatal("forged TLS controller accepted")
	}
	if received.Load() {
		t.Fatal("credential reached forged controller")
	}
}

// TestUnixTransport verifies local administration uses its explicit Unix socket.
func TestUnixTransport(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "api.sock")
	listener, e := net.Listen("unix", socket)
	// Require the test administration socket to bind successfully.
	if e != nil {
		t.Fatal(e)
	}
	// Return a simple response from the local administration fixture.
	server := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(map[string]bool{"ok": true}) })}
	defer server.Close()
	go server.Serve(listener)
	c, e := New(Options{SocketPath: socket})
	// Require explicit Unix client construction.
	if e != nil {
		t.Fatal(e)
	}
	var result map[string]bool
	// Verify the request traverses the socket and returns its expected payload.
	if e = c.Request(context.Background(), "GET", "/health", nil, &result); e != nil || !result["ok"] {
		t.Fatalf("unix request: %v", e)
	}
}

// TestProfilesKeepCredentialsSeparateAndPrivate checks owner-only credential files and complete local disconnection.
func TestProfilesKeepCredentialsSeparateAndPrivate(t *testing.T) {
	store := NewStore(t.TempDir())
	// Require initial enrollment persistence before inspecting files.
	if e := store.Save("prod", "https://controller.example.com", "client", "secret-token"); e != nil {
		t.Fatal(e)
	}
	metadata, _ := os.ReadFile(filepath.Join(store.Home, "profiles.json"))
	// Ensure public metadata never includes the bearer token.
	if strings.Contains(string(metadata), "secret-token") {
		t.Fatal("credential leaked into metadata")
	}
	info, _ := os.Stat(filepath.Join(store.Home, "credentials.json"))
	// Ensure credential bytes are accessible only to the owner.
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential mode", info.Mode())
	}
	profile, e := store.Get("")
	// Resolve the selected profile together with its separate credential.
	if e != nil || profile.Token != "secret-token" {
		t.Fatalf("profile retrieval: %v", e)
	}
	// Require local disconnection to remove the identity.
	if e = store.Remove(""); e != nil {
		t.Fatal(e)
	}
	// Verify the removed profile cannot still provide credentials.
	if _, e = store.Get(""); e == nil {
		t.Fatal("disconnected profile remains")
	}
}
