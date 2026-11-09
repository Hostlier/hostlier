package cli

import (
	"fmt"
	"net"
	"strconv"

	"hostlier/internal/localui"
	"hostlier/internal/model"
)

// ui opens locally before pairing and shares the selected controller through one observer.
func (s *session) ui() error {
	// Binding remote interfaces would expose the local browser session beyond its intended boundary.
	if len(s.parsed.args) != 1 || s.flags["host"] != "" || s.flags["socket"] != "" {
		return model.Err("ARGUMENTS", "Usage: hostlier ui [--server NAME] [--port PORT]; link or select a controller in the dashboard")
	}
	// Injected clients remain available for command-level integration tests.
	if s.options.Client != nil {
		s.remote = s.options.Client
	} else {
		managed, err := localui.NewManagedRemote(s.store, s.flags["server"])
		// Unreadable or explicitly unknown profiles must not silently select another controller.
		if err != nil {
			return err
		}
		s.remote = managed
	}
	remote, ok := s.remote.(localui.Remote)
	// A controller without status streaming must be upgraded instead of silently adding polling load.
	if !ok {
		return model.Err("STATUS_UNSUPPORTED", "Selected client does not support status streaming")
	}
	port, err := strconv.Atoi(defaultValue(s.flags["port"], "0"))
	// Zero chooses an available loopback port and explicit ports must be valid TCP ports.
	if err != nil || port < 0 || port > 65535 {
		return model.Err("ARGUMENTS", "UI port must be between 0 and 65535")
	}
	return s.serveUI(remote, port)
}

// serveUI owns listener startup and teardown separately from command validation.
func (s *session) serveUI(remote localui.Remote, port int) error {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	// Listener failure must not start an upstream observation worker.
	if err != nil {
		return model.Err("UI_LISTEN", "Cannot bind local dashboard port")
	}
	defer listener.Close()
	server, err := localui.New(remote, "http://"+listener.Addr().String())
	// Randomness failure closes the listener without accepting an unauthenticated session.
	if err != nil {
		return err
	}
	// The printed fragment authorizes this local UI only; it contains no controller credential.
	if _, err = fmt.Fprintln(s.out, server.URL()); err != nil {
		return err
	}
	return server.Serve(s.ctx, listener)
}
