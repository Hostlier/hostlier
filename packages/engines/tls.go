package engines

import (
	"crypto/tls"
	"crypto/x509"
	"errors"

	"hostlier/pkg/workflow"
)

// privateAlias resolves one protected input without allowing competing authorities.
func privateAlias(req workflow.Request, name string) (string, error) {
	secret, input := req.Secrets[name], req.Inputs[name]
	// Two sources for the same trust root would make reviews ambiguous.
	if secret != "" && input != "" {
		return "", errors.New("ambiguous private engine alias")
	}
	if secret != "" { // Resolve exactly one private authority for this engine alias.
		return secret, nil
	}
	return input, nil
}

// trustedTLS attaches an optional private provider CA while preserving hostname checks.
func trustedTLS(base *tls.Config, req workflow.Request) (*tls.Config, error) {
	// No TLS or deliberately weakened driver configuration is ever accepted.
	if base == nil || base.InsecureSkipVerify {
		return nil, errors.New("verified engine TLS required")
	}
	ca, err := privateAlias(req, "caCertificatePEM")
	if err != nil { // Require successful completion before using the returned engine state.
		return nil, err
	}
	config := base.Clone()
	config.MinVersion = tls.VersionTLS12
	// The operating system trust store is sufficient for ordinary public endpoints.
	if ca == "" {
		return config, nil
	}
	if len(ca) > 65536 { // Reject the operation: engine CA certificate exceeds limit.
		return nil, errors.New("engine CA certificate exceeds limit")
	}
	roots, err := x509.SystemCertPool()
	if err != nil { // Require successful completion before using the returned engine state.
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM([]byte(ca)) { // Reject the operation: invalid engine CA certificate.
		return nil, errors.New("invalid engine CA certificate")
	}
	config.RootCAs = roots
	return config, nil
}

// exportTrust returns the same private trust representation applications need for their connection.
func exportTrust(outputs map[string]string, req workflow.Request) {
	ca, _ := privateAlias(req, "caCertificatePEM")
	if ca != "" { // Propagate the private provider authority when applications need it.
		outputs["caCertificatePEM"] = ca
	}
}
