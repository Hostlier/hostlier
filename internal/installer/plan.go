package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"hostlier/internal/dockerlogin"
)

// Options contains the target installation paths and selected runtime identity.
type Options struct {
	MasterFingerprint  string              `json:"masterFingerprint,omitempty"`
	DockerLogin        *dockerlogin.Source `json:"dockerLogin,omitempty"`
	DockerMode         string              `json:"dockerMode,omitempty"`
	CollectorSource    string              `json:"collectorSource"`
	Proxy              string              `json:"proxy,omitempty"`
	Domain             string              `json:"domain"`
	Runtime            string              `json:"runtime"`
	ControllerHostname string              `json:"controllerHostname"`
	StateDir           string              `json:"stateDir"`
	ConfigPath         string              `json:"configPath"`
	Executable         string              `json:"executable"`
}

// Component describes a package or binary observed during inspection.
type Component struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
}

// Installation records approved ownership and completed stages for safe retries.
type Installation struct {
	MasterFingerprint  string              `json:"masterFingerprint,omitempty"`
	DockerLogin        *dockerlogin.Source `json:"dockerLogin,omitempty"`
	DockerMode         string              `json:"dockerMode,omitempty"`
	Proxy              string              `json:"proxy,omitempty"`
	OwnedNginx         bool                `json:"ownedNginx,omitempty"`
	Runtime            string              `json:"runtime"`
	Hostname           string              `json:"hostname"`
	ControllerHostname string              `json:"controllerHostname"`
	OwnedCaddy         bool                `json:"ownedCaddy"`
	Completed          []string            `json:"completed"`
	FirewallRules      []int               `json:"firewallRules"`
	Ready              bool                `json:"ready"`
}

// Snapshot captures host conditions that must remain unchanged after approval.
type Snapshot struct {
	UbuntuVersion       string        `json:"ubuntuVersion,omitempty"`
	DetectedDocker      bool          `json:"detectedDocker"`
	DetectedNginx       bool          `json:"detectedNginx"`
	ExistingNginxDigest string        `json:"existingNginxDigest,omitempty"`
	ExistingDockerID    string        `json:"existingDockerId,omitempty"`
	CollectorDigest     string        `json:"collectorDigest"`
	Nginx               Component     `json:"nginx"`
	Certbot             Component     `json:"certbot"`
	K3s                 Component     `json:"k3s"`
	Docker              Component     `json:"docker"`
	Caddy               Component     `json:"caddy"`
	Firewall            string        `json:"firewall"`
	Rules               []int         `json:"rules"`
	Ports               []bool        `json:"ports"`
	Installation        *Installation `json:"installation,omitempty"`
}

// Plan binds the installation preview and host snapshot to an approval fingerprint.
type Plan struct {
	Options
	Supported   bool     `json:"supported"`
	Changes     []string `json:"changes"`
	Warnings    []string `json:"warnings"`
	Errors      []string `json:"errors"`
	Snapshot    Snapshot `json:"snapshot"`
	Fingerprint string   `json:"fingerprint"`
}

// ApplyOptions carries explicit approval and an optional controller binary source.
type ApplyOptions struct {
	Approved         bool
	SourceExecutable string
	OnProgress       func(step, status string)
}

// defaults supplies rendering fallbacks; Inspect resolves automatic selections before rendering assets.
func defaults(o Options) Options {
	// Asset-only callers use Caddy unless inspection has resolved another proxy.
	if o.Proxy == "" {
		o.Proxy = "caddy"
	}
	// Select Docker when no runtime was explicitly requested.
	if o.Runtime == "" {
		o.Runtime = "docker"
	}
	// Derive the controller hostname from the declared supported domain.
	if o.ControllerHostname == "" {
		o.ControllerHostname = "hostlier." + o.Domain
	}
	// Keep persistent controller state under the standard protected system directory.
	if o.StateDir == "" {
		o.StateDir = "/var/lib/hostlier"
	}
	// Use the standard server configuration unless a path was supplied.
	if o.ConfigPath == "" {
		o.ConfigPath = "/etc/hostlier/config.yaml"
	}
	// Use the standard installed controller location unless overridden.
	if o.Executable == "" {
		o.Executable = "/usr/local/bin/hostlier-controller"
	}
	return o
}

// fingerprint binds all plan fields except the derived fingerprint itself.
func fingerprint(p Plan) string {
	p.Fingerprint = ""
	data, _ := json.Marshal(p)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
