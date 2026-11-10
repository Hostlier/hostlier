package controllercmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/controller"
	"hostlier/internal/emailauth"
	"hostlier/internal/fleet"
	"hostlier/internal/migration"
	"hostlier/internal/model"
	"hostlier/internal/recovery"
	"hostlier/internal/resourceclient"
	runtimes "hostlier/internal/runtime"
)

// runController loads runtime and server settings before listening until shutdown.
func (s *session) runController() error {
	var e error

	stateDir := defaultValue(s.flags["state-dir"], "/var/lib/hostlier")
	// Preserve failure before advancing to the next external-effect boundary.
	if err := migration.CheckStartup(stateDir); err != nil {
		return err
	}
	configPath := defaultValue(s.flags["config"], "/etc/hostlier/config.yaml")
	cfg, e := config.ReadServer(configPath)
	// Stop when server configuration cannot be loaded and validated.
	if e != nil {
		return e
	}
	settings, e := s.controllerSettings(stateDir)
	// Stop daemon startup when configuration or dependencies are unavailable.
	if e != nil {
		return e
	}
	runtime, hostname, host, port := settings.runtime, settings.hostname, settings.host, settings.port
	adapter, e := controllerRuntime(runtimes.Options{Proxy: settings.proxy, Type: runtime, StateDir: stateDir, Hostname: hostname, ControllerUpstream: fmt.Sprintf("%s:%d", host, port)})
	// Stop startup when the selected runtime or controller cannot be constructed.
	if e != nil {
		return e
	}
	logPath := defaultValue(s.flags["log-path"], "/var/log/hostlier/hostlier.log")
	manager, managerError := resourceclient.New(resourceclient.Options{Type: runtime, StateDir: stateDir})
	// Stop startup when the selected runtime or controller cannot be constructed.
	if managerError != nil {
		return managerError
	}
	emailEnrollment, e := s.controllerEmail(hostname)
	// Advance only after the required recovery invariant holds.
	if e != nil {
		return e
	}
	destinations, e := s.loadRecoveryDestinations()
	// Advance only after the required recovery invariant holds.
	if e != nil {
		return e
	}
	server, e := controller.New(controller.Options{Maintenance: newServerMaintenance(stateDir, configPath), TunnelDomain: s.flags["tunnel-domain"], TunnelURL: s.flags["tunnel-url"], ManagedCollector: true, CollectorEndpoint: os.Getenv("HOSTLIER_COLLECTOR_URL"), CollectorCAFile: os.Getenv("HOSTLIER_CA_FILE"), RecoveryDestinations: destinations, EmailEnrollment: emailEnrollment, StateDir: stateDir, Config: cfg, ConfigPath: configPath, Runtime: adapter, Hostname: hostname, LogPath: logPath, ResourceManager: manager})
	// Stop startup when the selected runtime or controller cannot be constructed.
	if e != nil {
		return e
	}
	closeIdle, err := runtimes.StartIdleGateway(s.ctx, adapter)
	// Never accept public traffic with an unrestorable idle route.
	if err != nil {
		server.Close(context.Background())
		return err
	}
	defer closeIdle()
	return s.serveInstalledController(server, host, port)
}

