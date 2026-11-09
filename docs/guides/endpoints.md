# Named endpoints and managed files

[All guides](../../GUIDE.md) · [Fused Engine example](../../examples/fused-engine/.hostlier)

Version 2 projects can declare up to 16 named listeners. Each public listener owns a route and container port. `primaryEndpoint` selects the existing deployment URL, CLI route selection and primary readiness listener. If omitted, Hostlier prefers `http`, `main`, `default`, then the first name in sorted order. A publicly routed group requires a public primary.

```yaml
version: 2
name: engine
source:
  image: ghcr.io/usefused/engine:latest
primaryEndpoint: http
endpoints:
  http:
    exposure: public
    port: 8081
    route: https://engine.apps.example.com
    protocol: http
    httpVersions: [h1, h2, h3]
    streaming: true
    websocket: true
  grpc:
    exposure: public
    port: 50051
    route: https://grpc.apps.example.com
    protocol: grpc
    idleTimeoutSeconds: 600
configs:
  engine:
    file: engine.yaml
    mount: /app/engine.yaml
health:
  path: /health
```

Use domains authorized on your controller. Every named public route participates in domain checks, pending-operation reservations and conflicts with other applications or operator routes. Routes within the same application can share a hostname with different path prefixes; the more specific path wins. `--route` changes the primary only. Native gRPC method paths cannot use `proxy.stripPrefix`. Typed endpoints proxy the exact configured path and its descendants without adding a trailing-slash redirect, preserving literal MCP addresses and RPC methods. Existing untyped web routes keep their previous redirect behavior.

Docker maps public listeners to separate loopback ports on the owned namespace container. Sidecars share its network namespace; all their listener ports are checked for collisions. Kubernetes uses named ClusterIP service ports. Applications remain one deployment, revision and rollback unit. Private application discovery keeps its existing primary endpoint contract; secondary private endpoints are rejected explicitly. Sidecars may use named loopback listeners.

## Transport policy

`protocol` describes the **upstream** connection. `httpVersions` describes required **client-to-edge** capabilities. These fields do not install a different application protocol server inside the container.

| Upstream protocol | Caddy | Nginx |
| --- | --- | --- |
| `http` or omitted | HTTP proxy | HTTP/1.1 proxy |
| `https` | Verified TLS proxy | Verified TLS proxy |
| `h2c` | Cleartext HTTP/2 | Rejected before workload mutation |
| `grpc` | Native HTTP/2 to cleartext backend | Native `grpc_pass` |
| `grpcs` | Native HTTP/2 to verified TLS backend | Native `grpc_pass grpcs://` |
| Client HTTP/3 (`httpVersions: [h3]`) | QUIC/UDP ingress | Rejected before workload mutation |

The owned TLS edge retains HTTP/1 and HTTP/2. An explicit `h3` requirement additionally enables UDP 443; permit UDP 443 through the server firewall. HTTP/3 support was tested with Caddy **2.11.7**. The disposable VM's old 2.6.2 failed QUIC certificate selection after configuration reload; upgrade older Caddy installations before using HTTP/3. Generic HTTP/3 upstream proxying is not provided.

HTTP endpoints preserve WebSocket upgrades, SSE, streaming request/response bodies and Streamable HTTP session headers and methods. Streaming and WebSockets default to enabled. Set `streaming: false` to leave buffering to the proxy’s normal behavior, or `websocket: false` to suppress HTTP upgrade forwarding. `idleTimeoutSeconds` sets upstream I/O timeouts (up to 86400 seconds), rather than an overall request lifetime. `upstreamServerName` selects a verified TLS identity for encrypted backends; certificates use the edge host's trust store.

Primary listeners using `grpc`, `grpcs`, `h2c` or `https` require `health.exec`, for example a health-probe executable included in the image. HTTP primaries can use `health.path`. Secondary gRPC routes are exercised by protocol clients rather than an HTTP GET health check.

For explicit gRPC service/method allowlists and HTTP, WebSocket, SSE, Streamable HTTP and HTTP/3 admission rules, see [Traffic policies](traffic-policies.md). Policy endpoints deny unlisted requests and preserve their full route ownership boundary.

## Managed configuration files

Each component can declare up to 32 files; the complete group allows 256 KiB of plaintext configuration, with 64 KiB per file. `file` selects a regular UTF-8 file inside the project directory. The CLI snapshots its exact bytes before planning, removes the source path from the request, and submits the contents for approval. Changing the local file after approval cannot change the retained revision.

Alternatively use `content` for inline text or `secret` for an existing encrypted Hostlier secret reference:

```yaml
configs:
  settings:
    content: "log_level: info\n"
    mount: /etc/my-app/settings.yaml
  credential:
    secret: my-app-credential-file
    mount: /run/my-app/credential
    mode: 0444
```

`mode` defaults to `0444` and accepts read-only permission bits. Mount paths must be normalized absolute file paths outside kernel/device filesystems; files cannot overlap other files or persistent volume mounts. Docker materializes operation-scoped files under the protected controller state directory and mounts them read-only. Kubernetes creates immutable ConfigMaps or Secrets with exact read-only `subPath` mounts. Secret values retain component scope and pinned secret revisions, including rollback; file secrets never become environment variables. Managed files are retained with controller state and recovery backups. Runtime files are not garbage-collected automatically in this version.

The dashboard lists named endpoints and managed-file mount metadata. Changes continue through CLI planning and approval.

## Deploy Fused Engine

Use the [complete example](../../examples/fused-engine/.hostlier), replace both public addresses, then set these secrets through a hidden prompt, `--stdin` or `--from-env`:

- `fused-engine-license`: a valid Fused license for the configured Registry.
- `fused-engine-encryption`: a fresh base64-encoded 32-byte key; preserve it across redeployments.
- `fused-engine-postgres`: a fresh database password.
- `fused-engine-database`: `postgres://fused:PASSWORD@127.0.0.1:5432/fused?sslmode=disable`, using the same password with URL encoding where needed.

```sh
hostlier validate --file examples/fused-engine/.hostlier
hostlier plan --file examples/fused-engine/.hostlier
hostlier deploy --file examples/fused-engine/.hostlier
hostlier status fused-engine
```

The example includes an isolated PostgreSQL sidecar and persistent data. It verifies Engine's HTTP API and native gRPC transport. Unified App subprocess workers additionally need the matching Engine seccomp profile and host namespace support; this Hostlier change does not add a container security-profile API. Check `unified_app_worker_ready` in Engine's `/health` response before treating Unified Apps as available.

[VM verification results](../endpoint-verification.md).
