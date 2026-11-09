package packagekit

import "encoding/json"

// InitRequest is sent to a local package's init entry point in the selected directory.
// Hooks propose new files on stdout. Hostlier handles dry runs, validation, and writing.
type InitRequest struct {
	Config       map[string]any `json:"config,omitempty"`
	UI           bool           `json:"ui,omitempty"`
	Version      int            `json:"version"`
	Name         string         `json:"name,omitempty"`
	DefaultName  string         `json:"defaultName"`
	Route        string         `json:"route,omitempty"`
	Port         int            `json:"port,omitempty"`
	Architecture string         `json:"architecture,omitempty"`
}

type InitResponse struct {
	Version int               `json:"version"`
	Project json.RawMessage   `json:"project,omitempty"`
	Files   map[string]string `json:"files,omitempty"`
	// Error is a public author-facing diagnostic. Never include credentials or config contents.
	Error string `json:"error,omitempty"`
}
