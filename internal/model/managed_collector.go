package model

import "context"

// CollectorDeployment carries controller-owned provisioning inputs, including a private ingest credential.
type CollectorDeployment struct {
	TLS                    *CollectorTLS
	Endpoint, Image, Token string
	Config, Manifest       string
	// CAFile optionally supplies exporter trust; image pulls still use the runtime host's registry trust.
	CAFile string
}

// ManagedCollectorRuntime supervises one shared collector per Docker host or Kubernetes node.
type ManagedCollectorRuntime interface {
	// EnsureCollector succeeds only after the owned singleton is ready at the requested revision.
	EnsureCollector(context.Context, CollectorDeployment) error
}

// CollectorTLS carries a dedicated collector identity, never a fleet management identity.
type CollectorTLS struct {
	CA          string
	Certificate string
	PrivateKey  string
}
