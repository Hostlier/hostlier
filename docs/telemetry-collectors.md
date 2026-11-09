# Shared runtime telemetry

Hostlier can receive standard OTLP HTTP JSON metrics and logs from one shared collector per Docker host or Kubernetes node. Applications and arbitrary sidecar containers do not need a monitoring sidecar. This covers runtime CPU/memory/network/filesystem metrics and container stdout/stderr. Kubernetes Metrics API alone does not supply logs, request traces, or application metrics.

## Automatic installation

The minimal Hostlier OpenTelemetry distribution is the default. A standard installation provisions one `hostlier-collector` container per Docker host, or a DaemonSet with one collector per Linux k3s node. Applications and arbitrary sidecars share this infrastructure.

Build controller bundles with `make release` and the matching Linux CLI with `./scripts/package-cli.sh linux arm64` (or `amd64`). Extract both archives into the same directory, keeping `hostlier`, `hostlier-controller` and `collector-image/` together:

```sh
sudo ./hostlier-controller init --domain hostlier.example.com --runtime docker --yes
# Alternatively, choose --runtime k3s.
```

The installer validates and stages the bundled image before changing services, then waits for managed collector readiness. The controller creates a scoped telemetry credential, provides authenticated image downloads at its HTTPS origin, and reconciles the singleton at startup and periodically. Healthy unchanged collectors are reused; missing collectors are recreated. Credentials rotate before expiry. Conflicting resources owned by another controller are refused and reported as degraded.

Release bundles contain an immutable OCI image index for Linux ARM64 and AMD64. Docker and newly joined k3s nodes pull their platform's image from the controller; no public Hostlier registry or manual image import is required. The controller URL must be reachable and its certificate trusted by the runtime. With a private certificate authority, configure Docker/containerd registry trust as well as the collector's `HOSTLIER_CA_FILE`; these are separate trust paths.

Fleet host agents automatically start a separate collector-only mTLS listener on the management port plus one. The management listener and its pinned identity remain unchanged. The collector listener accepts only image downloads and OTLP metrics/log uploads; every request requires a dedicated collector client certificate and a scoped token. Collector credentials cannot invoke management APIs.

The advertised host defaults to the configured hostname, falling back to a concrete listener address. To choose an address reachable from containers and all k3s nodes, set `HOSTLIER_COLLECTOR_URL=https://NODE_ADDRESS:PORT` when starting the host agent. Permit that collector port between the nodes and the host. Wildcard addresses such as `0.0.0.0` cannot be advertised as endpoints.

Hostlier generates and persists the collector CA and separate server/client identities. Server and client leaf certificates renew automatically before expiry; the ten-year issuing CA requires local renewal near its expiry. Docker receives an owned, origin-specific registry trust directory automatically. On k3s, a small digest-pinned BusyBox init container installs the registry CA and client identity into that node's containerd trust directory before the collector image is pulled. The collector's private key is mounted from a Kubernetes Secret. This also bootstraps newly joined Linux nodes. Existing trust owned by another installation is refused rather than overwritten.

The bootstrap uses the default k3s containerd certificate directory. Its writable host mount is limited to registry trust installation; the collector itself retains its normal read-only filesystem and restricted capabilities.

Collector state appears under `telemetry.managed` in the status snapshot. Image assets live under the controller state directory's `collector-image/`; they are reproducible release assets and excluded from recovery archives. Restore the matching release assets when restoring a controller. A standalone executable without those assets fails installation preflight.

The [component-only build](../collector/README.md) uses unmodified upstream OpenTelemetry receivers, native log parsing and discovery, filtering, batching, memory limiting, and OTLP HTTP export. It omits unrelated receivers, exporters, and connectors.

Fleet administrators can read host history through `/v1/hosts/HOST/proxy/telemetry/history?signal=metrics` (or `signal=logs`), with the normal history filters. The management proxy preserves these read-only queries; collector credential administration remains local.

## Manual rendering

`hostlier telemetry render` remains available for separately managed collectors. It writes configuration and manifests without installing them or embedding credentials. Its default is the local minimal image tag; use `--image` with an image available to your runtime when deploying manually. The following examples are for this manual path; standard installations manage these resources and credentials automatically.

## Docker

Run on the Linux Docker host with Hostlier installed:

```sh
hostlier telemetry render --runtime docker --url https://hostlier.example.com --output ./hostlier-observability
cd hostlier-observability
umask 077
sudo hostlier telemetry token --name docker-host --socket /run/hostlier/controller.sock \
  | jq -er '"HOSTLIER_TELEMETRY_TOKEN=" + .token' > collector.env
chmod 600 collector.env
docker compose -f deployment.yaml config --quiet
docker compose -f deployment.yaml up -d
docker compose -f deployment.yaml logs --tail=30 collector
```

