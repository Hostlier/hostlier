package runtime

import (
	"fmt"
	"path/filepath"
)

// collectorTrustHelper pins a public multiarchitecture bootstrap image independently of the private registry.
const collectorTrustHelper = "docker.io/rancher/mirrored-library-busybox@sha256:101b4afd76732482eff9b95cae5f94bcf295e521fbec4e01b69c5421f3f3f3e5"
const collectorK3sTrustRoot = "/var/lib/rancher/k3s/agent/etc/containerd/certs.d"

// configureCollectorTLS projects the dedicated Secret and bootstraps each newly scheduled node before its private pull.
func (k *K3s) configureCollectorTLS(pod, container map[string]any, p preparedCollector) {
	// Existing server-authenticated deployments retain the original Pod contract.
	if p.spec.TLS == nil {
		return
	}
	volumes, _ := pod["volumes"].([]any)
	mounts, _ := container["volumeMounts"].([]any)
	volumes = append(volumes, map[string]any{"name": "collector-tls", "secret": map[string]any{"secretName": "hostlier-telemetry", "defaultMode": 0400, "items": []any{map[string]any{"key": "ca.crt", "path": "ca.crt"}, map[string]any{"key": "client.cert", "path": "client.cert"}, map[string]any{"key": "client.key", "path": "client.key"}}}}, map[string]any{"name": "collector-registry-trust", "hostPath": map[string]any{"path": filepath.Join(collectorK3sTrustRoot, p.registry), "type": "DirectoryOrCreate"}})
	mounts = append(mounts, map[string]any{"name": "collector-tls", "mountPath": "/etc/otelcol/tls", "readOnly": true})
	pod["volumes"], container["volumeMounts"] = volumes, mounts
	pod["initContainers"] = []any{map[string]any{"name": "registry-trust", "image": collectorTrustHelper, "imagePullPolicy": "IfNotPresent", "command": []string{"/bin/sh", "-ec", collectorTrustScript}, "args": []string{"hostlier-registry-trust", k.base.owner, p.registry, p.revision, collectorHosts(p)}, "securityContext": map[string]any{"runAsUser": 0, "runAsGroup": 0, "readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}}}, "resources": map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "8Mi"}, "limits": map[string]any{"cpu": "100m", "memory": "32Mi"}}, "volumeMounts": []any{map[string]any{"name": "collector-tls", "mountPath": "/identity", "readOnly": true}, map[string]any{"name": "collector-registry-trust", "mountPath": "/registry"}}}}
}

// collectorHosts uses immutable revision files and atomically replaces only the containerd host configuration.
func collectorHosts(p preparedCollector) string {
	dir := filepath.Join(collectorK3sTrustRoot, p.registry, p.revision)
	return fmt.Sprintf("server = %q\n[host.%q]\n  capabilities = [\"pull\", \"resolve\"]\n  ca = %q\n  client = [[%q, %q]]\n", p.spec.Endpoint, p.spec.Endpoint, filepath.Join(dir, "ca.crt"), filepath.Join(dir, "client.cert"), filepath.Join(dir, "client.key"))
}

// collectorTrustScript validates an origin ownership marker and publishes a complete TLS revision before hosts.toml.
const collectorTrustScript = `umask 077
owner=$1
revision=$3
hosts=$4
[ -d /registry ] && [ ! -L /registry ]
[ "$(stat -c %u /registry)" = 0 ]
mode=$(stat -c %a /registry)
[ $((0$mode & 0022)) = 0 ]
if [ -e /registry/.hostlier-owner ] || [ -L /registry/.hostlier-owner ]; then
  [ -f /registry/.hostlier-owner ] && [ ! -L /registry/.hostlier-owner ]
  [ "$(stat -c %u /registry/.hostlier-owner)" = 0 ]
  [ "$(stat -c %a /registry/.hostlier-owner)" = 600 ]
  [ "$(cat /registry/.hostlier-owner)" = "$owner" ]
else
  [ -z "$(ls -A /registry)" ]
  stage=$(mktemp /registry/.owner.XXXXXX)
  printf '%s\n' "$owner" > "$stage"
  mv -f "$stage" /registry/.hostlier-owner
fi
[ ! -L "/registry/$revision" ]
mkdir -p "/registry/$revision"
[ "$(stat -c %u "/registry/$revision")" = 0 ]
chmod 700 "/registry/$revision"
for name in ca.crt client.cert client.key; do
  stage=$(mktemp "/registry/$revision/.identity.XXXXXX")
  cat "/identity/$name" > "$stage"
  mv -f "$stage" "/registry/$revision/$name"
done
stage=$(mktemp /registry/.hosts.XXXXXX)
printf '%s' "$hosts" > "$stage"
mv -f "$stage" /registry/hosts.toml
`
