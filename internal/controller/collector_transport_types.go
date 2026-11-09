package controller

import (
	"crypto/tls"
	"sync"

	"hostlier/internal/model"
)

// collectorTransport owns isolated TLS credentials and their live server configuration.
type collectorTransport struct {
	mu       sync.Mutex
	endpoint string
	identity *collectorTransportIdentity
}

// collectorTransportIdentity keeps the issuer and both mutually authenticated roles separate.
type collectorTransportIdentity struct {
	CA                string `json:"ca"`
	CAKey             string `json:"caKey"`
	ServerCertificate string `json:"serverCertificate"`
	ServerKey         string `json:"serverKey"`
	ClientCertificate string `json:"clientCertificate"`
	ClientKey         string `json:"clientKey"`
	Host              string `json:"host"`
}

// collectorTransportMaterial contains only the collector role and public issuer certificate.
func (i *collectorTransportIdentity) collectorTransportMaterial() *model.CollectorTLS {
	return &model.CollectorTLS{CA: i.CA, Certificate: i.ClientCertificate, PrivateKey: i.ClientKey}
}

// collectorServerCertificate returns the serving keypair independently from collector authority.
func (i *collectorTransportIdentity) collectorServerCertificate() (tls.Certificate, error) {
	return tls.X509KeyPair([]byte(i.ServerCertificate), []byte(i.ServerKey))
}