The collector discovers running containers using Docker metadata, including background workers without exposed ports. It filters `io.hostlier.managed=true`, attaches the actual `container.id`, reads the standard Docker `json-file` logs under `/var/lib/docker/containers`, and collects Docker runtime statistics. Hostlier verifies the ID against its current owned runtime inventory when accepting records; labels alone do not grant ownership.

This requires the standard Linux Docker data root and `json-file` log driver. Rootless Docker, alternative data roots, journald, remote logging drivers and Docker Desktop's VM file mounts need an adjusted deployment. Do not assume logs from those setups are automatically collected.

The Docker socket is powerful daemon authority: mounting it `:ro` does **not** make Docker API operations read-only. Protect the collector and token as infrastructure credentials. A carefully configured API proxy can restrict methods/endpoints, but is not included in this bundle. The collector filesystem and log mount are read-only; no host ports are published.

## k3s / Kubernetes

Render locally, then create the external token Secret before applying the DaemonSet. Mint the token on the controller host; securely transfer the protected token file if the cluster is elsewhere.

```sh
hostlier telemetry render --runtime k3s --url https://hostlier.example.com --output ./hostlier-observability
umask 077
sudo hostlier telemetry token --name k3s-cluster --socket /run/hostlier/controller.sock \
  | jq -erj '.token' > /tmp/hostlier-telemetry-token
kubectl create namespace hostlier-observability
kubectl -n hostlier-observability create secret generic hostlier-telemetry \
  --from-file=token=/tmp/hostlier-telemetry-token
rm /tmp/hostlier-telemetry-token
kubectl apply -f ./hostlier-observability/deployment.yaml
kubectl -n hostlier-observability rollout status daemonset/hostlier-collector
kubectl -n hostlier-observability logs daemonset/hostlier-collector --tail=30
```

The ConfigMap in `deployment.yaml` embeds the collector configuration; if you edit `collector.yaml` separately, update that embedded configuration too. Restart the DaemonSet after changing the ConfigMap or token Secret.

One DaemonSet Pod per Linux node reads local `/var/log/pods` files through a read-only mount. The container parser extracts namespace, Pod UID, Pod name and container name from runtime paths, independently of log bodies. It reads Hostlier's `hostlier-*` namespaces and excludes its own collector namespace. The Kubernetes attributes processor enriches records using API metadata. Kubelet statistics travel through the authenticated API-server proxy with the projected service-account token and cluster CA. Container metrics carry `k8s.container.name`; Pod aggregate metrics have no container name. Hostlier resolves these against current owned Pods and components.

RBAC allows Pod/namespace discovery plus `get` on `nodes/proxy` and `nodes/stats`. **`nodes/proxy` is sensitive kubelet access**, even with only the `get` verb; the API proxy route is not equivalent to a tightly scoped metrics-only endpoint. Keep this service account isolated. The collector runs as root to read host log files, drops Linux capabilities, uses a read-only root filesystem and does not use host networking. No Kubernetes event/cluster receiver is duplicated across nodes.

## Certificates and token lifecycle

Both exporter connections and Kubernetes API calls verify TLS certificates. For a private controller CA, mount its public CA certificate read-only and add this under `exporters.otlp_http/hostlier`:

```yaml
tls:
  ca_file: /etc/otelcol/controller-ca.pem
```

For Docker add the corresponding Compose bind mount. For Kubernetes add the CA to the ConfigMap mounted at `/etc/otelcol`, and update the embedded config. Do not disable verification. The renderer intentionally accepts an HTTPS origin, not credentials or a URL with query parameters.

Telemetry-only tokens expire after 30 days and authorize uploads only. They cannot read history, deploy applications or call admin APIs. Rotate the token and restart the collector before expiry. List and revoke on the controller:

```sh
sudo hostlier telemetry collectors --socket /run/hostlier/controller.sock
sudo hostlier telemetry revoke COLLECTOR_ID --socket /run/hostlier/controller.sock
```

At most 64 active collector tokens may exist; expired/revoked entries are pruned on subsequent issuance. Keep environment files, token files and Kubernetes Secrets out of version control. Kubernetes Secret protection also depends on cluster RBAC and encryption-at-rest configuration.

## Reading data and operational limits

Authenticated Hostlier users can inspect bounded history:

