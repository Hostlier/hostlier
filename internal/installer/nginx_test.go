package installer

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// TestNginxInstallationAssets makes the selected edge and automatic renewal reviewable before installation.
func TestNginxInstallationAssets(t *testing.T) {
	assets, err := InstallationAssets(Options{Domain: "example.com", Proxy: "nginx"})
	// A valid explicit proxy selection must produce standalone service assets.
	if err != nil {
		t.Fatal(err)
	}
	// The controller unit must use the same proxy selection on every reboot.
	if !strings.Contains(assets.Service, "--proxy nginx") || strings.Contains(assets.Service, "caddy.service") {
		t.Fatal(assets.Service)
	}
	// Renewal must verify configuration before reloading the owned master process.
	if !strings.Contains(assets.NginxRenewalService, "nginx -t") || !strings.Contains(assets.NginxRenewalTimer, "Persistent=true") {
		t.Fatal(assets.NginxRenewalService, assets.NginxRenewalTimer)
	}
	// Bootstrap HTTP-01 must work before an HTTPS certificate exists.
	if strings.Contains(assets.Nginx, "ssl_certificate") || !strings.Contains(assets.Nginx, "/.well-known/acme-challenge/") {
		t.Fatal(assets.Nginx)
	}
}

// TestProxyMigrationRefused preserves default legacy Caddy ownership rather than replacing it implicitly.
func TestProxyMigrationRefused(t *testing.T) {
	plan := Plan{Options: Options{Proxy: "nginx"}}
	inspectProxyMigration(&plan, Installation{OwnedCaddy: true})
	// Legacy records contain no Proxy field and therefore represent Caddy.
	if len(plan.Errors) == 0 {
		t.Fatal("legacy Caddy silently adopted as Nginx")
	}
	plan = Plan{Options: Options{Proxy: "caddy"}}
	inspectProxyMigration(&plan, Installation{Proxy: "nginx", OwnedNginx: true})
	// The reverse migration is equally unsafe without an explicit workflow.
	if len(plan.Errors) == 0 {
		t.Fatal("Nginx silently adopted as Caddy")
	}
}

// TestNginxReadinessWaitsForReload covers the first asynchronous HTTPS activation.
func TestNginxReadinessWaitsForReload(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	// Stop the check when its prerequisite operation fails.
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	ready := make(chan net.Listener, 1)
	// Simulate the Nginx master returning before its TLS worker starts listening.
	go func() {
		time.Sleep(100 * time.Millisecond)
		delayed, _ := net.Listen("tcp", address)
		ready <- delayed
	}()
	// Stop the check when its prerequisite operation fails.
	if err = waitNginxListener(context.Background(), address); err != nil {
		t.Fatal(err)
	}
	// Release the simulated worker listener after readiness succeeds.
	if delayed := <-ready; delayed != nil {
		delayed.Close()
	}
	// Cancellation must end readiness promptly when activation never opens a listener.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Stop the check when its prerequisite operation fails.
	if err = waitNginxListener(ctx, address); err == nil {
		t.Fatal("cancelled readiness succeeded")
	}
}
