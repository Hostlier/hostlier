package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

const memoryHelp = `Hostlier memory — existing host storage with confined access

Usage:
  hostlier memory validate|plan|deploy [--file .hostlier] [--server NAME]
  hostlier memory list [--server NAME]
  hostlier memory status|stop|start|restart|delete APP [--server NAME]
  hostlier memory grants APP [--server NAME]
  hostlier memory grant APP EXPORT --allow read|write --for 24h --output FILE
  hostlier memory revoke APP GRANT_ID
  hostlier memory mount APP EXPORT --output EXISTING_MOUNTPOINT

Grant files contain credentials and are created exclusively with mode 0600.
Mount uses the operating system's WebDAV client and prompts for a storage grant's
username/password. macOS uses mount_webdav; Linux requires davfs2 and permission
to mount. Private controller CAs must also be trusted by the OS mount client.
`

func (s *session) memoryCommand() error {
	if s.flags["on"] != "" && s.flags["on"] != "local" {
		return model.Err("ARGUMENTS", "Connect directly to the memory controller")
	}
	base := "/v1/memory/" + url.PathEscape(s.target) + "/grants"
	switch s.sub {
	case "list":
		return s.getPrint("/v1/apps")
	case "grants":
		if s.target == "" {
			return model.Err("ARGUMENTS", memoryHelp)
		}
		return s.getPrint(base)
	case "grant":
		name := s.position(3)
		if s.target == "" || name == "" || len(s.parsed.args) != 4 || s.flags["output"] == "" {
			return model.Err("ARGUMENTS", memoryHelp)
		}
		access := s.flags["allow"]
		if access != "read" && access != "write" {
			return model.Err("ARGUMENTS", "--allow must be read or write")
		}
		duration, err := time.ParseDuration(defaultValue(s.flags["for"], "24h"))
		if err != nil || duration < time.Minute || duration > 30*24*time.Hour {
			return model.Err("ARGUMENTS", "--for must be between 1m and 720h")
		}
		filename := s.flags["output"]
		if !filepath.IsAbs(filename) {
			filename = filepath.Join(s.cwd, filename)
		}
		f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		success := false
		defer func() {
			f.Close()
			if !success {
				os.Remove(filename)
			}
		}()
		var grant map[string]any
		if err = s.request("POST", base, map[string]any{"export": name, "readOnly": access == "read", "durationSeconds": int64(duration / time.Second)}, &grant); err != nil {
			return err
		}
		if remote, ok := s.remote.(*client.Client); ok {
			grant["controller"] = remote.URL
		}
		if err = json.NewEncoder(f).Encode(grant); err != nil {
			return err
		}
		if err = f.Sync(); err != nil {
			return err
		}
		success = true
		return s.print(map[string]any{"credentialsFile": filename, "expiresAt": grant["expiresAt"], "readOnly": grant["readOnly"]})
	case "revoke":
		if s.target == "" || s.position(3) == "" || len(s.parsed.args) != 4 {
			return model.Err("ARGUMENTS", memoryHelp)
		}
		var out any
		if err := s.request("DELETE", base+"/"+url.PathEscape(s.position(3)), nil, &out); err != nil {
			return err
		}
		return s.print(out)
	case "mount":
		return s.mountMemory()
	default:
		return model.Err("ARGUMENTS", memoryHelp)
	}
}
func (s *session) mountMemory() error {
	export := s.position(3)
	if s.target == "" || export == "" || len(s.parsed.args) != 4 || s.flags["output"] == "" {
		return model.Err("ARGUMENTS", memoryHelp)
	}
	var app struct{ Spec model.Project }
	if err := s.request("GET", "/v1/apps/"+url.PathEscape(s.target), nil, &app); err != nil {
		return err
	}
	selected, ok := app.Spec.Exports[export]
	if app.Spec.Kind != "memory" || !ok {
		return model.Err("NOT_FOUND", "Memory export not found")
	}
	hasDAV := false
	for _, p := range selected.Protocols {
		hasDAV = hasDAV || p == "webdav"
	}
	if !hasDAV {
		return model.Err("MEMORY_PROTOCOL", "This export does not enable webdav")
	}
	remote, ok := s.remote.(*client.Client)
	if !ok {
		return model.Err("CONNECTION", "Native mounting requires a saved HTTPS controller profile")
	}
	address, err := url.Parse(remote.URL)
	if err != nil || address.Scheme != "https" || address.User != nil {
		return model.Err("CONNECTION", "Native mounting requires HTTPS")
	}
	address.Path = "/memory/webdav/" + s.target + "/" + export + "/"
	address.RawQuery = ""
	address.Fragment = ""
	output := s.flags["output"]
	if !filepath.IsAbs(output) {
		output = filepath.Join(s.cwd, output)
	}
	mountpoint, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	info, err := os.Stat(mountpoint)
	if err != nil || !info.IsDir() {
		return model.Err("MOUNTPOINT", "--output must be an existing empty directory")
	}
	entries, err := os.ReadDir(mountpoint)
	if err != nil || len(entries) != 0 {
		return model.Err("MOUNTPOINT", "Mountpoint must be empty")
	}
	binary, args, err := memoryMountCommand(runtime.GOOS, address.String(), mountpoint)
	if err != nil {
		return err
	}
	fmt.Fprintln(s.errOut, "Enter the username and password from your storage grant when the native mount client prompts.")
	cmd := exec.CommandContext(s.ctx, binary, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.in, s.out, s.errOut
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("native WebDAV mount failed (check davfs2/OS trust and mount permissions): %w", err)
	}
	return s.print(map[string]string{"mounted": mountpoint, "url": address.String()})
}
func memoryMountCommand(goos, address, mountpoint string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "/sbin/mount_webdav", []string{"-i", "-s", address, mountpoint}, nil
	case "linux":
		return "mount", []string{"-t", "davfs", address, mountpoint}, nil
	default:
		return "", nil, model.Err("MEMORY_PLATFORM", "Native mounting is supported on macOS and Linux")
	}
}
