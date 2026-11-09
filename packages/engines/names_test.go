package engines

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/model"
	"hostlier/internal/resources"
	"hostlier/pkg/workflow"
)

// TestLocalNamesFitRuntimeDNSLabels verifies actual Docker and k3s application connection hostnames.
func TestLocalNamesFitRuntimeDNSLabels(t *testing.T) {
	req := workflow.Request{Key: strings.Repeat("long-app-dependency/", 128)}
	name := localName(req)
	// Stable names reserve the manager prefix budget and never depend on caller key length.
	if name != localName(req) || len(name) != 27 {
		t.Fatal("unstable or oversized resource identity")
	}
	for _, runtime := range []string{"docker", "k3s"} { // Both runtimes expose the same owner-scoped resource identity to applications.
		checkRuntimeHostname(t, runtime, name)
	}
}

// checkRuntimeHostname obtains the production manager connection without starting an engine.
func checkRuntimeHostname(t *testing.T, runtime, name string) {
	t.Helper()
	directory := t.TempDir()
	sum := sha256.Sum256([]byte(directory))
	network := "hostlier-" + hex.EncodeToString(sum[:])[:16] + "-resource-" + name
	record := map[string]model.Resource{name: {Name: name, Type: "postgres", Network: network}}
	data, err := json.Marshal(record)
	// The fixture represents persisted native metadata after provisioning.
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "resources-"+runtime+".json"), data, 0600); err != nil { // Native metadata must be readable before deriving a connection.
		t.Fatal(err)
	}
	manager, err := resources.New(resources.Options{Type: runtime, StateDir: directory})
	// Construction and Connection remain read-only and require no Docker socket or Kubernetes cluster.
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := manager.Connection(context.Background(), name, model.ResourceCredentials{Username: "hostlier", Password: strings.Repeat("a", 64), Database: "app"})
	// Validate the actual host emitted by each native manager rather than a duplicated URL template.
	if err != nil {
		t.Fatal(err)
	}
	address, err := url.Parse(outputs["url"])
	// A malformed connection URL cannot establish a valid DNS budget.
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range strings.Split(address.Hostname(), ".") { // Kubernetes service FQDNs contain several independently bounded labels.
		if len(label) > 63 || len(label) == 0 {
			t.Fatalf("%s returned invalid DNS label %q (%d bytes)", runtime, label, len(label))
		} // Enforce the DNS label limit used by application resolvers.
	}
}
