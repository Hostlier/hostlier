package controllercmd

import (
	"hostlier/internal/config"
	"hostlier/internal/controller"
	"hostlier/internal/memory"
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// runMemoryController keeps enrollment/listeners but has no container engine.
func (s *session) runMemoryController() error {
	if s.command == "agent" || s.flags["runtime"] != "" || s.flags["tunnel-domain"] != "" || s.flags["tunnel-url"] != "" || s.flags["master-url"] != "" || s.flags["master-fingerprint"] != "" || s.flags["controller-fingerprint"] != "" {
		return model.Err("CONTROLLER_MODE", "Memory mode cannot select a workload runtime, fleet-agent mode or public tunnels")
	}
	userConfig := ""
	if runtime.GOOS == "darwin" && s.flags["state-dir"] == "" {
		var err error
		userConfig, err = os.UserConfigDir()
		if err != nil {
			return err
		}
	}
	stateDir, configPath, socket := memoryPaths(runtime.GOOS, userConfig, s.flags["state-dir"])
	configPath = defaultValue(s.flags["config"], configPath)
	if s.flags["socket"] == "" && socket != "" {
		s.flags["socket"] = socket
	}
	cfg, err := config.ReadServer(configPath)
	if err != nil {
		return err
	}
	runtime, err := memory.New([]string{s.flags["memory-root"]}, stateDir)
	if err != nil {
		return err
	}
	defer runtime.Close()
	settings, err := s.controllerSettings(stateDir)
	if err != nil {
		return err
	}
	server, err := controller.New(controller.Options{StateDir: stateDir, Config: cfg, ConfigPath: configPath, Runtime: runtime, Hostname: settings.hostname, LogPath: s.flags["log-path"], WriteTimeout: 10 * time.Minute})
	if err != nil {
		return err
	}
	return s.serveInstalledController(server, settings.host, settings.port)
}

func memoryPaths(platform, userConfig, stateOverride string) (state, config, socket string) {
	if platform == "darwin" {
		state = defaultValue(stateOverride, filepath.Join(userConfig, "Hostlier", "memory"))
		return state, filepath.Join(state, "config.yaml"), filepath.Join(state, "controller.sock")
	}
	return defaultValue(stateOverride, "/var/lib/hostlier-memory"), "/etc/hostlier/config.yaml", ""
}