// serveInstalledController owns the daemon listeners until the command is cancelled.
func (s *session) serveInstalledController(server *controller.Controller, host string, port int) error {
	socket := defaultValue(s.flags["socket"], "/run/hostlier/controller.sock")
	// Abort startup when the configured listener cannot be opened.
	if e := server.Listen(controller.ListenOptions{MasterFingerprint: s.flags["master-fingerprint"], SocketPath: socket, Host: host, Port: port, TCP: true, ControllerFingerprint: s.flags["controller-fingerprint"]}); e != nil {
		server.Close(context.Background())
		return e
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if e := s.printAgentIdentity(server); e != nil {
		server.Close(context.Background())
		return e
	}
	s.print(map[string]bool{"running": true})
	<-s.ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return server.Close(shutdown)
}

// installedController abstracts listener startup and shutdown for the installed daemon.
type installedController struct{ Runtime, Hostname, ControllerHostname, Proxy string }

// controllerSettings groups validated daemon configuration and runtime dependencies.
type controllerSettings struct {
	runtime, hostname, host, proxy string
	port                           int
}

// controllerSettings validates server configuration and builds runtime dependencies.
func (s *session) controllerSettings(stateDir string) (controllerSettings, error) {
	var e error
	var installed installedController
	// Reuse recorded installation settings when the installation manifest is readable.
	if data, e := os.ReadFile(filepath.Join(stateDir, "installation.json")); e == nil {
		json.Unmarshal(data, &installed)
	}
	runtime := defaultValue(s.flags["runtime"], defaultValue(installed.Runtime, "docker"))
	hostname := defaultValue(s.flags["hostname"], defaultValue(s.flags["controller-hostname"], defaultValue(installed.ControllerHostname, installed.Hostname)))
	host := defaultValue(s.flags["host"], "127.0.0.1")
	// Only a fleet agent with an explicit valid peer pin may bind outside loopback.
	if !allowedControllerBind(host, s.flags["controller-fingerprint"]) {
		return controllerSettings{}, model.Err("LISTENER", "Controller must bind loopback behind the selected reverse proxy")
	}
	port := 7463
	// Parse an explicitly configured port before constructing listener settings.
	if s.flags["port"] != "" {
		port, e = strconv.Atoi(s.flags["port"])
		// Reject this path: Invalid controller port.
		if e != nil || port < 1 || port > 65535 {
			return controllerSettings{}, model.Err("PORT", "Invalid controller port")
		}
	}
	proxy, err := s.selectedProxy(installed.Proxy)
	// Reject unsupported edge selections before constructing the runtime.
	if err != nil {
		return controllerSettings{}, err
	}

	return controllerSettings{runtime: runtime, hostname: hostname, host: host, proxy: proxy, port: port}, nil
}

// selectedProxy preserves the installation selection unless an explicit reviewed proxy was requested.
func (s *session) selectedProxy(installed string) (string, error) {
	proxy := defaultValue(s.flags["proxy"], defaultValue(installed, "caddy"))
	// Only the two supported reverse proxies may own the public edge.
	if proxy != "caddy" && proxy != "nginx" && proxy != "nginx-existing" && proxy != "nginx-gateway" {
		return "", model.Err("PROXY", "Supported proxies are caddy, nginx, nginx-existing and nginx-gateway")
	}
	return proxy, nil
}

// printAgentIdentity publishes only the host certificate pin needed for trusted enrollment.
func (s *session) printAgentIdentity(server *controller.Controller) error {
	// Apply this check before granting authority or advancing protocol state.
	if s.command == "agent" {
		pin, err := server.FleetFingerprint()
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err != nil {
			return err
		}
		s.print(map[string]string{"hostFingerprint": pin})
	}
	return nil
}

// loadRecoveryDestinations keeps independently recoverable encryption keys in owner-only operator files.
func (s *session) loadRecoveryDestinations() (map[string]*recovery.Destination, error) {
	path := defaultValue(s.flags["data-config"], os.Getenv("HOSTLIER_DATA_CONFIG"))
	// Advance only after the required recovery invariant holds.
	if path == "" {
		return nil, nil
	}
	return recovery.Load(path)
}

// controllerEmail loads optional enrollment transport without weakening recovery startup validation.
func (s *session) controllerEmail(hostname string) (*emailauth.Service, error) {
	path := defaultValue(s.flags["auth-config"], os.Getenv("HOSTLIER_AUTH_CONFIG"))
	// Advance only after the required recovery invariant holds.
	if path == "" {
		return nil, nil
	}
	return emailauth.Load(path, hostname)
}

// allowedControllerBind permits remote binding only when the listener will enforce a fleet peer pin.
func allowedControllerBind(host, pin string) bool {
	return fleet.ValidPin(pin) || host == "127.0.0.1" || host == "::1" || host == "localhost"
}
