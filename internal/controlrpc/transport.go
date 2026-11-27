package controlrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"

	"hostlier/internal/model"
)

// NewHTTPTransport shares verified HTTPS and Unix transport without importing CLI credentials or profiles.
func NewHTTPTransport(origin, socket, caFile string) (*http.Transport, string, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	if socket != "" {
		t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
		return t, "http://localhost", nil
	}
	base, err := model.ControllerURL(origin)
	if err != nil {
		return nil, "", err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if caFile == "" {
		caFile = os.Getenv("HOSTLIER_CA_FILE")
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, "", model.Err("CA_FILE", "Cannot read trusted CA file")
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, "", model.Err("CA_FILE", "Trusted CA file contains no certificates")
		}
	}
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	return t, base, nil
}
