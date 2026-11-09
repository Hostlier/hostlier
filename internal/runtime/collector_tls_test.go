package runtime

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// collectorTLSFixture creates a matching disposable certificate and private key without exposing them in output.
func collectorTLSFixture(t *testing.T) *model.CollectorTLS {
	t.Helper()
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	cert := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	// A fixture key serialization failure must not weaken the tested validation path.
	if err != nil {
		t.Fatal(err)
	}
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}))
	return &model.CollectorTLS{CA: certificate, Certificate: certificate, PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}
}

// TestManagedCollectorTLS validates dedicated credentials, protected Docker files and rotation identity.
func TestManagedCollectorTLS(t *testing.T) {
	spec := managedCollectorSpec(t, "docker")
	spec.TLS = collectorTLSFixture(t)
	p, err := prepareCollector(spec)
	// The reviewed exporter must use all three dedicated identity files.
	if err != nil || !strings.Contains(p.config, "key_file: /etc/otelcol/tls/client.key") {
		t.Fatalf("TLS configuration failed: %v", err)
	}
	runtime := &Runtime{owner: "fixture-owner", options: Options{StateDir: t.TempDir(), CollectorTrustDirectory: collectorTrustTestRoot(t)}}
	// Automatic trust and mounted identity files must both be provisioned before the image pull.
	if err = runtime.installCollectorTrust(p); err != nil {
		t.Fatal(err)
	}
	dir, err := runtime.collectorFiles(p)
	// The collector receives its own private immutable revision.
	if err != nil {
		t.Fatal(err)
	}
	assertCollectorPrivateFile(t, filepath.Join(dir, "client.key"), spec.TLS.PrivateKey)
	assertCollectorPrivateFile(t, filepath.Join(runtime.options.CollectorTrustDirectory, p.registry, "client.key"), spec.TLS.PrivateKey)
	spec.TLS = &model.CollectorTLS{CA: spec.TLS.CA, Certificate: spec.TLS.Certificate, PrivateKey: spec.TLS.PrivateKey + "\n"}
	rotated, err := prepareCollector(spec)
	// Any credential material change must invalidate the mounted revision.
	if err != nil || rotated.revision == p.revision {
		t.Fatalf("TLS rotation was not reflected: %v", err)
	}
	spec.TLS.PrivateKey = "invalid"
	// Invalid client keys must fail before any runtime mutation.
	if _, err = prepareCollector(spec); err == nil {
		t.Fatal("malformed client key accepted")
	}
}

// assertCollectorPrivateFile checks actual bytes and permissions without logging private content.
func assertCollectorPrivateFile(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	// Persisted identities must match the exact controller-provided bytes.
	if err != nil || string(content) != want {
		t.Fatal("private identity contents differ")
	}
	info, err := os.Stat(path)
	// Group and world access to client credentials is never permitted.
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private identity permissions differ")
	}
}

// TestManagedCollectorTrustRefusesForeign checks ordinary and redirected foreign origins before any overwrite.
func TestManagedCollectorTrustRefusesForeign(t *testing.T) {
	for _, kind := range []string{"foreign", "symlink", "marker"} { // Every collision must preserve preexisting trust bytes.
		t.Run(kind, func(t *testing.T) {
			root := collectorTrustTestRoot(t)
			origin := filepath.Join(root, "controller.example.com")
			target := t.TempDir()
			// The origin itself must not redirect to a different trust directory.
			if kind == "symlink" {
				// A redirected origin is rejected even when its target is accessible.
				if err := os.Symlink(target, origin); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(origin, 0700); err != nil { // Ordinary collisions use a real existing directory.
				t.Fatal(err)
			}
			existing := filepath.Join(origin, "ca.crt")
			// Preserve a foreign trust entry even if its origin path is otherwise writable.
			if err := os.WriteFile(existing, []byte("foreign trust"), 0600); err != nil {
				t.Fatal(err)
			}
			// A different valid ownership marker is still a collision.
			if kind == "marker" {
				// The marker belongs to another controller and must remain untouched.
				if err := os.WriteFile(filepath.Join(origin, collectorTrustMarker), []byte("other-owner\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// None of the foreign cases authorizes replacing the existing trust entry.
			if writeCollectorTrust(root, "controller.example.com", "fixture-owner", map[string][]byte{"ca.crt": []byte("new trust")}) == nil {
				t.Fatal("foreign trust accepted")
			}
			assertCollectorPrivateFile(t, existing, "foreign trust")
		})
	}
}

// TestManagedCollectorK3sTLS checks Secret-only identity delivery and the public scoped bootstrap contract.
func TestManagedCollectorK3sTLS(t *testing.T) {
	runtime, fixture := collectorKubeRuntime(t)
	spec := managedCollectorSpec(t, "k3s")
	spec.TLS = collectorTLSFixture(t)
	// Automatic reconciliation must include the bootstrap without adding duplicate controllers.
	if err := runtime.EnsureCollector(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	daemon := fixture.objects["DaemonSet/"+collectorNamespace+"/"+collectorName]
	raw, _ := json.Marshal(daemon)
	// Public workload specs reference the Secret and pinned helper, never private PEM bytes.
	if !strings.Contains(string(raw), collectorTrustHelper) || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatal("unsafe bootstrap identity projection")
	}
	before := fixture.writes
	// Repeated fleet reconciliation must retain the running DaemonSet revision.
	if err := runtime.EnsureCollector(context.Background(), spec); err != nil || fixture.writes != before {
		t.Fatal("unchanged TLS deployment rolled out again")
	}
	config, _ := json.Marshal(fixture.objects["ConfigMap/"+collectorNamespace+"/"+collectorName])
	// Public configuration contains file references only, while the Secret carries the actual private identity.
	if strings.Contains(string(config), "PRIVATE KEY") || objectMap(fixture.objects["Secret/"+collectorNamespace+"/hostlier-telemetry"], "data")["client.key"] == nil {
		t.Fatal("TLS identity was not confined to the Secret")
	}
}

// collectorTrustTestRoot resolves platform temporary aliases before exercising strict ancestor checks.
func collectorTrustTestRoot(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	// Test roots must be real paths because production trust intentionally rejects symlinked ancestors.
	if err != nil {
		t.Fatal(err)
	}
	return path
}
