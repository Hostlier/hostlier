package controller

import (
	"time"
)

// Client persists a client identity and credential verifier.
type Client struct {
	DeploymentGrant     string `json:"deploymentGrant,omitempty"`
	PublicKey           string `json:"publicKey,omitempty"`
	EnrollmentEmailHash string `json:"enrollmentEmailHash,omitempty"`
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Verifier            string `json:"verifier,omitempty"`
	Revoked             bool   `json:"revoked"`
}

// Enrollment tracks an expiring one-time enrollment challenge.
type Enrollment struct {
	ID, Name, Verifier, CodeHash, Status string
	ExpiresAt                            int64
	Consumed, Cancelled                  bool
}

// rate tracks a bounded enrollment request window.
type rate struct {
	start time.Time
	count int
}
