package controllercmd

import (
	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// runAgent starts a host executor whose network authority is limited to one pinned controller.
func (s *session) runAgent() error {
	// Reject the request before side effects: Usage: agent run --controller-fingerprint SHA256 --host ADDRESS --port PORT [--state-dir DIR --config FILE --runtime docker|k3s].
	if s.sub != "run" || len(s.parsed.args) != 2 || !fleet.ValidPin(s.flags["controller-fingerprint"]) {
		return model.Err("ARGUMENTS", "Usage: agent run --controller-fingerprint SHA256 --host ADDRESS --port PORT [--state-dir DIR --config FILE --runtime docker|k3s]")
	}
	return s.runController()
}
