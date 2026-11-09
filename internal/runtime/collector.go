package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	"hostlier/internal/model"
)

const collectorName = "hostlier-collector"
const collectorNamespace = "hostlier-observability"
const collectorHashLabel = "io.hostlier.collector-revision"

// preparedCollector freezes validated inputs and public CA contents before any runtime mutation.
type preparedCollector struct {
	spec     model.CollectorDeployment
	config   string
	ca       []byte
	revision string
	registry string
}

// prepareCollector confines credentials to the controller's pinned collector image and exporter origin.
func prepareCollector(spec model.CollectorDeployment) (preparedCollector, error) {
	p := preparedCollector{spec: spec, config: spec.Config}
	u, err := url.Parse(spec.Endpoint)
	// Management-provided origins still must not contain redirect paths or embedded credentials.
	if err != nil || !collectorOrigin(u) {
		return p, model.Err("COLLECTOR_CONFIG", "Collector requires a plain HTTPS controller origin")
	}
	p.registry = u.Host
	// The scoped token must never be forwarded to an unrelated image registry or repository.
	if !strings.HasPrefix(spec.Image, u.Host+"/hostlier/collector@sha256:") || !digestPattern.MatchString(spec.Image) {
		return p, model.Err("COLLECTOR_CONFIG", "Collector image must be pinned in the controller registry")
	}
	// Bound private credentials and reject environment-file injection before persisting anything.
	if spec.Token == "" || len(spec.Token) > 4096 || strings.ContainsAny(spec.Token, "\r\n\x00") {
		return p, model.Err("COLLECTOR_CONFIG", "Collector credential is invalid")
	}
	// Read and integrate optional exporter trust before computing the immutable rollout identity.
	if err = p.prepareConfig(); err != nil {
		return p, err
	}
	raw, _ := json.Marshal([]string{spec.Endpoint, spec.Image, spec.Token, p.config, spec.Manifest, string(p.ca), collectorTLSFingerprint(spec.TLS)})
	hash := sha256.Sum256(raw)
	p.revision = hex.EncodeToString(hash[:])
	return p, nil
}

// collectorOrigin permits the same authenticated HTTPS origin for image and telemetry delivery.
func collectorOrigin(u *url.URL) bool {
	return u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

// prepareConfig validates bounded YAML and installs optional explicit exporter trust.
func (p *preparedCollector) prepareConfig() error {
	// Configuration and manifests are bounded independently of credentials and image metadata.
	if len(p.config) == 0 || len(p.config) > 1<<20 || len(p.spec.Manifest) > 2<<20 {
		return model.Err("COLLECTOR_CONFIG", "Collector configuration exceeds limits")
	}
	var config map[string]any
	// Malformed YAML must fail before a working collector can be replaced.
	if yaml.Unmarshal([]byte(p.config), &config) != nil || config == nil {
		return model.Err("COLLECTOR_CONFIG", "Collector configuration is invalid")
	}
	// Fleet collectors use a dedicated mutual TLS identity on both transport paths.
	if p.spec.TLS != nil {
		return p.installMutualTLS(config)
	}
	// The normal public-certificate deployment needs no private CA file.
	if p.spec.CAFile == "" {
		return nil
	}
	// Trust-file validation is separate from YAML parsing so both failure boundaries remain explicit.
	if err := p.readCA(); err != nil {
		return err
	}
	return p.installExporterCA(config)
}

// readCA loads only a bounded regular public trust bundle.
func (p *preparedCollector) readCA() error {
	info, err := os.Stat(p.spec.CAFile)
	// Public trust input must be a bounded regular file, never a device or pipe.
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return model.Err("COLLECTOR_CONFIG", "Collector CA file is unavailable")
	}
	p.ca, err = os.ReadFile(p.spec.CAFile)
	// Failed trust reads cannot downgrade to disabled certificate verification.
	if err != nil {
		return model.Err("COLLECTOR_CONFIG", "Collector CA file is unavailable")
	}
	return nil
}

// installExporterCA modifies only the reviewed Hostlier exporter trust configuration.
func (p *preparedCollector) installExporterCA(config map[string]any) error {
	exporters, _ := config["exporters"].(map[string]any)
	exporter, _ := exporters["otlp_http/hostlier"].(map[string]any)
	// A supplied CA requires the expected exporter rather than guessing another component.
	if exporter == nil {
		return model.Err("COLLECTOR_CONFIG", "Collector Hostlier exporter is missing")
	}
	exporter["tls"] = map[string]any{"ca_file": "/etc/otelcol/ca.pem"}
	raw, err := yaml.Marshal(config)
	p.config = string(raw)
	return err
}

// collectorDirectory creates only protected directories beneath the configured runtime state root.
func (r *Runtime) collectorDirectory(revision string) (string, error) {
	// Runtime state may be new on the first automatic provisioning attempt.
	if err := os.MkdirAll(r.options.StateDir, 0700); err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(r.options.StateDir)
	// Resolve an existing installation root before enforcing protected child-directory ownership.
	if err != nil {
		return "", err
	}
	for _, part := range []string{"observations", "collector", revision} { // Every generated child must be an ordinary private directory.
		dir = filepath.Join(dir, part)
		if err = privateCollectorDirectory(dir); err != nil { // Refuse redirected child paths before writing any credential.
			return "", err
		}
	}
	return dir, nil
}

// privateCollectorDirectory rejects symlinks and restricts access to the controller account.
func privateCollectorDirectory(path string) error {
	info, err := os.Lstat(path)
	// Create missing children with private permissions from the outset.
	if os.IsNotExist(err) {
		return os.Mkdir(path, 0700)
	}
	// An existing symlink or nondirectory cannot redirect collector credential writes.
	if err != nil || !info.IsDir() {
		return model.Err("COLLECTOR_PATH", "Collector state directory is unsafe")
	}
	return os.Chmod(path, 0700)
}

// writeCollectorFile atomically publishes a protected file without following an existing destination link.
func writeCollectorFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".collector-")
	// A failed private staging file leaves the currently mounted revision unchanged.
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	closeErr := file.Close()
	// Incomplete content must not replace an existing collector configuration.
	if err != nil {
		return err
	}
	// Closing the staged file must succeed before publishing it.
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}
