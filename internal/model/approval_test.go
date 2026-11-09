package model

import "testing"

// TestApprovalBoundaryCanonicalPaths prevents trailing-slash router aliases from bypassing sensitive reads.
func TestApprovalBoundaryCanonicalPaths(t *testing.T) {
	// Exercise every case independently, including denied inputs.
	for _, path := range []string{"/v1/backups/backup/export", "/v1/backups/backup/export/", "///v1/backups/backup/export///", "/v1/hosts/host/proxy/backups/backup/export/"} {
		// Fail if the fixture violates this invariant: unprotected export.
		if !RequiresHumanApproval("GET", path) {
			t.Fatal("unprotected export", path)
		}
	}
	// Exercise every case independently, including denied inputs.
	for _, path := range []string{"/v1/secrets/key", "/v1/authorizations", "/v1/plans/plan/apply", "/v1/hosts/host/proxy/plans/plan/apply"} {
		// Fail if the fixture violates this invariant: unprotected mutation.
		if !RequiresHumanApproval("POST", path) {
			t.Fatal("unprotected mutation", path)
		}
	}
}
