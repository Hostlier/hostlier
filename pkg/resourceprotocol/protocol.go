// Package resourceprotocol defines the private bounded resource package transport.
package resourceprotocol

import "hostlier/internal/model"

const MaxBackupBytes = 64 << 20
const MaxWireBytes = 90 << 20

// Backend contains administrator-selected runtime settings, never project inputs.
type Backend struct{ Type, StateDir, SocketPath, Kubeconfig string }

// Request carries one operation through an anonymous private pipe.
type Request struct {
	Version                   int                `json:"version"`
	Operation                 string             `json:"operation"`
	Backend                   Backend            `json:"backend"`
	Spec                      model.ResourceSpec `json:"spec"`
	Name, Action, OperationID string
	Credentials               model.ResourceCredentials `json:"credentials"`
	Fenced                    bool                      `json:"fenced,omitempty"`
	Purge                     bool                      `json:"purge"`
	Data                      []byte                    `json:"data,omitempty"`
}

// Response carries one bounded result without process diagnostics.
type Response struct {
	Evidence   model.DatabaseEvidence `json:"evidence,omitempty"`
	Events     []model.Event          `json:"events,omitempty"`
	Version    int                    `json:"version"`
	Plan       model.ResourcePlan     `json:"plan,omitempty"`
	Resource   model.Resource         `json:"resource,omitempty"`
	Connection map[string]string      `json:"connection,omitempty"`
	Logs       string                 `json:"logs,omitempty"`
	Data       []byte                 `json:"data,omitempty"`
	Error      *model.Error           `json:"error,omitempty"`
}
