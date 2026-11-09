package resourceclient

import (
	"bytes"
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
	"hostlier/pkg/resourceprotocol"
	"io"
	"os/exec"
	"path/filepath"
	"time"
)

// call serializes package calls and retains only bounded private response bytes.
func (m *Manager) call(ctx context.Context, engine, digest string, r resourceprotocol.Request) (resourceprotocol.Response, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.Version = 1
	r.Backend = m.options
	return m.invoke(ctx, engine, digest, r)
}

// invoke runs a verified immutable snapshot with a fixed private resource argument.
func (m *Manager) invoke(ctx context.Context, engine, expected string, r resourceprotocol.Request) (resourceprotocol.Response, string, error) {
	// Built-in resource operations may select only fixed engine executables.
	if engine != "postgres" && engine != "redis" && engine != "neo4j" {
		return resourceprotocol.Response{}, "", model.Err("RESOURCE_SPEC", "Unsupported resource engine")
	}
	runner := packageexec.Runner{Directory: filepath.Join(m.options.StateDir, "packages")}
	path, digest, cleanup, err := runner.Snapshot(engine, expected)
	// Missing packages are reported lazily with an actionable installation instruction.
	if err != nil {
		return resourceprotocol.Response{}, "", model.Err("RESOURCE_PACKAGE_PREFLIGHT", "Install or replan the "+engine+" resource package using hostlier package install")
	}
	defer cleanup()
	v, err := run(ctx, path, r)
	return v, digest, err
}

// run bounds private input before spawning the approved package snapshot.
func run(ctx context.Context, path string, r resourceprotocol.Request) (resourceprotocol.Response, error) {
	data, err := json.Marshal(r)
	// Native backup and JSON framing each have explicit protocol limits.
	if err != nil || len(r.Data) > resourceprotocol.MaxBackupBytes || len(data) > resourceprotocol.MaxWireBytes {
		return resourceprotocol.Response{}, model.Err("BACKUP_LIMIT", "Resource request exceeds protocol limit")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	return command(ctx, path, data)
}

// command isolates process arguments, environment, diagnostics and output size.
func command(ctx context.Context, path string, data []byte) (resourceprotocol.Response, error) {
	cmd := exec.CommandContext(ctx, path, "resource")
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C"}
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	output := &boundedOutput{}
	cmd.Stdout = output
	// Process diagnostics cannot become controller error messages.
	if cmd.Run() != nil || output.overflow {
		return resourceprotocol.Response{}, model.Err("RESOURCE_PACKAGE", "Resource package process failed")
	}
	return response(output.Bytes())
}

// response requires exactly one versioned bounded response.
func response(data []byte) (v resourceprotocol.Response, err error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	// Malformed packages cannot inject arbitrary response streams.
	if decoder.Decode(&v) != nil || decoder.Decode(new(any)) != io.EOF || v.Version != 1 || len(v.Data) > resourceprotocol.MaxBackupBytes {
		return v, model.Err("RESOURCE_PACKAGE", "Invalid resource package response")
	}
	// Preserve safe machine-readable engine failure codes.
	if v.Error != nil {
		return v, v.Error
	}
	return v, nil
}

// boundedOutput discards overflow without blocking a child process.
type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

// Write retains at most the wire bound and reports all bytes consumed.
func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := resourceprotocol.MaxWireBytes - b.Len()
	// Discard bytes that cannot fit into the bounded private response.
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.Buffer.Write(p)
	return n, nil
}
