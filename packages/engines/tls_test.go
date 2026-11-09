package engines

import (
	"crypto/tls"
	"testing"

	"hostlier/pkg/workflow"
)

// TestProviderTrustBoundary requires valid private authorities and preserves strict certificate verification.
func TestProviderTrustBoundary(t *testing.T) {
	req := workflow.Request{Inputs: map[string]string{"caCertificatePEM": dockerCertificates(t).ca}}
	config, err := trustedTLS(&tls.Config{ServerName: "localhost"}, req)
	// A supplied CA must become a trust root without weakening hostname validation.
	if err != nil || config.RootCAs == nil || config.InsecureSkipVerify || config.ServerName != "localhost" {
		t.Fatal("invalid provider trust configuration")
	}
	req.Inputs["caCertificatePEM"] = "not a certificate"
	// Malformed input cannot silently fall back to ordinary system trust.
	if _, err = trustedTLS(&tls.Config{}, req); err == nil {
		t.Fatal("invalid certificate accepted")
	}
	req.Secrets = map[string]string{"caCertificatePEM": "competing authority"}
	// Two private sources cannot compete for trust configuration.
	if _, err = trustedTLS(&tls.Config{}, req); err == nil {
		t.Fatal("ambiguous CA accepted")
	}
}
