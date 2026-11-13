package cli

import (
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// validate checks a project file before contacting a controller.
func (s *session) validate() error {
	var e error

	spec, e := s.readProjectInput("")
	// Reject invalid project inputs before planning any runtime work.
	if e != nil {
		return e
	}
	// Local file preparation is read-only and uses the same deployment source boundary.
	spec, e = s.prepareConfigFiles(spec)
	// Missing configuration files invalidate an offline validation too.
	if e != nil {
		return e
	}
	// Structural validation understands native artifacts without registry access.
	if e = config.ValidateInput(spec); e != nil {
		return e
	}
	// Local artifacts are inspected without running their code.
	if e = s.validateLocalArtifacts(spec); e != nil {
		return e
	}
	return s.print(map[string]any{"valid": true, "name": spec.Name})
}

// server manages local profiles and bootstraps fleet members.
func (s *session) server() error {

	// Select a local profile-management action.
	switch s.sub {
	case "ssh":
		return s.serverSSH()
	case "join":
		return s.joinMaster()
	case "add":
		return s.addServer()
	case "list":
		return s.listServers()
	case "use":
		return s.useServer()

	}
	return model.Err("ARGUMENTS", "Usage: server add USER@HOST | server list | server use NAME")
}

// disconnect removes the requested local connection profile.
func (s *session) disconnect() error {

	name := s.sub
	// Use the --server selection when no positional profile name was supplied.
	if name == "" {
		name = s.flags["server"]
	}
	// Report profile removal failures before claiming disconnection.
	if e := s.store.Remove(name); e != nil {
		return e
	}
	return s.print(map[string]bool{"disconnected": true})
}

// enrollmentCredential is the one-time client credential returned by either pairing flow.
type enrollmentCredential struct {
	ClientID string `json:"clientId"`
	Token    string `json:"token"`
	Status   string `json:"status"`
}

// enrollmentRequest bounds the recognized JSON fields for this API action.
type enrollmentRequest struct {
	RequestID     string `json:"requestId"`
	Code          string `json:"code"`
	RequestSecret string `json:"requestSecret"`
	ExpiresAt     int64  `json:"expiresAt"`
}

// requestEnrollment prints the public approval challenge and keeps its polling secret private.
func (s *session) requestEnrollment(remote Requester, name string) (enrollmentCredential, error) {
	// Reject this path: --request conflicts with --token-stdin.
	if s.flags["token-stdin"] != "" {
		return enrollmentCredential{}, model.Err("ARGUMENTS", "--request conflicts with --token-stdin")
	}
	var pending enrollmentRequest
	// Create the public enrollment challenge before starting private polling.
	if err := remote.Request(s.ctx, "POST", "/v1/pairing/requests", map[string]string{"name": name}, &pending); err != nil {
		return enrollmentCredential{}, err
	}
	s.print(map[string]any{"approvalCode": pending.Code, "expiresAt": pending.ExpiresAt, "authority": "Trusted server administrator. Approve this code with hostlier pairing approve."})
	return s.pollEnrollment(remote, pending)
}

// pollEnrollment waits for enrollment approval while honoring expiry and cancellation.
func (s *session) pollEnrollment(remote Requester, pending enrollmentRequest) (enrollmentCredential, error) {
	var credential enrollmentCredential
	// Stop enrollment polling at the challenge expiry deadline.
	for time.Now().UnixMilli() < pending.ExpiresAt {
		// Honor cancellation while waiting for the next polling interval.
		if err := s.pause(); err != nil {
			return credential, err
		}
		// Require an authenticated polling response before accepting enrollment progress.
		if err := remote.Request(s.ctx, "POST", "/v1/pairing/poll", map[string]string{"requestId": pending.RequestID, "requestSecret": pending.RequestSecret}, &credential); err != nil {
			return credential, err
		}
		// Return the credential as soon as approval has completed.
		if credential.Token != "" {
			return credential, nil
		}
		// Stop polling after an explicit cancellation, denial or expiry.
		if credential.Status == "cancelled" || credential.Status == "denied" || credential.Status == "expired" {
			return credential, model.Err("ENROLLMENT", "Enrollment "+credential.Status)
		}
	}
	return credential, model.Err("ENROLLMENT", "Enrollment expired")
}
