package cli

import (
	"hostlier/internal/model"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// prepareConfigFiles snapshots local text files before requesting immutable deployment approval.
func (s *session) prepareConfigFiles(p model.Project) (model.Project, error) {
	root, err := filepath.Abs(filepath.Dir(s.projectPath()))
	// Invalid roots cannot fall back to reading the controller's filesystem.
	if err != nil {
		return p, err
	}
	// Only declared sources are read; inline and encrypted secret references stay unchanged.
	for name, file := range p.Configs {
		// Already prepared files remain stable through planning retries.
		if file.File == "" {
			continue
		}
		content, err := readConfigSource(root, file)
		// A missing or changing source aborts the entire desired group.
		if err != nil {
			return p, err
		}
		file.Content = content
		file.File = ""
		p.Configs[name] = file
	}
	// Every component uses the same project-relative source boundary.
	for name, member := range p.Sidecars {
		prepared, err := s.prepareConfigFiles(member)
		// Abort the complete group snapshot when any member source is unavailable.
		if err != nil {
			return p, err
		}
		p.Sidecars[name] = prepared
	}
	return p, nil
}

// readConfigSource confines source reads to regular files beneath the authored project directory.
func readConfigSource(root string, file model.ConfigFile) (string, error) {
	// File sources cannot hide a second source or select absolute host paths.
	if file.Content != "" || file.Secret != "" || filepath.IsAbs(file.File) {
		return "", model.Err("CONFIG_SOURCE", "Use one project-relative managed config source")
	}
	full, err := resolvedConfigSource(root, file.File)
	// Resolve and confine the project path before opening it.
	if err != nil {
		return "", err
	}
	f, err := os.Open(full)
	// Missing source bytes cannot be substituted with previous runtime content.
	if err != nil {
		return "", model.Err("CONFIG_SOURCE", "Cannot open managed config source")
	}
	defer f.Close()
	info, err := f.Stat()
	// Devices, pipes and oversized files are never uploaded as configuration.
	if err != nil || !validConfigSourceInfo(info) {
		return "", model.Err("CONFIG_SOURCE", "Managed config source must be a regular file up to 64 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	// The read bound also covers files that grow after their metadata was checked.
	if err != nil || len(data) > 65536 {
		return "", model.Err("CONFIG_SOURCE", "Cannot snapshot managed config source")
	}
	return string(data), nil
}

// resolvedConfigSource resolves links without allowing a project file to escape its source directory.
func resolvedConfigSource(root, source string) (string, error) {
	full, err := filepath.EvalSymlinks(filepath.Join(root, source))
	// Missing paths cannot become empty configuration data.
	if err != nil {
		return "", model.Err("CONFIG_SOURCE", "Cannot read managed config source")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	// The canonical source must remain a descendant of the canonical project root.
	if err != nil || !strings.HasPrefix(full, realRoot+string(filepath.Separator)) {
		return "", model.Err("CONFIG_SOURCE", "Managed config source must remain inside the project directory")
	}
	return full, nil
}

// validConfigSourceInfo admits bounded ordinary text files, excluding pipes and devices.
func validConfigSourceInfo(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Size() <= 65536
}
