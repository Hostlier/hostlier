package controllercmd

import (
	"bytes"
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestStandaloneBootstrapUsesOnlyControllerSocket exercises the packaged command without a server CLI.
func TestStandaloneBootstrapUsesOnlyControllerSocket(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "hl-owner-")
	// macOS Unix socket paths have a small platform limit independent of application behavior.
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "admin.sock")
	listener, err := net.Listen("unix", socket)
	// A real Unix listener verifies socket addressing and JSON transport together.
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan model.OwnerSetupRequest, 1)
	// The fixture exposes only the running controller's protected bootstrap endpoint.
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No general remote API is required by this standalone subcommand.
		if r.Method != "POST" || r.URL.Path != "/v1/access/setup" {
			w.WriteHeader(404)
			return
		}
		var input model.OwnerSetupRequest
		_ = json.NewDecoder(r.Body).Decode(&input)
		seen <- input
		_ = json.NewEncoder(w).Encode(model.OwnerSetupResult{Status: "ready"})
	})}
	// Run only the isolated local protocol fixture.
	go server.Serve(listener)
	defer server.Close()
	var output bytes.Buffer
	err = Run(context.Background(), []string{"bootstrap", "--url", "https://controller.example.com", "--request", "pending", "--socket", socket}, nil, &output, io.Discard)
	// The result remains machine readable for the laptop's SSH process.
	if err != nil || output.String() != "{\"status\":\"ready\"}\n" {
		t.Fatal(output.String(), err)
	}
	input := <-seen
	// The standalone binary must bind both controller origin and initiating device identity.
	if input.URL != "https://controller.example.com" || input.RequestID != "pending" || input.Name != "Owner" {
		t.Fatal(input)
	}
}

// TestBootstrapRejectsIncompleteIntent checks command validation before reaching protected state.
func TestBootstrapRejectsIncompleteIntent(t *testing.T) {
	cases := [][]string{{"bootstrap"}, {"bootstrap", "--url", "http://controller.example.com", "--request", "pending"}, {"bootstrap", "--url", "https://controller.example.com", "--request", "pending", "--state-dir", "/unexpected"}}
	// Invalid bootstrap commands must not fall through to daemon startup or state recovery.
	for _, args := range cases {
		// Every rejected invocation needs an explicit error, not a partially created link.
		if err := Run(context.Background(), args, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("invalid bootstrap accepted", args)
		}
	}
}