```sh
hostlier telemetry metrics --app APP --component SIDECAR_NAME --limit 200 --server PROFILE
hostlier telemetry logs --app APP --revision REVISION_ID --limit 200 --server PROFILE
```

The HTTP history endpoint accepts `signal=logs|metrics`, `app`, `component`, `revision`, `limit`, `from`/`to` ISO timestamps, literal message search `q`, `level`, and `before`/`beforeTime` cursor components. Time ranges are start-inclusive and end-exclusive; results sort by observed timestamp and record ID, including when collector delivery arrives out of order. Status shows counts and freshness (90-second threshold), without exposing log bodies. History lives under `STATE_DIR/observations/history` with owner-only file permissions, separately from controller authority state. Retention is up to 30 days, 100,000 records and 128 MiB logical payload (whichever limit is reached first), with an approximately 256 MiB SQLite file cap plus bounded journaling. Stored records are limited to 64 KiB; query pages are limited to 2 MiB and 200 records, with `nextBefore` and `nextBeforeTime` for stable continuation (the existing CLI also accepts `--before ID`). Responses include `retentionDays` and `retainedFrom`. Expired records disappear from queries immediately; physical trimming runs during ingestion.

The initial implementation stores logs and numeric gauge/sum metrics; unsupported histograms and traces are not retained. Runtime telemetry does not automatically create application request traces or business metrics. There is deliberately no generic application OTLP receiver: accepting arbitrary self-declared container IDs would let one workload impersonate another. Secure application instrumentation requires an additional identity design.

Collectors poll runtime metrics every 10 seconds, batch exports for up to 5 seconds and use bounded in-memory queues with a 60-second retry window. Metrics batches contain at most 128 samples; logs batches at most 8 records with 16 KiB record limits (allowing for JSON escaping) to fit the controller's 1 MiB request bound. Controller ingestion accepts at most 512 samples and two concurrent requests. Overload and long outages can drop data. File readers start at the end, with no persisted checkpoint or disk queue; startup/restart gaps are possible. This is bounded operational history, not a durable audit archive.

## Footprint and validation

The minimal upstream-component build was compared with full Contrib in three paired Docker runs, using the same Go 1.26.10 compiler, Linux ARM64 host, pipeline configuration, workload and resource limits:

| Measurement | Full Contrib | Minimal build |
| --- | ---: | ---: |
| Executable | 373.13 MiB | 70.69 MiB |
| Unpacked image regular files | 373.30 MiB | 70.86 MiB |
| Median runtime working set | 45.24 MiB | 16.77 MiB |

The executable is 81.1% smaller and median working memory is 62.9% lower in this fixture. These short runs do not establish production capacity; the existing 256 MiB limits remain unchanged. Startup exports were similar because scrape and batch intervals still apply. The same minimal image also passed k3s collection for Pod metrics and primary/sidecar logs; its single smoke-test memory sample was 17.51 MiB. See the [measurement method and raw results](../../benchmarks/collector-minimal/README.md).

The minimal Linux ARM64 executable is 70.69 MiB; the paired Docker benchmark measured a median 16.77 MiB container working set, and the k3s smoke test measured 17.51 MiB. These short tests are not production sizing guarantees. See the [measurement report](../../benchmarks/collector-minimal/README.md). Generated deployments retain a 256 MiB memory limit and a 96 MiB Go soft limit/heap limiter to allow workload growth. Kubernetes requests 128 MiB/20 millicores and limits 256 MiB/500 millicores; Docker limits 256 MiB/0.5 CPU. Measure your workload cardinality and log volume before changing these bounds.

Actual pinned collector runs were exercised against Linux Docker and k3s with a locally trusted HTTPS sink requiring bearer authentication and JSON payloads. Docker verified metrics and stdout from a generic worker without a port and exclusion of an unmanaged worker. Kubernetes verified Pod/container metrics and stdout from main/helper containers using the generated DaemonSet and RBAC. Sanitized real payloads are in `internal/collector/testdata`. These tests validate collection and wire shape; the controller's separate tests validate owned-resource association and storage.

Upstream references: [Docker stats](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.164.0/receiver/dockerstatsreceiver), [Docker observer](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.164.0/extension/observer/dockerobserver), [receiver creator](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.164.0/receiver/receivercreator), [kubelet stats](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.164.0/receiver/kubeletstatsreceiver), [container log parser](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.164.0/pkg/stanza/docs/operators/container.md), and [Kubernetes RBAC risks](https://kubernetes.io/docs/concepts/security/rbac-good-practices/#access-to-proxy-subresource-of-nodes).
