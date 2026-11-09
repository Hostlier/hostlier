package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// collectorCertificate creates a role-specific certificate signed by the private collector issuer.
func collectorCertificate(host string, issuer *x509.Certificate, issuerKey *ecdsa.PrivateKey, ca bool, client bool, now time.Time) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return "", "", err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Hostlier collector transport"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
	// Apply this transport-specific case without changing fleet management authority.
	if ca {
		cert.IsCA = true
		cert.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		cert.NotAfter = now.Add(10 * 365 * 24 * time.Hour)
		issuer = cert
		issuerKey = key
	} else if client { // Apply this transport-specific case without changing fleet management authority.
		cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	} else {
		cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		setCollectorSAN(cert, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, issuer, &key.PublicKey, issuerKey)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return "", "", err
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})), nil
}

// setCollectorSAN uses standard hostname verification for both IP and DNS origins.
func setCollectorSAN(cert *x509.Certificate, host string) {
	// Apply this transport-specific case without changing fleet management authority.
	if ip := net.ParseIP(host); ip != nil {
		cert.IPAddresses = []net.IP{ip}
	} else {
		cert.DNSNames = []string{host}
	}
}

// collectorIssuer checks the persisted issuer and returns the signing key without publishing it.
func collectorIssuer(identity *collectorTransportIdentity, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	pair, err := tls.X509KeyPair([]byte(identity.CA), []byte(identity.CAKey))
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return nil, nil, errors.New("invalid collector issuer")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return nil, nil, err
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	// Fail closed when the transport prerequisite or credential validation fails.
	if !ok || !cert.IsCA || now.Before(cert.NotBefore) || !now.Add(31*24*time.Hour).Before(cert.NotAfter) {
		return nil, nil, errors.New("collector issuer requires local renewal")
	}
	return cert, key, nil
}

// collectorLeafFresh validates persisted role, hostname, private key and renewal margin.
func collectorLeafFresh(certPEM, keyPEM, caPEM, host string, usage x509.ExtKeyUsage, now time.Time) bool {
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return false
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil || !now.Add(30*24*time.Hour).Before(cert.NotAfter) {
		return false
	}
	roots := x509.NewCertPool()
	// Fail closed when the transport prerequisite or credential validation fails.
	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return false
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{usage}})
	return err == nil
}

// refreshCollectorIdentity renews leaves against a stable CA, preserving overlap during rollout.
func refreshCollectorIdentity(identity *collectorTransportIdentity, host string, now time.Time) (bool, error) {
	changed := false
	// Apply this transport-specific case without changing fleet management authority.
	if identity.CA == "" {
		ca, key, err := collectorCertificate("", nil, nil, true, false, now)
		// Fail closed when the transport prerequisite or credential validation fails.
		if err != nil {
			return false, err
		}
		identity.CA, identity.CAKey = ca, key
		changed = true
	}
	issuer, key, err := collectorIssuer(identity, now)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return false, err
	}
	// Apply this transport-specific case without changing fleet management authority.
	if !collectorLeafFresh(identity.ServerCertificate, identity.ServerKey, identity.CA, host, x509.ExtKeyUsageServerAuth, now) {
		identity.ServerCertificate, identity.ServerKey, err = collectorCertificate(host, issuer, key, false, false, now)
		// Fail closed when the transport prerequisite or credential validation fails.
		if err != nil {
			return false, err
		}
		identity.Host = host
		changed = true
	}
	// Renew the collector role without invalidating the previous leaf during rollout.
	if !collectorLeafFresh(identity.ClientCertificate, identity.ClientKey, identity.CA, "", x509.ExtKeyUsageClientAuth, now) {
		identity.ClientCertificate, identity.ClientKey, err = collectorCertificate("", issuer, key, false, true, now)
		// Fail closed when the transport prerequisite or credential validation fails.
		if err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// loadCollectorIdentity reads bounded owner-only credentials before any network exposure.
func (c *Controller) loadCollectorIdentity(host string) (*collectorTransportIdentity, error) {
	path := filepath.Join(c.opts.StateDir, "collector-transport.json")
	// Fail closed when the transport prerequisite or credential validation fails.
	if err := privateManagedFile(path); err != nil {
		return nil, err
	}
	identity := &collectorTransportIdentity{}
	info, err := os.Lstat(path)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err == nil {
		// Fail closed when the transport prerequisite or credential validation fails.
		if info.Size() > 65536 {
			return nil, errors.New("collector identity exceeds size limit")
		}
		data, readErr := os.ReadFile(path)
		// Fail closed when the transport prerequisite or credential validation fails.
		if readErr != nil {
			return nil, readErr
		}
		// Fail closed when the transport prerequisite or credential validation fails.
		if json.Unmarshal(data, identity) != nil {
			return nil, errors.New("invalid collector identity")
		}
	} else if !os.IsNotExist(err) { // Fail closed when the transport prerequisite or credential validation fails.
		return nil, err
	}
	changed, err := refreshCollectorIdentity(identity, host, c.opts.Now())
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return nil, err
	}
	// Fail closed when the transport prerequisite or credential validation fails.
	if changed {
		// Fail closed when the transport prerequisite or credential validation fails.
		if err = c.saveCollectorIdentity(identity); err != nil {
			return nil, err
		}
	}
	return identity, nil
}

// saveCollectorIdentity durably replaces only the private credential checkpoint.
func (c *Controller) saveCollectorIdentity(identity *collectorTransportIdentity) error {
	path := filepath.Join(c.opts.StateDir, "collector-transport.json")
	// Fail closed when the transport prerequisite or credential validation fails.
	if err := privateManagedFile(path); err != nil {
		return err
	}
	raw, err := json.Marshal(identity)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(c.opts.StateDir, ".collector-transport-*")
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	// Apply this transport-specific case without changing fleet management authority.
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return err
	}
	// Fail closed when the transport prerequisite or credential validation fails.
	if closeErr != nil {
		return closeErr
	}
	// Fail closed when the transport prerequisite or credential validation fails.
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(c.opts.StateDir)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
