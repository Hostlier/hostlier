package cli

import (
	"encoding/json"
	"hostlier/internal/securitykey"
	"testing"
	"time"
)

// TestSigningIntentBindsReviewAndAudience rejects challenge substitution before invoking OpenSSH.
func TestSigningIntentBindsReviewAndAudience(t *testing.T) {
	fields := map[string]any{"protocol": securitykey.Namespace, "controllerHost": "controller.example.com", "requestId": "request", "fingerprint": "intent", "decision": "approve", "keyFingerprint": "key", "expiresAt": time.Now().Add(time.Minute).UnixMilli()}
	data, _ := json.Marshal(fields)
	// A matching TLS hostname and reviewed intent are required together.
	if err := checkSigningIntent(string(data), "request", "intent", "key", "https://controller.example.com"); err != nil {
		t.Fatal(err)
	}
	// Every independently meaningful field must remain bound to the displayed review.
	for _, field := range []string{"protocol", "controllerHost", "requestId", "fingerprint", "decision", "keyFingerprint", "expiresAt"} {
		original := fields[field]
		fields[field] = "substituted"
		data, _ = json.Marshal(fields)
		// Tampering must fail before a hardware operation can start.
		if checkSigningIntent(string(data), "request", "intent", "key", "https://controller.example.com") == nil {
			t.Fatal("accepted altered", field)
		}
		fields[field] = original
	}
	// An attacker-controlled HTTPS origin cannot relay the legitimate controller's challenge.
	if checkSigningAudience("controller.example.com", "https://attacker.example.com") == nil {
		t.Fatal("cross-controller challenge accepted")
	}
}
