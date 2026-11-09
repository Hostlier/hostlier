// Package fleet implements explicitly pinned, mutually authenticated host transport.
package fleet

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"time"
)

// Identity contains a public certificate and its private signing key.
type Identity struct {
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"privateKey"`
}

// Generate creates an independent transport identity with a bounded lifetime.
func Generate() (Identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return Identity{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return Identity{}, err
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Hostlier fleet transport"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return Identity{}, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return Identity{}, err
	}
	return Identity{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))}, nil
}

// Fingerprint names the exact leaf certificate that the operator must enroll.
func Fingerprint(certificate string) (string, error) {
	block, _ := pem.Decode([]byte(certificate))
	// Apply this check before granting authority or advancing protocol state.
	if block == nil {
		return "", errors.New("missing fleet certificate")
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return "", err
	}
	digest := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(digest[:]), nil
}

// ValidPin rejects malformed pins before any host connection is attempted.
func ValidPin(pin string) bool {
	decoded, err := hex.DecodeString(pin)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == pin
}

// verifyPeer checks exact identity, validity and role; TLS itself verifies private-key possession.
func verifyPeer(raw [][]byte, pin string, usage x509.ExtKeyUsage) error {
	// Apply this check before granting authority or advancing protocol state.
	if len(raw) == 0 {
		return errors.New("fleet peer certificate required")
	}
	sum := sha256.Sum256(raw[0])
	// Apply this check before granting authority or advancing protocol state.
	if hex.EncodeToString(sum[:]) != pin {
		return errors.New("fleet peer identity mismatch")
	}
	cert, err := x509.ParseCertificate(raw[0])
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	now := time.Now()
	// Apply this check before granting authority or advancing protocol state.
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return errors.New("fleet peer certificate expired or not yet valid")
	}
	// Validate each entry before accepting the complete collection.
	for _, purpose := range cert.ExtKeyUsage {
		// Accept the certificate only for the expected client or server role.
		if purpose == usage {
			return nil
		}
	}
	return errors.New("fleet peer certificate has the wrong role")
}

// TLS configures strict pin verification independently of public certificate authorities.
func TLS(identity Identity, peerPin string, server bool) (*tls.Config, error) {
	// Stop before this invalid or unavailable state can reach the next protocol step.
	if !ValidPin(peerPin) {
		return nil, errors.New("a SHA-256 fleet peer fingerprint is required")
	}
	cert, err := tls.X509KeyPair([]byte(identity.Certificate), []byte(identity.PrivateKey))
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
	usage := x509.ExtKeyUsageServerAuth
	// Apply this check before granting authority or advancing protocol state.
	if server {
		cfg.ClientAuth = tls.RequireAnyClientCert
		usage = x509.ExtKeyUsageClientAuth
	} else {
		cfg.InsecureSkipVerify = true
	} // Exact pin and validity verification below replace public-CA verification.
	cfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error { return verifyPeer(raw, peerPin, usage) }
	return cfg, nil
}
