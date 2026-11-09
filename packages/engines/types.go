package engines

import (
	"context"
	"hostlier/internal/model"
)

// Config contains public desired settings; connections are private aliases.
type Config struct {
	Mode      string `json:"mode"`
	Database  string `json:"database,omitempty"`
	Storage   string `json:"storage,omitempty"`
	KeyPrefix string `json:"keyPrefix,omitempty"`
}

// Package selects an optional engine implementation.
type Package struct{ Engine string }

// payload binds generated credentials to the reviewed request.
type payload struct {
	Fingerprint string
	Credentials model.ResourceCredentials
}

// nativeVerifier is the read-only authenticated verification capability of native managers.
type nativeVerifier interface {
	// VerifyWorkflow authenticates protected credentials against an owned live engine.
	VerifyWorkflow(context.Context, string, model.ResourceCredentials) error
}
