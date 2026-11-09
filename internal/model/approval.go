package model

import "strings"

// RequiresHumanApproval defines the shared fail-closed API mutation boundary.
func RequiresHumanApproval(method, path string) bool {
	path = "/" + strings.Trim(path, "/")
	// Host routing preserves the inner operation's approval boundary.
	if _, inner, ok := FleetPath(path); ok {
		return RequiresHumanApproval(method, inner)
	}
	// Exporting database contents requires explicit data-release authority.
	if method == "GET" {
		return strings.HasPrefix(path, "/v1/backups/") && strings.HasSuffix(path, "/export")
	}
	// These exact POST routes prepare work or implement separately verified ceremonies.
	preparation := map[string]bool{
		"/v1/access/pair/start": true, "/v1/access/pair/redeem": true, "/v1/access/pair/poll": true,
		"/v1/access/passkey/register/begin": true, "/v1/access/passkey/register/finish": true,
		"/v1/access/passkey/decision/begin": true, "/v1/access/passkey/decision/finish": true,
		"/v1/access/passkey/pair/begin": true, "/v1/access/passkey/pair/finish": true,
		"/v1/email/begin": true, "/v1/email/verify": true,
		"/v1/plans": true, "/v1/resource-plans": true, "/v1/planning": true,
		"/v1/pairing/redeem": true, "/v1/pairing/requests": true, "/v1/pairing/poll": true,
	}
	// Preparation is inert; OAuth completion separately verifies the prior owner-approved attempt.
	if method == "POST" && (preparation[path] || fusedOAuthCompletion(path)) {
		return false
	}
	// Approval routes verify their own identities and never accept bearer assertions as proof.
	if strings.HasPrefix(path, "/v1/approvals") || strings.HasPrefix(path, "/v1/human/") {
		return false
	}
	// Immutable artifact staging cannot execute or activate the uploaded payload.
	if inertUpload(method, path) {
		return false
	}
	return true
}

// packageUploadPath restricts preparation to one exact versioned chunk route.
func packageUploadPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) == 4 && parts[0] == "v1" && parts[1] == "package-uploads" && parts[3] == "chunks"
}

// inertUpload admits only nonexecuting content-staging routes before installation approval.
func inertUpload(method, path string) bool {
	return method == "POST" && (strings.HasPrefix(path, "/v1/artifacts/") || packageUploadPath(path))
}

// fusedOAuthCompletion admits only authenticated code relay, not connection creation or token issuance.
func fusedOAuthCompletion(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) == 4 && parts[0] == "v1" && parts[1] == "fused" && parts[3] == "complete"
}
