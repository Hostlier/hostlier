package localui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"hostlier/internal/model"
)

// connectionIntent bounds public input to profile selection or first-run administrator-approved linking.
type connectionIntent struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// dashboardAPI serializes connection switches with proposals so approval IDs cannot cross controllers.
func (s *Server) dashboardAPI(r *http.Request) (any, error) {
	// Connection metadata reads reveal no credential material or private pairing secrets.
	if r.Method == "GET" && r.URL.Path == "/api/connection" {
		return s.connectionState()
	}
	// Active-page reads never acquire the connection/approval mutation lock.
	if r.Method == "GET" && !strings.HasPrefix(r.URL.Path, "/api/approvals/") {
		return s.readPage(r)
	}
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	// Mutating connection routes share the same exact-origin and local-session checks as action requests.
	if strings.HasPrefix(r.URL.Path, "/api/connection") {
		return s.connectionAPI(r)
	}
	return s.actionAPI(r)
}

// connectionState permits an injected fixed controller to render without offering profile changes.
func (s *Server) connectionState() (any, error) {
	manager, ok := s.remote.(*ManagedRemote)
	// Fixed test clients expose no synthetic saved profile or controller credential.
	if !ok {
		return ConnectionState{Profiles: []ConnectionProfile{}}, nil
	}
	return manager.State()
}

// connectionAPI keeps linking within existing controller enrollment and CLI profile contracts.
func (s *Server) connectionAPI(r *http.Request) (any, error) {
	manager, ok := s.remote.(*ManagedRemote)
	// A fixed injected client cannot be converted into an unrelated network authority.
	if !ok {
		return nil, model.Err("CONNECTION_FIXED", "This dashboard uses a fixed connection")
	}
	s.mu.Lock()
	pending := len(s.approvals) > 0
	s.mu.Unlock()
	// Pending exact approvals stay bound to the controller that retained their intent.
	if pending {
		return nil, model.Err("APPROVAL_PENDING", "Finish the pending action before changing controllers")
	}
	// Linking polls consume only a previously created, private CLI-owned enrollment request.
	if r.Method == "POST" && r.URL.Path == "/api/connection/poll" {
		return manager.PollLink(r.Context())
	}
	return s.applyConnection(r, manager)
}

// applyConnection handles only saved-profile selection and administrator-reviewed new pairing.
func (s *Server) applyConnection(r *http.Request, manager *ManagedRemote) (any, error) {
	var intent connectionIntent
	// Unknown methods and paths must never allocate a controller challenge.
	if r.Method != "POST" || (r.URL.Path != "/api/connection/link" && r.URL.Path != "/api/connection/select") {
		return nil, model.Err("NOT_FOUND", "Connection endpoint not found")
	}
	// Decode only the documented bounded fields before any network request.
	if err := decodeConnection(r, &intent); err != nil {
		return nil, err
	} // Reject hidden or oversized URL/name fields before network I/O.
	// Profile selection resolves an existing credential wholly inside the CLI process.
	if r.URL.Path == "/api/connection/select" {
		// Resolve a saved CLI profile before replacing its upstream stream.
		if err := manager.Select(intent.Name); err != nil {
			return nil, err
		}
		return manager.State()
	} // No enrollment occurs for saved profiles.
	return manager.BeginLink(r.Context(), intent.Name, intent.URL)
}

// decodeConnection requires one complete bounded object rather than an arbitrary proxy request.
func decodeConnection(r *http.Request, intent *connectionIntent) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	// No URL or name may cause unbounded local input buffering.
	if err != nil || len(data) > 4096 {
		return model.Err("INVALID_REQUEST", "Connection request exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	// Exact fields prevent browser-supplied credentials or transport overrides from entering the flow.
	if decoder.Decode(intent) != nil || decoder.Decode(new(any)) != io.EOF {
		return model.Err("INVALID_REQUEST", "Expected a controller profile name and HTTPS URL")
	}
	return nil
}
