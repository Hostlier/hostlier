package model

import "context"

// DatabaseItem is a hashed Redis value with its absolute native expiration time.
type DatabaseItem struct {
	Key     string `json:"key"`
	Hash    string `json:"hash"`
	Expires int64  `json:"expires"`
}

// DatabaseEvidence retains native version and content checks without exposing row values.
type DatabaseEvidence struct {
	Engine  string         `json:"engine"`
	Version string         `json:"version"`
	Digest  string         `json:"digest"`
	Items   []DatabaseItem `json:"items,omitempty"`
}

// DatabaseRecoveryManager separates read verification and persistent network fencing from general lifecycle authority.
type DatabaseRecoveryManager interface {
	Evidence(context.Context, string, ResourceContext) (DatabaseEvidence, error)
	Fence(context.Context, string, bool, ResourceContext) error
}
