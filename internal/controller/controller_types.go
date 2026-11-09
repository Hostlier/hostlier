package controller

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"

	"hostlier/internal/emailauth"
	"hostlier/internal/model"
	"hostlier/internal/recovery"
	"hostlier/internal/store"
	"hostlier/internal/tunnel"
	"hostlier/pkg/workflow"
)

// Options configures runtime dependencies, persistence and bounded controller deadlines.
type Options struct {
	// TunnelDomain reserves a dedicated wildcard suffix; TunnelURL is the public HTTPS controller origin.
	TunnelDomain, TunnelURL            string
	ManagedCollector                   bool
	CollectorEndpoint, CollectorCAFile string
	CollectorInterval                  time.Duration
	RecoveryDestinations               map[string]*recovery.Destination
	EmailEnrollment                    *emailauth.Service
	StatusInterval, StatusTimeout      time.Duration
	PlanningTimeout                    time.Duration
	Packages                           workflow.Runner
	StateDir                           string
	ConfigPath                         string
	WriteTimeout, LongOperationTimeout time.Duration
	Config                             model.ServerConfig
	Runtime                            model.Runtime
	ResourceManager                    model.ResourceManager
	Hostname, LogPath                  string
	Now                                func() time.Time
	PairingTTL, PlanTTL                time.Duration
}

// ListenOptions selects protected local and TLS remote API listeners.
type ListenOptions struct {
	MasterFingerprint     string
	ControllerFingerprint string
	SocketPath, Host      string
	Port                  int
	TCP                   bool
	TLSConfig             *tls.Config
}

// Controller owns durable state, listener lifecycle and serialized background execution.
type Controller struct {
	master                 masterListener
	agentOnly              bool
	tunnelBrokers          map[string]*tunnel.Broker
	access                 accessState
	collectorTransport     collectorTransport
	managed                managedCollectorService
	telemetry              telemetryService
	deviceEpoch            string
	deviceNonces           map[string]int64
	observations           *statusCapture
	human                  humanSecurity
	mu                     sync.Mutex
	opts                   Options
	state                  State
	store                  *store.Store
	servers                []*http.Server
	listeners              []net.Listener
	address                string
	ctx                    context.Context
	cancel                 context.CancelFunc
	wg                     sync.WaitGroup
	execMu                 sync.Mutex
	rates                  map[string]rate
	closed                 bool
	streams                int
	eventPersistedSequence uint64
}
