package controllercmd

import (
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"hostlier/internal/config"
	"hostlier/internal/controller"
	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// runControlPlane starts the native daemon with the same TLS fleet and approval APIs as compute hosts.
func (s *session) runControlPlane() error {
	listeners, err := s.controlPlaneListeners()
	if err != nil {
		return err
	}
	stateDir := s.flags["state-dir"]
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		stateDir = filepath.Join(home, ".hostlier", "control-plane")
	}
	configPath := defaultValue(s.flags["config"], filepath.Join(stateDir, "config.yaml"))
	cfg, err := config.ReadServer(configPath)
	if err != nil {
		return err
	}
	hostname, err := config.NormalizeDomain(defaultValue(s.flags["hostname"], defaultValue(s.flags["controller-hostname"], "hostlier."+cfg.Domains[0])))
	if err != nil {
		return err
	}
	enrollment, err := s.controllerEmail(hostname)
	if err != nil {
		return err
	}
	origin := "https://" + hostname
	if listeners.TLSConfig != nil && listeners.Port != 443 {
		origin = "https://" + net.JoinHostPort(hostname, strconv.Itoa(listeners.Port))
	}
	server, err := controller.NewControlPlane(controller.Options{
		StateDir: stateDir, Config: cfg, ConfigPath: configPath, Hostname: hostname,
		LogPath: defaultValue(s.flags["log-path"], filepath.Join(stateDir, "controller.log")), EmailEnrollment: enrollment, PublicOrigin: origin,
	})
	if err != nil {
		return err
	}
	listeners.SocketPath = defaultValue(s.flags["socket"], filepath.Join(stateDir, "controller.sock"))
	// The controller state lock is held while recovering a socket left by a crash.
	if err := checkControlPlaneSocket(listeners.SocketPath, true); err != nil {
		server.Close(s.ctx)
		return err
	}
	return s.serveController(server, listeners)
}

// controlPlaneListeners validates transport settings before writing controller state.
func (s *session) controlPlaneListeners() (controller.ListenOptions, error) {
	for _, name := range []string{"runtime", "proxy", "memory-root", "master-url", "master-fingerprint", "controller-fingerprint", "tunnel-domain", "tunnel-url", "data-config"} {
		if s.flags[name] != "" {
			return controller.ListenOptions{}, model.Err("CONTROLLER_MODE", "Control-plane mode does not support --"+name)
		}
	}
	port, err := controllerPort(s.flags["port"])
	if err != nil {
		return controller.ListenOptions{}, err
	}
	host := defaultValue(s.flags["host"], "127.0.0.1")
	tlsConfig, err := controlPlaneTLS(s.flags["tls-cert"], s.flags["tls-key"])
	if err != nil {
		return controller.ListenOptions{}, err
	}
	if tlsConfig == nil && !allowedControllerBind(host, "") {
		return controller.ListenOptions{}, model.Err("TLS_REQUIRED", "A nonlocal control-plane listener requires --tls-cert and --tls-key")
	}
	return controller.ListenOptions{
		Host: host, Port: port, TCP: true, TLSConfig: tlsConfig,
		FleetAddress: defaultValue(s.flags["fleet-address"], ":"+fleet.Port),
	}, nil
}

func controlPlaneTLS(certPath, keyPath string) (*tls.Config, error) {
	if certPath == "" && keyPath == "" {
		return nil, nil
	}
	if certPath == "" || keyPath == "" {
		return nil, model.Err("TLS_REQUIRED", "Supply both --tls-cert and --tls-key")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}
