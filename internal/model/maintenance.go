package model

import (
	"context"
	"encoding/json"
)

// ControllerUpdatePlan is the public, actor-bound review of one verified release.
type ControllerUpdatePlan struct {
	ID                      string   `json:"id"`
	Fingerprint             string   `json:"fingerprint"`
	Version                 string   `json:"version"`
	Architecture            string   `json:"architecture"`
	BundleSHA256            string   `json:"bundleSha256"`
	Runtime                 string   `json:"runtime"`
	Proxy                   string   `json:"proxy"`
	ControllerHostname      string   `json:"controllerHostname"`
	InstallationFingerprint string   `json:"installationFingerprint"`
	Changes                 []string `json:"changes"`
	Warnings                []string `json:"warnings"`
	Errors                  []string `json:"errors"`
	ExpiresAt               int64    `json:"expiresAt"`
}

// ControllerUpdateRequest binds execution and retries to the exact reviewed plan.
type ControllerUpdateRequest struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	RequestID   string `json:"requestId"`
}

// ControllerUpdateJob survives the controller restart independently of its process.
type ControllerUpdateJob struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Step    string `json:"step,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// ControllerMaintenance supplies privileged local operations without coupling the
// controller API to offline migration or release-installer implementation packages.
type ControllerMaintenance interface {
	PreviewMigration(context.Context, string) (json.RawMessage, error)
	PreviewUpdate(context.Context, string, int64) (ControllerUpdatePlan, error)
	ReviewUpdate(context.Context, string, int64, ControllerUpdateRequest) (ControllerUpdatePlan, error)
	StartUpdate(context.Context, string, int64, ControllerUpdateRequest) (ControllerUpdateJob, error)
	UpdateStatus(context.Context, string, string) (ControllerUpdateJob, error)
}
