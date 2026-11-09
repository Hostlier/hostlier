package cli

import (
	"archive/tar"
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hostlier/internal/fleet"
)

//go:embed server_install.sh
var serverInstallScript string

func (s *session) installServerSSH(target serverInstall, master, dir string) (string, error) {
	remote := "sh -c " + quoteSSHArgument(serverInstallScript) + " hostlier " + quoteSSHArgument(target.domain) + " " + quoteSSHArgument(target.hostname) + " " + quoteSSHArgument(master)
	remote = "set -- " + remote + "; if [ \"$(id -u)\" = 0 ]; then exec \"$@\"; else exec sudo -n -- \"$@\"; fi"
	args := append(s.sshArguments(), "-o", "ControlMaster=no", "-o", "ControlPath=none", "--", s.target, remote)
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stderr = s.errOut
	input, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	fmt.Fprintf(s.errOut, "Installing the controller over SSH to %s…\n", s.target)
	if err = cmd.Start(); err != nil {
		return "", err
	}
	reader := bufio.NewReader(output)
	pin, transferErr := s.transferServerRelease(ctx, reader, input, dir, master)
	input.Close()
	if transferErr != nil {
		cancel()
	}
	err = cmd.Wait()
	if transferErr != nil {
		return "", transferErr
	}
	if err != nil {
		return "", fmt.Errorf("SSH installation failed: %w", err)
	}
	return pin, nil
}

func (s *session) transferServerRelease(ctx context.Context, output *bufio.Reader, input io.WriteCloser, dir, master string) (string, error) {
	line, err := output.ReadSlice('\n')
	arch := strings.TrimSpace(string(line))
	if err == nil && arch == "installed" {
		return serverIdentity(output, master)
	}
	if err != nil || (arch != "amd64" && arch != "arm64") {
		return "", fmt.Errorf("SSH server did not report a supported architecture; check SSH access and passwordless sudo")
	}
	dir, cleanup, err := s.serverReleases(ctx, dir, arch)
	if err != nil {
		return "", err
	}
	defer cleanup()
	bundle := tar.NewWriter(input)
	for _, kind := range []string{"controller", "cli"} {
		name := "hostlier-" + kind + "-linux-" + arch + ".tar.gz"
		if err := sendReleaseArchive(bundle, dir, name); err != nil {
			return "", fmt.Errorf("transfer %s: %w", name, err)
		}
	}
	if err := bundle.Close(); err != nil {
		return "", err
	}
	if err := input.Close(); err != nil {
		return "", err
	}
	return serverIdentity(output, master)
}

func serverIdentity(output io.Reader, master string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(output, 65537))
	var identity struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err != nil || len(data) > 65536 || json.Unmarshal(data, &identity) != nil || !fleet.ValidPin(identity.Fingerprint) || identity.Fingerprint == master {
		return "", fmt.Errorf("SSH installation did not return a valid, distinct controller identity; inspect the installation output")
	}
	return identity.Fingerprint, nil
}

func sendReleaseArchive(out *tar.Writer, dir, name string) error {
	data, err := os.ReadFile(filepath.Join(dir, name+".sha256"))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name || !fleet.ValidPin(fields[0]) {
		return fmt.Errorf("invalid release checksum for %s", name)
	}
	file, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return fmt.Errorf("expected a nonempty release archive")
	}
	if err = out.WriteHeader(&tar.Header{Name: name + ".sha256", Mode: 0600, Size: int64(len(data))}); err != nil {
		return err
	}
	if _, err = out.Write(data); err != nil {
		return err
	}
	if err = out.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: info.Size()}); err != nil {
		return err
	}
	_, err = io.CopyN(out, file, info.Size())
	return err
}
