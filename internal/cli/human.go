package cli

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"hostlier/internal/model"
)

// approvalStatus contains the nonsecret receipt needed to review or resume a pending mutation.
type approvalStatus struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expiresAt"`
}

// requestHumanApproval submits immutable intent and waits for the independently authenticated human.
func (s *session) requestHumanApproval(method, path string, body, result any) error {
	var approval approvalStatus
	request := map[string]any{"method": method, "path": path, "body": body}
	// A controller without this protocol cannot silently fall back to bearer-approved execution.
	if err := s.remote.Request(s.ctx, "POST", "/v1/approvals", request, &approval); err != nil {
		return err
	}
	// Keep the final operation output separate from the human-facing review locator.
	if approval.Status == "pending" {
		// Propagate failure rather than continuing with incomplete or unverified state.
		if _, err := fmt.Fprintf(s.errOut, "Human approval request: %s\nBrowser: %s/approve?request=%s\nOwner: hostlier approve %s --key KEY_FILE\nResume: hostlier approval %s --watch\n", approval.Code, s.serverTarget, approval.ID, approval.Code, approval.ID); err != nil {
			return err
		}
	}
	// Advance only from the expected retained protocol state.
	if approval.Status == "pending" && s.flags["key"] != "" {
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err := s.signHumanApproval(approval.ID); err != nil {
			return err
		}
	}
	return s.awaitHumanApproval(approval, result)
}

// awaitHumanApproval executes only the stored request after the server verifies a hardware-key signature.
func (s *session) awaitHumanApproval(approval approvalStatus, result any) error {
	path := "/v1/approvals/" + url.PathEscape(approval.ID)
	// Polling never changes approval or substitutes a new request payload.
	for {
		// Only verified approval or a completed idempotent receipt permits execution retrieval.
		if approval.Status == "approved" || approval.Status == "completed" {
			err := s.remote.Request(s.ctx, "POST", path+"/execute", map[string]any{}, result)
			// Apply this check before granting authority or advancing protocol state.
			if planningErrorCode(err) == "CONNECTION" {
				return s.remote.Request(s.ctx, "POST", path+"/execute", map[string]any{}, result)
			}
			return err
		}
		// Rejection, uncertain execution and expiration require explicit investigation or new review.
		if approval.Status != "pending" || approval.ExpiresAt <= time.Now().UnixMilli() {
			return model.Err("APPROVAL_UNAVAILABLE", "Approval is "+approval.Status+" or expired; inspect it before submitting new work")
		}
		// Ctrl-C stops waiting; it neither approves nor executes pending work.
		if err := s.pause(); err != nil {
			return err
		}
		// The original API identity is required on every poll.
		if err := s.remote.Request(s.ctx, "GET", path, nil, &approval); err != nil {
			return err
		}
	}
}

// approval inspects a pending request or resumes observation and exact-request execution.
func (s *session) approval() error {
	// Requiring an explicit identifier avoids accidentally approving or executing another request.
	if len(s.parsed.args) != 2 {
		return model.Err("ARGUMENTS", "Usage: hostlier approval ID [--watch]")
	}
	path := "/v1/approvals/" + url.PathEscape(s.sub)
	// Inspection alone never executes approved work.
	if s.flags["watch"] == "" {
		return s.getPrint(path)
	}
	var status approvalStatus
	// Resume with fresh server status rather than a cached client assertion.
	if err := s.remote.Request(s.ctx, "GET", path, nil, &status); err != nil {
		return err
	}
	var result any
	// The server decides whether this identity may execute the retained request.
	if err := s.awaitHumanApproval(status, &result); err != nil {
		return err
	}
	return s.print(result)
}

// security manages hardware public keys through explicit local administration.
func (s *session) security() error {
	// Apply this check before granting authority or advancing protocol state.
	if s.sub == "status" {
		return s.getPrint("/v1/human/status")
	}
	// Reject the request before side effects: Key enrollment and recovery require an explicit local --socket.
	if s.flags["socket"] == "" {
		return model.Err("ARGUMENTS", "Key enrollment and recovery require an explicit local --socket")
	}
	var body any
	// Keep distinct protocol outcomes on explicit execution paths.
	switch s.sub {
	// Bootstrap a browser passkey with explicit server-administrator authority.
	case "passkey":
		return s.passkeyEnrollment()
	// Handle only this explicit protocol outcome.
	case "enroll":
		// Reject the request before side effects: Usage: security enroll --public-key FILE --name NAME --socket PATH.
		if s.flags["public-key"] == "" || s.flags["name"] == "" {
			return model.Err("ARGUMENTS", "Usage: security enroll --public-key FILE --name NAME --socket PATH")
		}
		public, err := os.ReadFile(s.flags["public-key"])
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err != nil {
			return err
		}
		body = map[string]string{"name": s.flags["name"], "publicKey": string(public)}
	// Handle only this explicit protocol outcome.
	case "revoke":
		body = map[string]string{"fingerprint": s.flags["fingerprint"]}
	// Handle only this explicit protocol outcome.
	default:
		return model.Err("ARGUMENTS", "Usage: security status|enroll|revoke; security keygen --key FILE")
	}
	var result any
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := s.request("POST", "/v1/human/"+s.sub, body, &result); err != nil {
		return err
	}
	return s.print(result)
}
