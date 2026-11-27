//go:build linux || darwin

package controllercmd

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"hostlier/internal/client"
)

func TestMemoryDaemonStartsWithoutContainerEngine(t *testing.T) {
	// Keep the Unix-domain socket path below macOS's 104-byte limit.
	tmp, pathErr := os.MkdirTemp("", "hm-")
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	base, pathErr := filepath.EvalSymlinks(tmp)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	source := filepath.Join(base, "existing")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(base, "config.yaml")
	if err := os.WriteFile(cfg, []byte("version: 1\ndomains: [example.com]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	socket := filepath.Join(base, "controller.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{"run", "--mode", "memory", "--memory-root", source, "--state-dir", filepath.Join(base, "state"), "--config", cfg, "--hostname", "storage.example.com", "--port", strconv.Itoa(port), "--socket", socket}, bytes.NewReader(nil), &out, &out)
	}()
	api, err := client.NewGRPC(client.Options{SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	ready := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("daemon exited before readiness: %v %s", err, out.String())
		default:
		}
		var result map[string]any
		requestCtx, requestCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err = api.Request(requestCtx, "GET", "/v1/memory", nil, &result)
		requestCancel()
		if err == nil {
			if result["mode"] != "memory" {
				t.Fatal(result)
			}
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("daemon failed to stop")
	}
	if !ready {
		t.Fatalf("memory daemon never became ready: %s", out.String())
	}
}
