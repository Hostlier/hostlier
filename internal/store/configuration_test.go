package store

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRestoredCertbotConfigurationReexportsFreshSystemAssets exercises recovery at the original operational path.
func TestRestoredCertbotConfigurationReexportsFreshSystemAssets(t *testing.T) {
	source, settings, caddy := t.TempDir(), canonicalConfigurationRoot(t), t.TempDir()
	state := map[string]any{}
	writer, err := Open(source, &state)
	// Create a real authenticated SQLite snapshot before exporting any fixtures.
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	certificate, key := recoveryCertificate(t)
	writeRecoveryFixture(t, settings, "nginx-certificates/archive/site/fullchain1.pem", certificate)
	writeRecoveryFixture(t, settings, "nginx-certificates/archive/site/privkey1.pem", key)
	live := filepath.Join(settings, "nginx-certificates/live/site")
	// Certbot's relative archive links must survive restoration without poisoning later backup walks.
	if err = os.MkdirAll(live, 0700); err != nil {
		t.Fatal(err)
	}
	// Require protected ordinary files before staging private Caddy state.
	for _, name := range []string{"fullchain", "privkey"} {
		// Both live certificate entries resolve within the protected settings root.
		if err = os.Symlink("../../archive/site/"+name+"1.pem", filepath.Join(live, name+".pem")); err != nil {
			t.Fatal(err)
		}
	}
	writeRecoveryFixture(t, settings, "nginx/nginx.pid", []byte("stale pid"))
	writeRecoveryFixture(t, caddy, ".local/share/caddy/acme/account.json", []byte("private Caddy ACME account"))
	writeRecoveryFixture(t, caddy, ".local/share/caddy/certificates/site/site.crt", certificate)
	writeRecoveryFixture(t, caddy, ".local/share/caddy/certificates/site/site.key", key)
	bundle := filepath.Join(t.TempDir(), "first.tar")
	// Capture both edge layouts in the same checksummed owner-only archive.
	if err = exportWithConfiguration(source, bundle, "", settings, caddy); err != nil {
		t.Fatal(err)
	}
	assertRecoveryRoundtrip(t, source, bundle, settings, caddy)
}

// assertRecoveryRoundtrip activates restored SQLite and verifies fresh system assets replace staging.
func assertRecoveryRoundtrip(t *testing.T, source, bundle, settings, caddy string) {
	t.Helper()
	moved := source + ".source-stopped"
	// The source is fenced before restoring at its exact original path.
	if err := os.Rename(source, moved); err != nil {
		t.Fatal(err)
	}
	// Inspect each state entry without following unsupported links.
	t.Cleanup(func() { os.RemoveAll(moved) })
	// Abort rather than accepting incomplete or unsafe recovery data.
	if err := Restore(bundle, source); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "recovery-config/etc/hostlier/nginx-certificates/live/site/fullchain.pem")
	// Verify that this regression really traverses a restored Certbot symlink.
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal(info, err)
	}
	assertCaddyRecoveryFile(t, source)
	writeRecoveryFixture(t, source, "recovery-config/stale-only", []byte("stale restored settings"))
	state := map[string]any{}
	active, err := Open(source, &state)
	// Opening and closing models a stopped recovered controller that has activated its state.
	if err != nil {
		t.Fatal(err)
	}
	active.Close()
	writeRecoveryFixture(t, settings, "new-settings.yaml", []byte("fresh system settings"))
	output := filepath.Join(t.TempDir(), "second.tar")
	// The restored staging tree must be skipped before its symlinks reach regular-file checks.
	if err = exportWithConfiguration(source, output, "", settings, caddy); err != nil {
		t.Fatal(err)
	}
	files, err := readBundle(output)
	assertFreshRecoveryFiles(t, files, err)
}

// assertCaddyRecoveryFile verifies ACME account bytes and owner-only staged permissions.
func assertCaddyRecoveryFile(t *testing.T, source string) {
	t.Helper()
	path := filepath.Join(source, "recovery-config/var/lib/caddy/.local/share/caddy/acme/account.json")
	data, err := os.ReadFile(path)
	// Caddy's private account state lives outside /etc/hostlier and must round-trip separately.
	if err != nil || string(data) != "private Caddy ACME account" {
		t.Fatal(string(data), err)
	}
	pairRoot := filepath.Join(source, "recovery-config/var/lib/caddy/.local/share/caddy/certificates/site/site")
	// Caddy certificate and key material must round-trip as a usable matching TLS pair.
	if _, err := tls.LoadX509KeyPair(pairRoot+".crt", pairRoot+".key"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	// Restored credentials are inaccessible to other users until explicitly reinstalled for Caddy.
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}

// TestCaddyStateRejectsLinks refuses both redirected state roots and redirected files.
func TestCaddyStateRejectsLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeRecoveryFixture(t, outside, "private", []byte("unrelated system file"))
	// The private-state collector never follows links into another tree.
	if err := os.Symlink(filepath.Join(outside, "private"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	// Abort rather than accepting incomplete or unsafe recovery data.
	if err := collectCaddyState(map[string][]byte{}, root); err == nil {
		t.Fatal("Caddy file link accepted")
	}
	linkedRoot := filepath.Join(t.TempDir(), "redirected")
	// Root-level redirects must be rejected before starting a directory walk.
	if err := os.Symlink(outside, linkedRoot); err != nil {
		t.Fatal(err)
	}
	// Abort rather than accepting incomplete or unsafe recovery data.
	if err := collectCaddyState(map[string][]byte{}, linkedRoot); err == nil {
		t.Fatal("Caddy root link accepted")
	}
}

// writeRecoveryFixture creates one protected local fixture under a test-owned root.
func writeRecoveryFixture(t *testing.T, root, name string, data []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	// Fixture layout mirrors the installed root without touching system directories.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	// Abort rather than accepting incomplete or unsafe recovery data.
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// recoveryCertificate provides a real matching certificate/key pair for archive validation.
func recoveryCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// Generate isolated test key material instead of embedding production credentials.
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "site"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	// A complete TLS pair allows the same production export validator to run.
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	// Encode the private key with its matching certificate for Certbot's staged file layout.
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
}

// canonicalConfigurationRoot avoids macOS temporary-directory aliases in confined-link fixtures.
func canonicalConfigurationRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	// This fixture must use the same canonical path returned by certificate link resolution.
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// assertFreshRecoveryFiles checks fresh settings inclusion and stale runtime/staging exclusion.
func assertFreshRecoveryFiles(t *testing.T, files map[string][]byte, err error) {
	t.Helper()
	// Fresh installed settings are collected again rather than carrying old staged copies forward.
	if err != nil {
		t.Fatal(err)
	}
	// Require protected ordinary files before staging private Caddy state.
	if string(files["recovery-config/etc/hostlier/new-settings.yaml"]) != "fresh system settings" {
		t.Fatal("fresh settings absent")
	}
	// Stale staged copies never become source configuration in later backups.
	if _, exists := files["recovery-config/stale-only"]; exists {
		t.Fatal("stale staging exported")
	}
	// Exclude runtime artifacts and stale staged files from the next archive.
	if _, exists := files["recovery-config/etc/hostlier/nginx/nginx.pid"]; exists {
		t.Fatal("ephemeral Nginx pid exported")
	}
}
