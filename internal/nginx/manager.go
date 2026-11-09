package nginx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// Runner executes bounded local configuration checks, reloads and certificate tooling.
type Runner func(context.Context, string, ...string) error

// Options supplies owned paths and injectable local tooling for the Nginx edge.
type Options struct {
	// Gateway confines both backend listeners to loopback; Caddy owns public TLS and QUIC.
	Gateway                                             bool
	H2Address                                           string
	Directory, CertificateDirectory, ChallengeDirectory string
	HTTPAddress, HTTPSAddress                           string
	// ACMEDirectoryURL is an explicit loopback-only integration-test override.
	// Empty uses Certbot's production directory and normal system trust.
	ACMEDirectoryURL string
	IsolatedACME     bool
	// CertificateUnit delegates ACME issuance to an installed systemd template.
	// This keeps Snap's privilege transition outside the controller service.
	CertificateUnit string
	Run             Runner
}

// Manager serializes edge activation within one controller process.
type Manager struct {
	Options Options
	mu      sync.Mutex
}

// defaults supplies production paths and listeners for explicitly selected Nginx installations.
func defaults(o Options) Options {
	// Gateway defaults never allocate a second public edge listener.
	if o.Gateway {
		o = gatewayDefaults(o)
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if o.Directory == "" {
		o.Directory = "/etc/hostlier/nginx"
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if o.CertificateDirectory == "" {
		o.CertificateDirectory = "/etc/hostlier/nginx-certificates"
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if o.ChallengeDirectory == "" {
		o.ChallengeDirectory = "/var/lib/hostlier-acme"
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if o.HTTPAddress == "" {
		o.HTTPAddress = "80"
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if o.HTTPSAddress == "" {
		o.HTTPSAddress = "443"
	}
	// Validate the selected edge ownership and configuration before proceeding.
	if o.Run == nil {
		o.Run = runCommand
	}
	return o
}

// runCommand invokes only explicit argument vectors and returns bounded non-sensitive errors.
func runCommand(ctx context.Context, file string, args ...string) error {
	cmd := exec.CommandContext(ctx, file, args...)
	var output bytes.Buffer
	// Certbot's short validation summary is useful when its full log is stored separately.
	if file == "certbot" {
		cmd.Stdout = &output
		cmd.Stderr = &output
	}
	// Stop activation when the required local operation fails.
	if err := cmd.Run(); err != nil {
		// Report the certificate authority's diagnostic without printing the full Certbot log.
		if file == "certbot" {
			// Prefer the actionable ACME detail when Certbot provided one.
			if detail := certbotFailureDetail(output.String()); detail != "" {
				return fmt.Errorf("certificate validation failed: %s: %w", detail, err)
			}
		}
		return fmt.Errorf("nginx edge command failed: %s: %w", file, err)
	}
	return nil
}

// certbotFailureDetail keeps only the final printable validation detail from Certbot output.
func certbotFailureDetail(output string) string {
	lines := strings.Split(output, "\n")
	// The final detail corresponds to the last challenge attempted in this command.
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		// Ignore routine output and account metadata.
		if !strings.HasPrefix(line, "Detail:") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "Detail:"))
		var safe strings.Builder
		// Limit unexpected server response text and remove terminal control characters.
		for _, r := range line {
			// Keep the public failure reason short enough for a terminal error.
			if safe.Len() >= 500 {
				break
			}
			// Certbot may quote text from an HTTP server response.
			if unicode.IsPrint(r) {
				safe.WriteRune(r)
			}
		}
		return safe.String()
	}
	return ""
}

// Apply obtains missing host certificates then validates and activates the complete owned configuration.
func (m *Manager) Apply(ctx context.Context, controller, upstream string, routes []Route) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := defaults(m.Options)
	data, err := Render(o, controller, upstream, routes)
	// Stop activation when the required local operation fails.
	if err != nil {
		return err
	}
	// Stop activation when the required local operation fails.
	if err = ensureEdgeCertificates(ctx, o, controller, routes); err != nil {
		return err
	}
	return activate(ctx, o, data)
}

// Activate validates a supplied complete standalone configuration before atomic replacement.
func Activate(ctx context.Context, o Options, data []byte) error {
	return activate(ctx, defaults(o), data)
}

// activate preserves the previous configuration and rolls it back if reload fails.
func activate(ctx context.Context, o Options, data []byte) error {
	// Validate the selected edge ownership and configuration before proceeding.
	if !safePath(o.Directory) {
		return errors.New("unsafe nginx directory")
	}
	// Stop activation when the required local operation fails.
	if err := os.MkdirAll(o.Directory, 0700); err != nil {
		return err
	}
	path := filepath.Join(o.Directory, "nginx.conf")
	previous, readErr := os.ReadFile(path)
	// Stop activation when the required local operation fails.
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	candidate := path + ".candidate"
	// Stop activation when the required local operation fails.
	if err := writeConfig(candidate, data); err != nil {
		return err
	}
	defer os.Remove(candidate)
	// Stop activation when the required local operation fails.
	if err := o.Run(ctx, "nginx", "-t", "-c", candidate); err != nil {
		return err
	}
	// Stop activation when the required local operation fails.
	if err := os.Rename(candidate, path); err != nil {
		return err
	}
	// Stop activation when the required local operation fails.
	if err := o.Run(ctx, "nginx", "-s", "reload", "-c", path); err != nil {
		return rollback(ctx, o, path, previous, readErr, err)
	}
	return nil
}

// rollback restores the last validated configuration and reports reload failure even after recovery.
func rollback(ctx context.Context, o Options, path string, previous []byte, readErr, errorCause error) error {
	// Stop activation when the required local operation fails.
	if readErr != nil {
		os.Remove(path)
		return errorCause
	}
	// Stop activation when the required local operation fails.
	if err := writeConfig(path, previous); err != nil {
		return fmt.Errorf("nginx reload failed and configuration rollback failed: %w", err)
	}
	// Stop activation when the required local operation fails.
	if err := o.Run(ctx, "nginx", "-s", "reload", "-c", path); err != nil {
		return fmt.Errorf("nginx reload failed and previous configuration reload failed: %w", err)
	}
	return errorCause
}

// writeConfig refuses redirected files and flushes the staging configuration before activation.
func writeConfig(path string, data []byte) error {
	// Validate the selected edge ownership and configuration before proceeding.
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("unsafe nginx configuration path")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".nginx-config-")
	// Only a newly allocated private staging file receives configuration bytes.
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	// Configuration can contain sensitive upstream routing and private certificate paths.
	if err = file.Chmod(0600); err != nil {
		return err
	}
	// Write and flush the complete candidate before publishing its pathname.
	if _, err = file.Write(data); err != nil {
		return err
	}
	// A failed synchronization must leave the previous configuration intact.
	if err = file.Sync(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// Check verifies the owned configuration without changing the active edge.
func Check(ctx context.Context, o Options) error {
	o = defaults(o)
	return o.Run(ctx, "nginx", "-t", "-c", filepath.Join(o.Directory, "nginx.conf"))
}
