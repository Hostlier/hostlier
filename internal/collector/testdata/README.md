# Live collector wire fixtures

These OTLP HTTP JSON bodies came from OpenTelemetry Collector Contrib v0.164.0 running on Linux ARM64. The HTTPS fixture sink verified its bearer header and `application/json` content type. It used an ephemeral test CA with normal certificate verification.

- `docker-*`: a generic managed shell worker with no exposed port, stdout messages and runtime statistics. An unmanaged worker was excluded. The container ID/name/hostname have been normalized.
- `k3s-*`: a two-container Pod (`main`, `helper`), a generated shared DaemonSet, kubelet statistics via the API-server proxy, and native container-file logs. Includes Pod aggregate and both container metric resources. Pod UID and node name have been normalized.

Only dedicated fixture workload records were retained. Values and timestamps illustrate real wire encoding, not production observations or a performance benchmark. Configured collection labels are hints; controller tests must resolve ownership through actual runtime inventory rather than trusting these resource attributes.
