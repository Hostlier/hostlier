package resourcepackage

import (
	"bytes"
	"encoding/json"
	"hostlier/internal/model"
	"hostlier/pkg/resourceprotocol"
	"strings"
	"testing"
)

// TestPlanDoesNotReachDocker verifies engine planning requires no runtime connection.
func TestPlanDoesNotReachDocker(t *testing.T) {
	r := resourceprotocol.Request{Version: 1, Operation: "plan", Backend: resourceprotocol.Backend{Type: "docker", StateDir: t.TempDir(), SocketPath: "/nonexistent/docker.sock"}, Spec: model.ResourceSpec{Name: "db", Type: "postgres", Storage: "1Gi", PackageDigest: "broker-metadata"}}
	v := roundtrip(t, "postgres", r)
	// Planning normalizes the engine image without attempting Docker provisioning.
	if v.Error != nil || v.Plan.Spec.Image == "" || v.Plan.Spec.PackageDigest != "" {
		t.Fatal(v)
	}
}

// TestFixedEngineRejectsOtherEngine checks package identity before adapter creation.
func TestFixedEngineRejectsOtherEngine(t *testing.T) {
	r := resourceprotocol.Request{Version: 1, Operation: "plan", Backend: resourceprotocol.Backend{StateDir: t.TempDir()}, Spec: model.ResourceSpec{Type: "redis"}}
	v := roundtrip(t, "postgres", r)
	// A fixed PostgreSQL executable cannot authorize Redis engine operations.
	if v.Error == nil || v.Error.Code != "RESOURCE_ENGINE" {
		t.Fatal(v)
	}
}

// TestDecodeRejectsUnknownAndTrailingFields validates the private request boundary.
func TestDecodeRejectsUnknownAndTrailingFields(t *testing.T) {
	_, err := decode(strings.NewReader(`{"version":1,"unexpected":"secret"}`))
	// Unknown fields cannot silently alter protocol authority.
	if err == nil {
		t.Fatal("unknown request field accepted")
	}
	_, err = decode(strings.NewReader(`{"version":1} {"version":1}`))
	// Requests must contain exactly one JSON document.
	if err == nil {
		t.Fatal("trailing request accepted")
	}
}

// roundtrip exercises the same bounded JSON entry point used by executables.
func roundtrip(t *testing.T, engine string, r resourceprotocol.Request) resourceprotocol.Response {
	t.Helper()
	data, err := json.Marshal(r)
	// Fixture requests must be valid JSON before exercising the server boundary.
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	// Server operation failures are returned as safe structured responses.
	if err = Serve(engine, bytes.NewReader(data), &output); err != nil {
		t.Fatal(err)
	}
	var v resourceprotocol.Response
	// Responses must remain valid protocol JSON.
	if err = json.Unmarshal(output.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
