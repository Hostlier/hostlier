package client

import (
	"net/http"
	"testing"
	"time"
)

// TestApprovalExecutionTimeout preserves bounded maintenance timeouts through the retained-intent endpoint.
func TestApprovalExecutionTimeout(t *testing.T) {
	client := &Client{HTTP: &http.Client{Timeout: 30 * time.Second}}
	// Exercise every case independently, including denied inputs.
	for _, path := range []string{"/v1/resources/db/backups", "/v1/approvals/request/execute"} {
		// Fail if the fixture violates this invariant: maintenance timeout lost.
		if client.requestTransport("POST", path).Timeout != 10*time.Minute {
			t.Fatal("maintenance timeout lost", path)
		}
	}
	// Fail if the fixture violates this invariant: ordinary request timeout changed.
	if client.requestTransport("GET", "/v1/status").Timeout != 30*time.Second {
		t.Fatal("ordinary request timeout changed")
	}
}
