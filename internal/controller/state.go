package controller

import "hostlier/internal/fleet"

// State contains the versioned durable controller data protected by the state lock.
type State struct {
	MasterFingerprint     *string                          `json:"masterFingerprint,omitempty"`
	TunnelGatewayDomain   string                           `json:"tunnelGatewayDomain,omitempty"`
	TunnelGatewayURL      string                           `json:"tunnelGatewayUrl,omitempty"`
	Tunnels               map[string]TunnelLease           `json:"tunnels,omitempty"`
	FusedConnections      map[string]*FusedOAuthConnection `json:"fusedConnections,omitempty"`
	RemoteBackupSchedules map[string]RemoteBackupSchedule  `json:"remoteBackupSchedules,omitempty"`
	BackupPolicies        map[string]*BackupPolicy         `json:"backupPolicies,omitempty"`
	RecoveryJobs          map[string]*RecoveryJob          `json:"recoveryJobs,omitempty"`
	RecoveryPoints        map[string]RemoteRecoveryPoint   `json:"recoveryPoints,omitempty"`
	TelemetryCollectors   map[string]TelemetryCollector    `json:"telemetryCollectors,omitempty"`
	Authorizations        map[string]*Authorization        `json:"authorizations,omitempty"`
	HumanRealm            string                           `json:"humanRealm,omitempty"`
	Hosts                 map[string]HostRecord            `json:"hosts,omitempty"`
	FleetIdentity         fleet.Identity                   `json:"fleetIdentity,omitempty"`
	Human                 *HumanIdentity                   `json:"human,omitempty"`
	HumanApprovals        map[string]*HumanApproval        `json:"humanApprovals,omitempty"`
	PlanningJobs          map[string]*PlanningJob          `json:"planningJobs,omitempty"`
	WorkflowNamespace     string                           `json:"workflowNamespace,omitempty"`
	WorkflowData          map[string]string                `json:"workflowData,omitempty"`
	WorkflowRecords       map[string]*WorkflowRecord       `json:"workflowRecords,omitempty"`
	Backups               map[string]BackupRecord          `json:"backups"`
	Resources             map[string]*ResourceRecord       `json:"resources"`
	ResourceCredentials   map[string]string                `json:"resourceCredentials"`
	Version               int                              `json:"version"`
	Generation            int64                            `json:"generation"`
	Runtime               string                           `json:"runtime"`
	Hostname              string                           `json:"hostname"`
	Domains               []string                         `json:"domains"`
	Clients               map[string]*Client               `json:"clients"`
	Enrollments           map[string]*Enrollment           `json:"enrollments"`
	Requests              map[string]*Enrollment           `json:"requests"`
	Secrets               map[string]*Secret               `json:"secrets"`
	Writes                map[string]Write                 `json:"writes"`
	Plans                 map[string]*Plan                 `json:"plans"`
	Operations            map[string]*Operation            `json:"operations"`
	Applications          map[string]*Application          `json:"applications"`
	Events                []LogEntry                       `json:"events"`
	EventSequence         uint64                           `json:"eventSequence"`
}

// LogEntry contains one bounded public execution event.
type LogEntry struct {
	Application string `json:"application,omitempty"`
	Actor       string `json:"actor,omitempty"`
	Sequence    uint64 `json:"sequence"`
	Timestamp   string `json:"timestamp"`
	Level       string `json:"level"`
	Message     string `json:"message"`
	OperationID string `json:"operationId,omitempty"`
}
