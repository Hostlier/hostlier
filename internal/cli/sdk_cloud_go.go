package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"hostlier/internal/model"
)

var goModuleLine = regexp.MustCompile(`(?m)^module[ \t]+([^ \t\r\n]+)`)

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
	match := goModuleLine.FindSubmatch(mod)
	if len(match) != 2 {
		return model.Err("SDK_DIRECTORY", "Go module path is missing")
	}
	destination := filepath.Join(root, "hostliercloud")
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || !bundledCloudGoSDK(destination) {
			return model.Err("SDK_CONFLICT", "Existing hostliercloud package was not installed by Hostlier")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Add only the parser dependency. Module source comes from this CLI, not a registry.
	if !strings.Contains(string(mod), "gopkg.in/yaml.v3") {
		command := exec.CommandContext(s.ctx, "go", "mod", "edit", "-require=gopkg.in/yaml.v3@v3.0.1")
		command.Dir = root
		if err := command.Run(); err != nil {
			return model.Err("GO_MOD", "Cannot add the YAML module requirement; install Go and inspect go.mod")
		}
		if err := addCloudGoYAMLSums(root); err != nil {
			return err
		}
	}
	stage, err := os.MkdirTemp(root, ".hostliercloud-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	sources := map[string]string{"cloudsdk.go.txt": "cloudsdk.go", "cloudsdk.api.go.txt": "api.go", "cloudsdk.README.md": "README.md"}
	for asset, name := range sources {
		data, readErr := cloudSDKAssets.ReadFile("cloud_sdk_assets/" + asset)
		if readErr != nil {
			return readErr
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
	return s.print(map[string]string{"directory": destination, "import": string(match[1]) + "/hostliercloud", "dependency": "gopkg.in/yaml.v3"})
}

// addCloudGoYAMLSums pins the one transitive module without fetching it during SDK installation.
func addCloudGoYAMLSums(root string) error {
	path := filepath.Join(root, "go.sum")
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	const module = "gopkg.in/yaml.v3 v3.0.1 h1:fxVm/GzAzEWqLHuvctI91KS9hhNmmWOoWu0XTYJS7CA="
	const manifest = "gopkg.in/yaml.v3 v3.0.1/go.mod h1:K4uyk7z7BCEPqu6E+C64Yfv1cQ7kz7rIZviUmN+EgEM="
	var addition strings.Builder
	if len(current) > 0 && current[len(current)-1] != '\n' {
		addition.WriteByte('\n')
	}
	if !strings.Contains(string(current), module) {
		addition.WriteString(module + "\n")
	}
	if !strings.Contains(string(current), manifest) {
		addition.WriteString(manifest + "\n")
	}
	if addition.Len() == 0 {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(addition.String())
	return err
}

// bundledCloudGoSDK protects a same-named package owned by the user's repository.
func bundledCloudGoSDK(directory string) bool {
	data, err := os.ReadFile(filepath.Join(directory, ".hostlier-cloud-sdk"))
	return err == nil && string(data) == "bundled\n"
}
