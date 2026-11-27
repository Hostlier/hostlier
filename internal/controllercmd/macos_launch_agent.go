package controllercmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"hostlier/internal/model"
)

func macDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (p macInitPlan) binaryPath(digest string) string {
	return filepath.Join(p.StateDir, "bin", "hostlier-controller-"+digest)
}

// readPriorAgent recognizes only the canonical launch agent for this state directory.
func (p *macInitPlan) readPriorAgent() error {
	data, err := readMacInstallFile(p.LaunchAgent)
	if err != nil || data == nil {
		return err
	}
	var plist struct {
		Args []string `xml:"dict>array>string"`
	}
	conflict := model.Err("INSTALL_OWNERSHIP", "The existing LaunchAgent is not owned by this Hostlier installation")
	if xml.Unmarshal(data, &plist) != nil || len(plist.Args) < 6 || !slices.Equal(plist.Args[1:6], []string{"run", "--mode", "control-plane", "--state-dir", p.StateDir}) {
		return conflict
	}
	binary := plist.Args[0]
	digest := strings.TrimPrefix(filepath.Base(binary), "hostlier-controller-")
	if !validSHA256Digest("sha256:"+digest) || binary != p.binaryPath(digest) || (len(plist.Args)-6)%2 != 0 {
		return conflict
	}
	for i := 6; i < len(plist.Args); i += 2 {
		name, flagged := strings.CutPrefix(plist.Args[i], "--")
		if !flagged || !slices.Contains(macDaemonFlags, name) || p.DaemonOptions[name] != "" {
			return conflict
		}
		p.DaemonOptions[name] = plist.Args[i+1]
	}
	if !bytes.Equal(data, p.renderAgent(binary)) {
		return conflict
	}
	p.previous, p.previousBinary, p.PriorAgentSHA256 = data, binary, macDigest(data)
	return nil
}

func (p *macInitPlan) prepareAgent(source string) error {
	var err error
	p.SourceSHA256, err = maintenanceHash(source)
	if err != nil {
		return err
	}
	p.Executable = p.binaryPath(p.SourceSHA256)
	if p.ConfigPath == p.SocketPath || p.ConfigPath == p.LaunchAgent || p.ConfigPath == p.Executable ||
		p.SocketPath == p.LaunchAgent || p.SocketPath == p.Executable {
		return model.Err("INSTALL_PATH", "Use separate paths for the configuration, admin socket and LaunchAgent")
	}
	p.plist = p.renderAgent(p.Executable)
	return nil
}

func (p macInitPlan) renderAgent(binary string) []byte {
	args := []string{binary, "run", "--mode", "control-plane", "--state-dir", p.StateDir}
	for _, name := range macDaemonFlags {
		if value := p.DaemonOptions[name]; value != "" {
			args = append(args, "--"+name, value)
		}
	}
	var data bytes.Buffer
	data.WriteString(xml.Header + `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>`)
	writeMacPlistString(&data, p.Label)
	data.WriteString("<key>ProgramArguments</key><array>")
	for _, arg := range args {
		writeMacPlistString(&data, arg)
	}
	data.WriteString("</array>\n\t<key>StandardOutPath</key>")
	writeMacPlistString(&data, filepath.Join(p.StateDir, "launchd.log"))
	data.WriteString("<key>StandardErrorPath</key>")
	writeMacPlistString(&data, filepath.Join(p.StateDir, "launchd.log"))
	data.WriteString(`<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>Umask</key><integer>63</integer>
</dict>
</plist>
`)
	return data.Bytes()
}

func writeMacPlistString(out *bytes.Buffer, value string) {
	out.WriteString("<string>")
	xml.EscapeText(out, []byte(value))
	out.WriteString("</string>\n\t")
}

func readMacInstallFile(name string) ([]byte, error) {
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", name)
	}
	return os.ReadFile(name)
}

func (sys macInitSystem) inspectLaunchd(ctx context.Context, p *macInitPlan) error {
	if _, err := sys.launch(ctx, "print", sys.domain()); err != nil {
		return model.Err("LAUNCHD_SESSION", "Open a terminal in your logged-in macOS session and run init there")
	}
	target := sys.domain() + "/" + p.Label
	out, err := sys.launch(ctx, "print", target)
	if err == nil {
		if p.previous == nil || !strings.Contains(out, "path = "+p.LaunchAgent+"\n") {
			return model.Err("INSTALL_OWNERSHIP", "The launchd service label is already in use by another installation")
		}
		p.AlreadyLoaded = true
		return nil
	}
	if !strings.Contains(out, "Could not find service") {
		return fmt.Errorf("inspect launchd service: %w: %s", err, strings.TrimSpace(out))
	}
	return checkControlPlaneSocket(p.SocketPath, false)
}

func checkControlPlaneSocket(path string, removeStale bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().Type() != os.ModeSocket {
		return model.Err("INSTALL_PATH", "The admin socket path is already used by another file")
	}
	conn, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err == nil {
		conn.Close()
		return model.Err("INSTALL_STATE", "Stop the existing foreground controller before installing its LaunchAgent")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return err
	}
	if !removeStale {
		return nil
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !ok || owner.Uid != uint32(os.Geteuid()) || !os.SameFile(info, current) {
		return model.Err("INSTALL_PATH", "The admin socket changed or belongs to another user")
	}
	return os.Remove(path)
}

// waitMacControlPlane checks the launched service through its protected socket.
func waitMacControlPlane(ctx context.Context, p macInitPlan) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.SocketPath)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/v1/status", nil)
		if err != nil {
			return err
		}
		if response, err := client.Do(request); err == nil {
			var status struct{ Runtime, Hostname string }
			err = json.NewDecoder(response.Body).Decode(&status)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && err == nil && status.Runtime == p.Runtime && status.Hostname == p.ControllerHostname {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("launchd did not start a ready control plane. See %s: %w", filepath.Join(p.StateDir, "launchd.log"), ctx.Err())
		case <-ticker.C:
		}
	}
}
