package model

import "context"

// SSHAccessRequest authorizes only a public key, never a caller-selected Linux identity.
type SSHAccessRequest struct {
	PublicKey string `json:"publicKey"`
}

// SSHAccess describes the dedicated unprivileged account and verified server key.
type SSHAccess struct {
	User        string `json:"user"`
	Port        int    `json:"port"`
	HostKey     string `json:"hostKey"`
	Fingerprint string `json:"fingerprint"`
}

// ControllerSSHAccess is supplied only by the installed privileged server executable.
type ControllerSSHAccess interface {
	Grant(context.Context, string, SSHAccessRequest) (SSHAccess, error)
	Revoke(context.Context, string) error
}
