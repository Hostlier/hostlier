package cli

import (
	"context"
	"hostlier/internal/fleetjoin"
	"hostlier/internal/model"
	"strings"
)

// joinMaster preserves the CLI spelling while sharing the controller-only handshake.
func (s *session) joinMaster() error {
	if len(s.parsed.args) != 3 {
		return model.Err("ARGUMENTS", "Usage: hostlier server join HTTPS_MASTER [--name NAME] [--socket PATH]")
	}
	args := []string{s.target}
	for flag, value := range s.flags {
		switch flag {
		case "name", "socket", "ca-file":
			args = append(args, "--"+flag, value)
		default:
			return model.Err("ARGUMENTS", "Unsupported join option --"+flag)
		}
	}
	return fleetjoin.RunConnect(s.ctx, args, s.out, s.errOut)
}

// joinApprovalClient retains the controller's normal grant and receipt protocol.
type joinApprovalClient struct{ session *session }

func (c joinApprovalClient) Request(_ context.Context, method, path string, body, result any) error {
	return c.session.request(method, path, body, result)
}

// approveServer uses a saved signed login with servers.add authority; it never opens a browser.
func (s *session) approveServer() error {
	if len(s.parsed.args) != 3 {
		return model.Err("ARGUMENTS", "Usage: hostlier server approve CODE [--server PROFILE]")
	}
	for flag := range s.flags {
		if flag != "server" {
			return model.Err("ARGUMENTS", "Unsupported server approval option --"+flag)
		}
	}
	if !s.interactive {
		return model.Err("INTERACTIVE_REQUIRED", "Review controller enrollment from an interactive terminal")
	}
	if err := s.connectRemote(); err != nil {
		return err
	}
	return fleetjoin.Approve(s.ctx, joinApprovalClient{s}, strings.ToUpper(s.target), s.in, s.out)
}
