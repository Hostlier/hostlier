package localui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// actionIntent limits dashboard controls to explicit reversible application lifecycle requests.
type actionIntent struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

// approvalView carries only a review locator and the controller's verified receipt status.
type approvalView struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expiresAt"`
}

// actionAPI exposes proposal, observation and exact-request execution through independent passkey approval.
func (s *Server) actionAPI(r *http.Request) (any, error) {
	// Dashboard actions always create a retained controller proposal; they never call a mutation directly.
	if r.Method == "POST" && r.URL.Path == "/api/actions" {
		return s.proposeAction(r)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Do not expose a generic proxy or allow arbitrary controller path construction.
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "api" || parts[1] != "approvals" {
		return nil, model.Err("NOT_FOUND", "Dashboard endpoint not found")
	}
	s.mu.Lock()
	known := s.approvals[parts[2]]
	s.mu.Unlock()
	// Only proposals created through this local session can be inspected or executed here.
	if !known {
		return nil, model.Err("NOT_FOUND", "Approval not found in this dashboard session")
	}
	path := "/v1/approvals/" + url.PathEscape(parts[2])
	return s.approvalAPI(r, path, parts)
}

// approvalAPI preserves the controller's distinction between observing and executing an approval.
func (s *Server) approvalAPI(r *http.Request, path string, parts []string) (any, error) {
	// Observation cannot consume or execute an approved request.
	if r.Method == "GET" && len(parts) == 3 {
		var view approvalView
		err := s.remote.Request(r.Context(), "GET", path, nil, &view)
		return view, err
	}
	// The controller independently verifies passkey authority, generation and exact retained intent.
	if r.Method == "POST" && len(parts) == 4 && parts[3] == "execute" {
		var result any
		err := s.remote.Request(r.Context(), "POST", path+"/execute", map[string]any{}, &result)
		// A successful receipt releases controller-switch protection.
		if err == nil {
			s.mu.Lock()
			delete(s.approvals, parts[2])
			s.mu.Unlock()
		}
		return result, err
	}
	return nil, model.Err("NOT_FOUND", "Dashboard endpoint not found")
}

// proposeAction validates bounded intent and reserves capacity before contacting the controller.
func (s *Server) proposeAction(r *http.Request) (any, error) {
	intent, err := decodeActionIntent(r)
	// Invalid or oversized input cannot acquire a retained approval proposal.
	if err != nil {
		return nil, err
	}
	reservation := client.RequestID()
	// Reserve before network work so concurrent browser requests cannot exceed the session bound.
	if !s.reserveApproval(reservation) {
		return nil, model.Err("APPROVAL_LIMIT", "Restart the dashboard after inspecting its retained approvals")
	}
	var view approvalView
	request := map[string]any{"method": "POST", "path": "/v1/apps/" + url.PathEscape(intent.Name) + "/action", "body": map[string]any{"action": intent.Action, "purge": false}}
	err = s.remote.Request(r.Context(), "POST", "/v1/approvals", request, &view)
	s.mu.Lock()
	delete(s.approvals, reservation)
	// Store only canonical response identifiers safe for the narrow follow-up routes.
	if err == nil && validApprovalView(view) {
		s.approvals[view.ID] = true
	} else if err == nil { // A malformed successful receipt cannot become local execution authority.
		err = model.Err("EVENT_PROTOCOL", "Invalid approval receipt")
	}
	s.mu.Unlock()
	return view, err
}

// decodeActionIntent verifies the entire bounded body before recording visible browser intent.
func decodeActionIntent(r *http.Request) (actionIntent, error) {
	var intent actionIntent
	data, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	// Whitespace cannot hide a second payload beyond the decoding limit.
	if err != nil || len(data) > 4096 {
		return intent, model.Err("INVALID_REQUEST", "Application action exceeds request limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	// Trailing content and unknown fields cannot silently change the visible action.
	if decoder.Decode(&intent) != nil || decoder.Decode(new(any)) != io.EOF {
		return intent, model.Err("INVALID_REQUEST", "Expected one application action")
	}
	// Limit controls to start, stop and restart under the existing controller approval workflow.
	if !validActionIntent(intent) {
		return intent, model.Err("INVALID_ACTION", "Choose an application and start, stop or restart")
	}
	return intent, nil
}

// reserveApproval prevents unbounded proposal tracking without holding a lock across network work.
func (s *Server) reserveApproval(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Retained session proposals are bounded even with concurrent action requests.
	if len(s.approvals) >= 64 {
		return false
	}
	s.approvals[id] = true
	return true
}

// validActionIntent keeps names canonical before constructing a controller path.
func validActionIntent(intent actionIntent) bool {
	// Actions have no arbitrary command, purge flag or provider payload.
	if intent.Action != "start" && intent.Action != "stop" && intent.Action != "restart" {
		return false
	}
	// Names are a single bounded path segment, never a URL or encoded route.
	if intent.Name == "" || len(intent.Name) > 200 || strings.ContainsAny(intent.Name, "/%?#\\\x00\r\n") || intent.Name == "." || intent.Name == ".." {
		return false
	}
	return true
}

// validApprovalView accepts a bounded terminal locator without accepting controller-supplied commands or links.
func validApprovalView(view approvalView) bool {
	// Fail closed when the required identity, scope or format does not match.
	if len(view.Code) != 16 {
		return false
	}
	// Validate each entry before accepting the complete collection.
	for _, char := range view.Code {
		// Fail closed when the required identity, scope or format does not match.
		if !(char >= '0' && char <= '9') && !(char >= 'A' && char <= 'F') {
			return false
		}
	}
	return view.ID != "" && len(view.ID) <= 128 && !strings.ContainsAny(view.ID, "/%?#\\\x00\r\n")
}
