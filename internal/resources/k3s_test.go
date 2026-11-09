package resources

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// TestK3sResourceManifests checks isolation and credential placement for all supported Kubernetes engines.
func TestK3sResourceManifests(t *testing.T) {
	adapter, e := New(Options{Type: "k3s", StateDir: t.TempDir()})
	// Require the selected resource backend to initialize successfully.
	if e != nil {
		t.Fatal(e)
	}
	m := adapter.(*K3sManager)
	// Apply the same contract to each supported engine or owned object kind.
	for _, kind := range []string{"postgres", "redis", "neo4j"} {
		checkK3sResourceManifest(t, m, kind)
	}
}

// TestK3sProtocolPasswordOnlyStdin checks that protocol credentials never enter command arguments.
func TestK3sProtocolPasswordOnlyStdin(t *testing.T) {
	password := "test-sensitive-password"
	calls := 0
	// Stub kubectl execution to assert protected stdin credentials and explicit cluster selection.
	adapter, e := New(Options{Type: "k3s", StateDir: t.TempDir(), KubeRun: func(_ context.Context, args []string, input []byte) ([]byte, error) {
		calls++
		// Fail the fixture on password in process arguments.
		if strings.Contains(strings.Join(args, " "), password) {
			t.Fatal("password in process arguments")
		}
		// Fail the fixture on missing protected stdin credential.
		if string(input) != password+"\n" {
			t.Fatal("missing protected stdin credential")
		}
		// Fail the fixture on ambient cluster selection.
		if !strings.Contains(strings.Join(args, " "), "--kubeconfig=") {
			t.Fatal("ambient cluster selection")
		}
		// Return the authenticated Redis health response from this protocol stub.
		if strings.Contains(strings.Join(args, " "), "redis-cli") {
			return []byte("PONG"), nil
		}
		return []byte("1"), nil
	}})
	// Require the selected resource backend to initialize successfully.
	if e != nil {
		t.Fatal(e)
	}
	m := adapter.(*K3sManager)
	// Apply the same contract to each supported engine or owned object kind.
	for _, kind := range []string{"postgres", "redis", "neo4j"} {
		_, e = m.protocol(context.Background(), record{Resource: model.Resource{Type: kind, Network: m.namespace("db")}}, model.ResourceCredentials{Password: password})
		// Require the native authenticated protocol check to succeed.
		if e != nil {
			t.Fatal(e)
		}
	}
	// Require one authenticated protocol call for every supported engine.
	if calls != 3 {
		t.Fatal(calls)
	}
}

// TestK3sOutputBound checks that excessive kubectl output is truncated and terminates its command.
func TestK3sOutputBound(t *testing.T) {
	killed := false
	// Record the output-limit callback without launching a real kubectl process.
	writer := &kubeOutputLimit{limit: 8, kill: func() { killed = true }}
	writer.Write([]byte("123456"))
	writer.Write([]byte("789012"))
	writer.Write([]byte("more"))
	// Fail the fixture on unbounded stdout.
	if !killed || !writer.exceeded || writer.buffer.Len() != 8 {
		t.Fatal("unbounded stdout", writer.buffer.Len())
	}
}

// checkK3sResourceManifest checks isolation and credential placement for one engine.
func checkK3sResourceManifest(t *testing.T, m *K3sManager, kind string) {
	t.Helper()
	c := model.ResourceCredentials{Username: "hostlier", Password: "unique-private-secret", Database: "app"}
	// Use the Neo4j image authentication username for this fixture.
	if kind == "neo4j" {
		c.Username = "neo4j"
	}
	spec, e := normalize(model.ResourceSpec{Name: "db", Type: kind, Storage: "1Gi"})
	// Reject invalid resource specifications before creating engine objects.
	if e != nil {
		t.Fatal(e)
	}
	manifests := m.manifests(spec, c)
	// Fail the fixture on isolation applied after workload.
	if manifests[1]["kind"] != "NetworkPolicy" {
		t.Fatal("isolation applied after workload")
	}
	raw, _ := json.Marshal(manifests)
	// Check each required isolation and storage field in the serialized manifest.
	for _, part := range []string{`"type":"ClusterIP"`, `"kind":"PersistentVolumeClaim"`, `"automountServiceAccountToken":false`, `"type":"Recreate"`, `"kind":"Secret"`} {
		// Report each missing isolation or storage field without skipping remaining assertions.
		if !strings.Contains(string(raw), part) {
			t.Errorf("%s missing %s", kind, part)
		}
	}
	// Fail the fixture on public database endpoint.
	if strings.Contains(string(raw), `"hostPort"`) || strings.Contains(string(raw), `"NodePort"`) {
		t.Fatal("public database endpoint")
	}
	checkK3sSecretPlacement(t, manifests, c.Password)
	// Fail the fixture on PostgreSQL trust authentication not disabled.
	if kind == "postgres" && !strings.Contains(string(raw), "--auth-host=scram-sha-256 --auth-local=scram-sha-256") {
		t.Fatal("PostgreSQL trust authentication not disabled")
	}
}

// checkK3sSecretPlacement checks that only Secret payloads contain credentials.
func checkK3sSecretPlacement(t *testing.T, manifests []map[string]any, password string) {
	t.Helper()
	// Check every non-Secret manifest for accidental copies of the fixture password.
	for _, manifest := range manifests {
		// Inspect non-Secret objects; the credentials Secret is the permitted password location.
		if manifest["kind"] != "Secret" {
			data, _ := json.Marshal(manifest)
			// Fail the fixture on secret outside Kubernetes Secret payload.
			if strings.Contains(string(data), password) {
				t.Fatal("secret outside Kubernetes Secret payload")
			}
		}
	}
}
