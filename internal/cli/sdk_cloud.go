package cli

import (
	"embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"hostlier/internal/model"
)

//go:embed cloud_sdk_assets/*
var cloudSDKAssets embed.FS

var cloudSDKFiles = []string{"index.js", "index.d.ts", "package.json", "YAML-LICENSE"}

// installCloudSDK writes the self-contained Node client shipped in this exact CLI release.
func (s *session) installCloudSDK() error {
	if len(s.parsed.args) != 3 {
		return model.Err("ARGUMENTS", "Usage: hostlier sdk install cloud [--directory NODE_PROJECT]")
	}
	for flag := range s.flags {
		if flag != "directory" && flag != "json" && flag != "toon" {
			return model.Err("ARGUMENTS", "Cloud SDK installation accepts only --directory and output format flags")
		}
	}
	root, err := filepath.Abs(defaultValue(s.flags["directory"], s.cwd))
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return model.Err("SDK_DIRECTORY", "Choose an existing Node project directory")
	}
	modules := filepath.Join(root, "node_modules")
	scope := filepath.Join(modules, "@hostlier")
	if err := ensureSDKDirectory(modules); err != nil {
		return err
	}
	if err := ensureSDKDirectory(scope); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(scope, ".cloud-sdk-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	version := ""
	for _, name := range cloudSDKFiles {
		content, readErr := cloudSDKAssets.ReadFile("cloud_sdk_assets/" + name)
		if readErr != nil {
			return readErr
		}
		if name == "package.json" {
			var metadata struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(content, &metadata) != nil || metadata.Version == "" {
				return model.Err("SDK_ASSET", "Bundled cloud SDK metadata is invalid")
			}
			version = metadata.Version
		}
		if writeErr := os.WriteFile(filepath.Join(stage, name), content, 0644); writeErr != nil {
			return writeErr
		}
	}
	if err := os.Chmod(stage, 0755); err != nil {
		return err
	}
	destination := filepath.Join(scope, "cloud-sdk")
	backup := stage + ".old"
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || !bundledCloudSDK(destination) {
			return model.Err("SDK_CONFLICT", "Existing @hostlier/cloud-sdk was not installed by Hostlier")
		}
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
	return s.print(map[string]string{"directory": destination, "import": "@hostlier/cloud-sdk", "version": version})
}

// ensureSDKDirectory never follows a preexisting node_modules or scope symlink.
func ensureSDKDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.Mkdir(path, 0755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return model.Err("SDK_DIRECTORY", "SDK installation path must be an ordinary directory")
	}
	return nil
}

// bundledCloudSDK prevents overwriting a package managed by another installer.
func bundledCloudSDK(path string) bool {
	data, err := os.ReadFile(filepath.Join(path, "package.json"))
	if err != nil || len(data) > 4096 {
		return false
	}
	var metadata struct {
		Name            string `json:"name"`
		HostlierBundled bool   `json:"hostlierBundled"`
	}
	return json.Unmarshal(data, &metadata) == nil && metadata.Name == "@hostlier/cloud-sdk" && metadata.HostlierBundled
}
