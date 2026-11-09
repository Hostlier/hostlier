package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// TestCaddySegmentRouting checks prefix boundaries, redirects and optional path stripping.
func TestCaddySegmentRouting(t *testing.T) {
	route, e := BuildCaddyRoute(model.Project{Name: "app", Route: "example.com/app", Proxy: &model.Proxy{StripPrefix: true}}, "127.0.0.1:8000")
	// Require route rendering for a valid prefix-stripping application.
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(route)
	// Check every segment-aware proxy and redirect property in the generated route.
	for _, part := range []string{`"path":["/app","/app/*"]`, `"Location":["/app/"]`, `"strip_path_prefix":"/app"`, `"dial":"127.0.0.1:8000"`} {
		// A missing matcher or rewrite would change public routing behavior.
		if !strings.Contains(string(raw), part) {
			t.Fatalf("missing %s: %s", part, raw)
		}
	}
}

// TestMemoryAndPrivateAdmin covers byte conversion, overflow and private administration boundaries.
func TestMemoryAndPrivateAdmin(t *testing.T) {
	n, e := ParseMemory("512Mi")
	// Require the exact binary-unit byte count.
	if e != nil || n != 536870912 {
		t.Fatalf("%d %v", n, e)
	}
	// Reject a quantity that would overflow runtime memory enforcement.
	if _, e = ParseMemory("999999999999999999999999Ti"); e == nil {
		t.Fatal("overflowing memory accepted")
	}
	// Reject malformed memory quantities before numeric conversion.
	if _, e = ParseMemory("invalid"); e == nil {
		t.Fatal("invalid memory accepted")
	}
	// Reject unsupported backend selection explicitly.
	if _, e = New(Options{Type: "unsupported"}); e == nil {
		t.Fatal("unsupported runtime accepted")
	}
	// Refuse Caddy administration endpoints outside local loopback HTTP.
	if _, e = New(Options{CaddyAdmin: "http://0.0.0.0:2019"}); e == nil {
		t.Fatal("public admin accepted")
	}
}

// TestInspectUsesNoDockerMutation ensures planning reads immutable local metadata without daemon changes.
func TestInspectUsesNoDockerMutation(t *testing.T) {
	dir, e := os.MkdirTemp("/tmp", "hrt-")
	// Require an isolated temporary Unix-socket directory.
	if e != nil {
		t.Fatal(e)
	}
	// Remove the temporary socket directory after the fixture server exits.
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	listener, e := net.Listen("unix", socket)
	// Require the fixture Docker socket to bind successfully.
	if e != nil {
		t.Fatal(e)
	}
	mutations := 0
	// Emulate Docker inspection and record any attempted mutation.
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Count every request outside the allowed read-only method.
		if r.Method != "GET" {
			mutations++
			t.Error("planning mutated runtime")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Os": "linux", "Architecture": goruntime.GOARCH, "RepoDigests": []string{"example.com/app@sha256:" + strings.Repeat("a", 64)}})
	})}
	go server.Serve(listener)
	defer server.Close()
	adapter, e := New(Options{StateDir: dir, SocketPath: socket})
	// Require runtime construction against the isolated Docker fixture.
	if e != nil {
		t.Fatal(e)
	}
	info, e := adapter.InspectImage(context.Background(), "example.com/app@sha256:"+strings.Repeat("a", 64))
	// Require the local immutable digest to resolve successfully.
	if e != nil || !strings.HasSuffix(info.Digest, strings.Repeat("a", 64)) {
		t.Fatalf("%+v %v", info, e)
	}
	// Image inspection must never create or pull Docker images.
	if mutations != 0 {
		t.Fatal("unexpected mutation")
	}
	// Read-only planning must not create durable runtime metadata.
	if _, e = os.Stat(filepath.Join(dir, "runtime-docker.json")); !os.IsNotExist(e) {
		t.Fatal("planning wrote runtime state")
	}
}

// TestUnownedContainersCannotBeStopped verifies ownership is checked before Docker mutations.
func TestUnownedContainersCannotBeStopped(t *testing.T) {
	dir, e := os.MkdirTemp("/tmp", "hrt-")
	// Require an isolated socket directory for the ownership fixture.
	if e != nil {
		t.Fatal(e)
	}
	// Remove the temporary ownership-test socket directory.
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	listener, e := net.Listen("unix", socket)
	// Require the fixture Docker listener before invoking lifecycle operations.
	if e != nil {
		t.Fatal(e)
	}
	mutations := 0
	// Return an external container and count any attempted daemon mutations.
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Track every non-read request as a forbidden mutation.
		if r.Method != "GET" {
			mutations++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "external", "Config": map[string]any{"Labels": map[string]string{"io.hostlier.managed": "true", "io.hostlier.app": "app", "io.hostlier.owner": "other-owner"}}})
	})}
	go server.Serve(listener)
	defer server.Close()
	adapter, _ := New(Options{StateDir: dir, SocketPath: socket})
	rt := adapter.(*Runtime)
	// Seed only controller metadata; the inspected external labels remain authoritative.
	if e = rt.save(state{Apps: map[string]appRecord{"app": {Deployment: model.Deployment{Name: "app", RuntimeID: "external"}}}}); e != nil {
		t.Fatal(e)
	}
	e = adapter.Action(context.Background(), "app", "stop", model.ActionContext{})
	// Require an ownership error with no Docker stop or delete request.
	if !hasCode(e, "OWNERSHIP") || mutations != 0 {
		t.Fatalf("ownership failed: %v mutations%d", e, mutations)
	}
}
