package cli

import (
	"errors"
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"hostlier/internal/model"
)

// installCloudGoSDK copies the Go client shipped with this CLI into an existing module.
func (s *session) installCloudGoSDK() error {
	if len(s.parsed.args) != 3 {
		return model.Err("ARGUMENTS", "Usage: hostlier sdk install cloud-go [--directory GO_PROJECT]")
	}
	for flag := range s.flags {
		if flag != "directory" && flag != "json" && flag != "toon" {
			return model.Err("ARGUMENTS", "Cloud Go SDK installation accepts only --directory and output format flags")
		}
	}
	root, err := filepath.Abs(defaultValue(s.flags["directory"], s.cwd))
	if err != nil {
		return err
	}
	modPath := filepath.Join(root, "go.mod")
	mod, err := os.ReadFile(modPath)
	if err != nil || len(mod) > 1024*1024 {
		return model.Err("SDK_DIRECTORY", "Choose an existing Go module directory")
	}
	module, err := modfile.ParseLax(modPath, mod, nil)
	if err != nil || module.Module == nil {
		return model.Err("SDK_DIRECTORY", "Go module path is missing or invalid")
	}
	if module.Go == nil || version.Compare("go"+module.Go.Version, "go1.24") < 0 {
		return model.Err("GO_MOD", "The Go cloud SDK requires a module targeting Go 1.24 or newer")
	}
	destination := filepath.Join(root, "hostliercloud")
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || !bundledCloudGoSDK(destination) {
			return model.Err("SDK_CONFLICT", "Existing hostliercloud package was not installed by Hostlier")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Module sources and checksums ship with the CLI. Keep existing dependency versions.
	if err := addCloudGoRequirements(s, root, module); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(root, ".hostliercloud-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	sources := map[string]string{"cloudsdk.go.txt": "cloudsdk.go", "cloudsdk.api.go.txt": "api.go", "cloudsdk.grpc.go.txt": "grpc.go", "controller.pb.go.txt": "controlrpc/controller.pb.go", "controlrpc.watch.go.txt": "controlrpc/watch.go", "cloudsdk.README.md": "README.md"}
	for asset, name := range sources {
		data, readErr := cloudSDKAssets.ReadFile("cloud_sdk_assets/" + asset)
		if readErr != nil {
			return readErr
		}
		if name == "grpc.go" {
			data = []byte(strings.ReplaceAll(string(data), `"hostlier/internal/controlrpc"`, fmt.Sprintf("%q", module.Module.Mod.Path+"/hostliercloud/controlrpc")))
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(stage, name)), 0755); err != nil {
			return err
		}
		if writeErr := os.WriteFile(filepath.Join(stage, name), data, 0644); writeErr != nil {
			return writeErr
		}
	}
	if err := os.WriteFile(filepath.Join(stage, ".hostlier-cloud-sdk"), []byte("bundled\n"), 0644); err != nil {
		return err
	}
	if err := os.Chmod(stage, 0755); err != nil {
		return err
	}
	backup := stage + ".old"
	if _, err := os.Lstat(destination); err == nil {
		if err := os.Rename(destination, backup); err != nil {
			return err
		}
		if err := os.Rename(stage, destination); err != nil {
			_ = os.Rename(backup, destination)
			return err
		}
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(stage, destination); err != nil {
			return err
		}
	} else {
		return err
	}
	return s.print(map[string]string{"directory": destination, "import": module.Module.Mod.Path + "/hostliercloud", "dependency": "gopkg.in/yaml.v3"})
}

// Install only missing requirements and the matching known sums, without
// fetching code, running project hooks, or lowering a project's versions.
func addCloudGoRequirements(s *session, root string, module *modfile.File) error {
	data, err := cloudSDKAssets.ReadFile("cloud_sdk_assets/cloudsdk.sums.txt")
	if err != nil {
		return err
	}
	sumsPath := filepath.Join(root, "go.sum")
	current, err := os.ReadFile(sumsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var additions strings.Builder
	args := []string{"mod", "edit"}
	versions := make(map[string]string)
	for _, r := range module.Require {
		versions[r.Mod.Path] = r.Mod.Version
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if !strings.HasSuffix(fields[1], "/go.mod") && semver.Compare(versions[fields[0]], fields[1]) < 0 {
			args = append(args, "-require="+fields[0]+"@"+fields[1])
		}
		if !strings.Contains(string(current), line) {
			additions.WriteString(line + "\n")
		}
	}
	if len(args) > 2 {
		command := exec.CommandContext(s.ctx, "go", args...)
		command.Dir = root
		if err := command.Run(); err != nil {
			return model.Err("GO_MOD", "Cannot add Go SDK dependencies; inspect go.mod")
		}
	}
	if additions.Len() == 0 {
		return nil
	}
	file, err := os.OpenFile(sumsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if len(current) > 0 && current[len(current)-1] != '\n' {
		if _, err = file.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = file.WriteString(additions.String())
	return err
}

// bundledCloudGoSDK protects a same-named package owned by the user's repository.
func bundledCloudGoSDK(directory string) bool {
	data, err := os.ReadFile(filepath.Join(directory, ".hostlier-cloud-sdk"))
	return err == nil && string(data) == "bundled\n"
}
