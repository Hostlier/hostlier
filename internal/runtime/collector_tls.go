package runtime

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
	"hostlier/internal/model"
)

const collectorTrustMarker = ".hostlier-owner"

// collectorTLSFingerprint makes every dedicated credential part of the rollout identity.
func collectorTLSFingerprint(identity *model.CollectorTLS) string {
	raw, _ := json.Marshal(identity)
	return string(raw)
}

// collectorTLSFiles returns dedicated transport files without exposing fleet management keys.
func collectorTLSFiles(identity *model.CollectorTLS) map[string][]byte {
	// Public-certificate deployments retain their existing token-only transport.
	if identity == nil {
		return nil
	}
	return map[string][]byte{"ca.crt": []byte(identity.CA), "client.cert": []byte(identity.Certificate), "client.key": []byte(identity.PrivateKey)}
}

// installMutualTLS validates a complete client identity before any runtime mutation.
func (p *preparedCollector) installMutualTLS(config map[string]any) error {
	identity := p.spec.TLS
	// Bound all private material before parsing or persisting it.
	if len(identity.CA)+len(identity.Certificate)+len(identity.PrivateKey) > 1<<20 {
		return model.Err("COLLECTOR_TLS", "Collector TLS identity exceeds limits")
	}
	roots := x509.NewCertPool()
	_, err := tls.X509KeyPair([]byte(identity.Certificate), []byte(identity.PrivateKey))
	// Neither malformed trust nor mismatched keys may fall back to server-only TLS.
	if !roots.AppendCertsFromPEM([]byte(identity.CA)) || err != nil {
		return model.Err("COLLECTOR_TLS", "Collector TLS identity is invalid")
	}
	exporters, _ := config["exporters"].(map[string]any)
	exporter, _ := exporters["otlp_http/hostlier"].(map[string]any)
	// Bind only the controlled Hostlier exporter, never arbitrary plugin configuration.
	if exporter == nil {
		return model.Err("COLLECTOR_CONFIG", "Collector Hostlier exporter is missing")
	}
	exporter["tls"] = map[string]any{"ca_file": "/etc/otelcol/tls/ca.crt", "cert_file": "/etc/otelcol/tls/client.cert", "key_file": "/etc/otelcol/tls/client.key"}
	raw, err := yaml.Marshal(config)
	p.config = string(raw)
	return err
}

// installCollectorTrust maintains the daemon's registry-specific TLS identity before a pull.
func (r *Runtime) installCollectorTrust(p preparedCollector) error {
	// Automatic registry trust applies only to dedicated mutually authenticated collectors.
	if p.spec.TLS == nil {
		return nil
	}
	root := r.options.CollectorTrustDirectory
	// The controller runs beside the local Docker daemon in ordinary installations.
	if root == "" {
		root = "/etc/docker/certs.d"
	}
	return writeCollectorTrust(root, p.registry, r.owner, collectorTLSFiles(p.spec.TLS))
}

// writeCollectorTrust refuses foreign origin directories before installing scoped private files.
func writeCollectorTrust(root, registry, owner string, files map[string][]byte) error {
	// Origins are one literal path component; encoded or escaped path separators are never accepted.
	if registry == "" || strings.ContainsAny(registry, "/\\\x00") || registry == "." || registry == ".." {
		return model.Err("COLLECTOR_TLS", "Collector registry origin is invalid")
	}
	// Every parent must remain an ordinary controller-owned nonwritable directory.
	if err := collectorTrustParents(root); err != nil {
		return err
	}
	dir := filepath.Join(root, registry)
	// Creating a fresh origin does not grant permission to overwrite an existing foreign directory.
	if err := ensureCollectorTrustOwner(dir, owner); err != nil {
		return err
	}
	for name, data := range files { // Atomic replacement cannot follow an existing credential symlink.
		if err := writeCollectorFile(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}

// collectorTrustParents rejects symlinked or writable trust ancestors before creating missing children.
func collectorTrustParents(path string) error {
	path, err := filepath.Abs(path)
	// Invalid absolute paths cannot be used as trust roots.
	if err != nil {
		return err
	}
	// Validate existing ancestors too; a safe leaf cannot make a redirected parent trustworthy.
	if parent := filepath.Dir(path); parent != path {
		// Reject the first unsafe ancestor before descending.
		if err = collectorTrustParents(parent); err != nil {
			return err
		} // Reject the first unsafe ancestor before descending.
	}
	info, err := os.Lstat(path)
	// Missing directories are private from creation after their parents were verified.
	if os.IsNotExist(err) {
		return os.Mkdir(path, 0700)
	}
	// Ordinary root-owned ancestors are allowed when an embedding runs as an unprivileged user.
	if err != nil || !collectorTrustAncestorSafe(info) {
		return model.Err("COLLECTOR_TLS", "Collector registry trust directory is unsafe")
	}
	return nil
}

// collectorTrustAncestorSafe admits protected root/current-user ancestors and sticky root-owned temporary roots.
func collectorTrustAncestorSafe(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	// A symlink or unrelated owner's directory cannot participate in a credential path.
	if !ok || !info.IsDir() || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
		return false
	}
	return info.Mode().Perm()&0022 == 0 || (stat.Uid == 0 && info.Mode()&os.ModeSticky != 0)
}

// collectorTrustDirectorySafe permits only owned directories without group or world write access.
func collectorTrustDirectorySafe(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return info.IsDir() && info.Mode().Perm()&0022 == 0 && ok && stat.Uid == uint32(os.Geteuid())
}

// ensureCollectorTrustOwner permits adoption only of an empty safe origin directory.
func ensureCollectorTrustOwner(dir, owner string) error {
	// New origins are private from their first appearance.
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(dir)
	// A symlink or foreign writable directory must not redirect root credential writes.
	if err != nil || !collectorTrustDirectorySafe(info) {
		return model.Err("OWNERSHIP", "Collector registry trust directory is unsafe")
	}
	entries, err := os.ReadDir(dir)
	// Failed inventory cannot authorize adopting an unknown origin.
	if err != nil {
		return err
	}
	// Empty directories are the only unmarked origins that may be claimed.
	if len(entries) == 0 {
		return writeCollectorFile(filepath.Join(dir, collectorTrustMarker), []byte(owner+"\n"))
	}
	return verifyCollectorTrustMarker(dir, owner)
}

// verifyCollectorTrustMarker requires an existing origin to belong to this exact controller.
func verifyCollectorTrustMarker(dir, owner string) error {
	marker := filepath.Join(dir, collectorTrustMarker)
	info, err := os.Lstat(marker)
	// Existing ownership markers must be ordinary protected files, never symlinks.
	if err != nil || !collectorTrustMarkerSafe(info) {
		return model.Err("OWNERSHIP", "Refusing foreign collector registry trust")
	}
	content, err := os.ReadFile(marker)
	// Only this runtime may rotate its exact origin identity.
	if err != nil || string(content) != owner+"\n" {
		return model.Err("OWNERSHIP", "Refusing foreign collector registry trust")
	}
	return nil
}

// collectorTrustMarkerSafe rejects redirected, shared, foreign-owned or publicly accessible ownership claims.
func collectorTrustMarkerSafe(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && ok && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}
