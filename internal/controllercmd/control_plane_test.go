package controllercmd

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/controller"
	"hostlier/internal/model"
)

func TestControlPlaneStartsWithoutLocalRuntime(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "config.yaml"), []byte("version: 1\ndomains: [example.com]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "hl-cp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "admin.sock")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		args := []string{"run", "--mode", "control-plane", "--state-dir", state, "--socket", socket, "--port", port, "--fleet-address", "127.0.0.1:0"}
		done <- Run(ctx, args, nil, io.Discard, io.Discard)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(25 * time.Second):
			t.Error("control-plane daemon did not stop")
		}
	})
	api, err := client.NewGRPC(client.Options{SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	defer api.HTTP.CloseIdleConnections()
	var status struct{ Runtime, Hostname string }
	for {
		requestCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		err = api.Request(requestCtx, "GET", "/v1/status", nil, &status)
		stop()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("control-plane daemon did not become ready", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.Runtime != controller.ControlPlaneMode || status.Hostname != "hostlier.example.com" {
		t.Fatal(status)
	}
	var identity struct{ Fingerprint, URL string }
	if err := api.Request(ctx, "GET", "/v1/hosts/identity", nil, &identity); err != nil || identity.Fingerprint == "" || identity.URL == "" {
		t.Fatalf("missing fleet identity: %+v (%v)", identity, err)
	}
	for _, route := range []struct{ method, path string }{
		{"POST", "/v1/plans"},
		{"POST", "/v1/packages/stage"},
		{"POST", "/v1/resources"},
		{"PUT", "/v1/fleet/master"},
		{"POST", "/v1/fleet-joins/prepare"},
	} {
		err := api.Request(ctx, route.method, route.path, map[string]any{}, nil)
		var failure *model.Error
		if !errors.As(err, &failure) || failure.Code != "CONTROLLER_MODE" {
			t.Errorf("%s %s: %v", route.method, route.path, err)
		}
	}
}

func TestControlPlaneRejectsConflictingOptionsBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"run", "--mode", "control-plane", "--runtime", "k3s"}, "CONTROLLER_MODE"},
		{[]string{"agent", "run", "--mode", "control-plane"}, "CONTROLLER_MODE"},
		{[]string{"run", "--mode", "control-plane", "--host", "0.0.0.0"}, "TLS_REQUIRED"},
		{[]string{"run", "--mode", "control-plane", "--tls-cert", "missing.pem"}, "TLS_REQUIRED"},
	} {
		err := Run(context.Background(), tc.args, nil, io.Discard, io.Discard)
		var failure *model.Error
		if !errors.As(err, &failure) || failure.Code != tc.code {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
}
