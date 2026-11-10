---
name: hostlier-deploy
description: Deploy an application, agent or worker through Hostlier using a project file and reviewed deployment plan.
---

# Hostlier deploy

Inspect the repository's existing `.hostlier` and the application's runtime contract before editing. For a simple public image, a version 1 project looks like:

```yaml
version: 1
name: storefront
image: ghcr.io/acme/storefront:1.0
port: 3000
route: storefront.example.com
health:
  path: /health
```

Replace the example image, route, port and health path with the application's actual values. Keep secrets as references to Hostlier secrets, never inline credentials. Use version 2 for named endpoints, managed config files, private services, workers or sidecars. The complete OCI image schema below works without access to a Hostlier source checkout.

```sh
hostlier validate --file .hostlier
hostlier plan --file .hostlier --server production
hostlier deploy --file .hostlier --server production
```

For private images, top-level `image` and version 2
`source.image: ghcr.io/acme/storefront:1.0` use the same automatic preparation.
The CLI detects the selected
controller or fleet host's architecture and asks it to stage the image first.
Registry connectivity or login failure triggers an automatic download with local
Docker credentials/helpers and a verified upload. Log in locally for fallback
with `docker login ghcr.io --username USERNAME`. No Docker daemon is needed for
registry downloads. Architecture is optional for all container sources; an
explicit value must match the controller. Controller access, platform and storage
errors stop preparation. Prepared artifact references skip registry preparation
in both schema versions. `source.container` selects exactly one of `context`
(local Docker build) or `archive` (prebuilt `docker save` file). Use `image` or
`source.image` for registry references.
`hostlier validate` remains offline for registry download sources.

The controller enforces exact human approval or a hardware-signed deployment window. In a valid window, named stateless apps can deploy without another prompt. In a sandbox window (an app prefix such as `try-`), new prefixed apps can be created up to the window's app cap, and the agent may stop or delete them with `hostlier stop NAME` / `hostlier delete NAME`; see the authorize skill. Otherwise the CLI prints a request code and waits; give the human the printed `hostlier approve CODE --key FILE` command to run in their own trusted terminal. Do not invent confirmation or use `--yes`. Resume an interrupted wait with `hostlier approval REQUEST_ID --watch`; do not create duplicate deployments.

Progress and reconnects are automatic. `--detach` returns after acceptance; an operation ID is not proof of success. Use `hostlier operation ID --watch` to reattach, then check `hostlier status storefront` and relevant logs. Project package hooks require an installed adapter.

## Version 2: named transports and configuration files

Use the application's published image, ports and health contract. This example demonstrates a generic service with HTTP and native gRPC listeners; adapt the environment names and config filename for the actual application:

```yaml
version: 2
name: service
source:
  image: ghcr.io/acme/service:1.0
primaryEndpoint: http
endpoints:
  http:
    exposure: public
    port: 8081
    route: https://service.apps.example.com
    protocol: http
    streaming: true
    websocket: true
    httpVersions: [h1, h2]
  grpc:
    exposure: public
    port: 50051
    route: https://grpc.apps.example.com
    protocol: grpc
    idleTimeoutSeconds: 600
configs:
  settings:
    file: service.yaml
    mount: /app/service.yaml
secrets:
  API_KEY: secret://service-api-key
resources:
  cpu: "1"
  memory: 1Gi
health:
  path: /health
  timeoutSeconds: 120
dependencies:
  database:
    type: postgres
    source: provision
    storage: 2Gi
bindings:
  DATABASE_URL: database.url
```

Omit unused sections. `source.image` replaces the v1 `image`; named `endpoints` replace singular `port`/`route`/`endpoint`. Do not mix contradictory singular and named definitions. `primaryEndpoint` selects the existing public deployment URL and HTTP readiness check. `--route` changes only that primary; secondary routes must be explicit. The controller must authorize every route domain. The application must bind its listeners to `0.0.0.0` inside the container; Hostlier publishes public ports through private loopback mappings on the controller and routes through its edge.

`protocol` describes the upstream: `http`, `https`, `h2c`, `grpc` or `grpcs`. Native gRPC uses HTTP/2 without JSON translation; preserve its method paths and do not use `proxy.stripPrefix`. HTTP listeners support WebSocket upgrades, SSE, duplex uploads and Streamable HTTP. Exact typed resource paths are proxied without trailing-slash redirects. `httpVersions` describes client-to-edge capabilities: HTTP/1 and HTTP/2 remain enabled; add `h3` for QUIC ingress through a current Caddy edge with UDP 443 available. Direct Nginx modes support native gRPC and HTTP streaming but reject `h3` and generic `h2c`. Explicit `nginx-gateway` mode adds managed Caddy H2/H3 ingress ahead of private Nginx; generic h2c application upstreams remain unsupported. Upstream TLS stays verified; `upstreamServerName` selects its certificate identity. Primary protocols other than plain `http`, private services and workers require an in-image `health.exec` probe rather than a plaintext HTTP probe.

`configs.NAME.file` selects a project-relative UTF-8 file. The CLI snapshots its exact bytes before planning and mounts the immutable revision read-only. Alternatively use `content: "literal text"` or `secret: EXISTING_SECRET_NAME`, with one source per file. Every file needs an absolute `mount`; `mode` defaults to `0444`. Files are limited to 64 KiB each, 32 per component and 256 KiB plaintext per group. Mounts cannot overlap other config files, volumes or kernel/device paths. Files, secret revisions and endpoint maps participate in update/rollback approval. Never put credentials in nonsecret config contents.

Set application credentials with `hostlier secret set NAME --from-env VARIABLE --server NAME` or `--stdin`. Alternatively use flat `secrets` values: `API_KEY: $API_KEY` reads the local environment and is synchronized before planning; `API_KEY: "literal-value"` supplies the value directly. Use `secret://NAME` to reference a stored Hostlier secret, as `API_KEY: secret://service-api-key` above. Do not store the resulting database URL as another secret when using a resource binding: `bindings` injects it transiently under the selected destination environment name (for example `FUSED_DATABASE_URL: database.url`).

A provisioned dependency becomes `<application>-<dependency>` under the same approved deployment plan. To reuse a resource, replace `source: provision`/`storage` with `source: existing`/`instance: RESOURCE_NAME`. PostgreSQL/Redis provide `NAME.url`; Neo4j provides `NAME.uri`, `NAME.username`, `NAME.password`. Optional resource engine packages must be installed on the controller; use `hostlier resource --help` and `hostlier package --help` for inspection and installation. Do not guess unsupported database version flags.

## Optional PostgreSQL sidecar

When the application should own its database lifecycle, use a sidecar instead of `dependencies` and `bindings`. Sidecars share the application network namespace, have separate environment/secret/file/volume scopes, and start before the primary. They permit only `loopback` or `none` exposure. For example:

```yaml
sidecars:
  database:
    source:
      image: postgres:16-alpine
    endpoint:
      exposure: loopback
      port: 5432
    env:
      POSTGRES_USER: app
      POSTGRES_DB: app
    secrets:
      POSTGRES_PASSWORD: secret://service-postgres-password
    volumes:
      data:
        mount: /var/lib/postgresql/data
        size: 2Gi
    resources:
      cpu: "0.5"
      memory: 256Mi
    health:
      exec: [pg_isready, -U, app, -d, app]
      timeoutSeconds: 60
```

The primary reaches this database at `127.0.0.1:5432`. Supply its DSN through an encrypted secret reference, with the same database password and proper URL encoding. PostgreSQL connection credentials must match its declared user/database. Pin a supported database major in the image tag; planning resolves image tags to immutable digests. Persistent volumes use an exclusive stop/start update strategy and remain retained on normal stop/delete. Omit `sidecars` when using managed resource dependencies.

Run protected mutations serially and wait for each CLI command to confirm completion before starting the next. Approvals bind the reviewed controller generation; another completed mutation can make an outstanding review stale. Inspect secret/application/operation metadata before retrying an expired review, and never duplicate an already accepted operation.

## Confined nonroot Docker containers

Version 2 supports an optional restricted `security` policy. It requires numeric nonroot user/group IDs, a read-only root filesystem, dropped Linux capabilities and no-new-privileges. Discover actual IDs from the image's published runtime contract; do not assume a username or grant extra capabilities. Example:

```yaml
security:
  runAsUser: 999
  runAsGroup: 999
  readOnlyRootFilesystem: true
  noNewPrivileges: true
  noSwap: true
  dropCapabilities: [ALL]
  pidsLimit: 1024
  seccompConfig: worker-policy
  tmpfs:
    /tmp:
      size: 256Mi
      mode: 01777
configs:
  worker-policy:
    file: seccomp.json
    mount: /app/worker-seccomp.json
volumes:
  runtime-data:
    mount: /app/data
    size: 2Gi
```

`seccompConfig` names a nonsecret managed config whose immutable JSON bytes are passed to Docker; it is not an arbitrary host path. Use the profile shipped with the exact application image/release. Profiles must be bounded, default-deny (`SCMP_ACT_ERRNO`) with literal syscall names and allow/deny actions. The entire profile participates in plan review and rollback. Omitting `seccompConfig` retains Docker's normal seccomp policy. Privileged mode, unconfined policy, host namespaces, added capabilities and writable container roots are not exposed by this contract. The application runs directly as the declared UID/GID, so select an in-image entrypoint that supports that identity; image wrappers that need root initialization cannot be used unchanged.

Tmpfs mounts are limited to four targets, 1Gi each, always `nosuid,nodev`, and may not overlap managed files, other scratch mounts or persistent volumes. Modes default to `0700`; shared `/tmp` conventionally uses `01777`. Writable runtime files must live in explicit scratch/volume mounts. New Docker volumes copy the image directory's existing ownership; check ownership when reusing an older volume.

A host administrator may install a confined AppArmor profile and select it with `security.appArmorProfile: hostlier-PROFILE_NAME`. The name must start with `hostlier-`; unconfined is rejected. Do not disable host namespace restrictions globally. Kernel/AppArmor denials require administrator diagnosis of the specific application profile. Security requirements are Docker-only in this release; Kubernetes rejects the contract before mutation instead of silently ignoring it. See the actual application's worker docs for its readiness environment flags and subprocess limits.


Version 2 process overrides are top-level literal arrays: `command` maps to the OCI/Docker entrypoint, and `args` maps to the OCI/Docker command arguments. They are separate from `source` and never interpreted by a shell. For an image whose nonroot entrypoint is an init wrapper around its binary:

```yaml
command: [/usr/bin/tini, --, /app/fused-engine]
args: [start]
```

Omit overrides to retain image defaults. The restricted security contract also requires `security.noSwap: true` and an explicit `resources.memory`; Docker's total RAM-plus-swap limit is set equal to its memory limit, allowing no additional swap. CPU/memory values are per-container deployment limits rather than throughput promises; choose the budget for the actual host.

## Explicit traffic policies

Public named endpoints can default-deny unlisted application paths. For native `grpc` or `grpcs`, declare `grpc.services.PACKAGE.Service.methods.Method: {}`. Service and method objects accept `idleTimeoutSeconds`; budgets inherit method → service → endpoint → proxy default. Every service needs 1–64 explicit methods, with at most 32 services and 256 methods per endpoint. Names are case-sensitive, wildcards are rejected, and the proxy preserves the full `/package.Service/Method` path. Unlisted RPCs return gRPC PERMISSION_DENIED.

For HTTP upstreams, use `http.routes.NAME: {path: /exact-path, protocol: http, methods: [GET]}`. Protocol can be `http`, `websocket`, `sse` or `streamable-http`. Each route accepts `idleTimeoutSeconds` and optional `httpVersions: [h1, h2, h3]` as a client admission restriction. Paths are exact and case-sensitive; unknown paths and disallowed methods return HTTP 403. WebSocket supports HTTP/1 GET Upgrade and HTTP/2/HTTP/3 extended CONNECT through Caddy 2.11.7 or newer. Select explicit `httpVersions`: h1 requires GET, h2/h3 require CONNECT, and a mixed route requires both methods. Only `:protocol=websocket` is admitted; generic CONNECT is denied. Direct `nginx` and `nginx-existing` modes support HTTP/1 Upgrade only and reject extended CONNECT requirements before deployment. Explicit `nginx-gateway` mode supports H2/H3 through a managed Caddy frontend and private Nginx backend. SSE permits GET/OPTIONS; Streamable HTTP permits GET/POST/DELETE/OPTIONS and preserves session headers and request/response streams. List only needed verbs. An h3 rule enables QUIC through Caddy, including the frontend in `nginx-gateway` mode; direct Nginx modes reject it.

Keep health paths in an HTTP allowlist when applying it to a primary endpoint. Policies cannot use prefix stripping or private listeners. They do not replace application authentication or implement JSON-RPC/protobuf payload authorization or rate limiting. The complete enclosing endpoint path remains reserved even for denied requests. Plans identify the existing app, endpoint and URL on ownership conflicts; never work around the conflict by selecting another protocol or verb. More specific sibling endpoints cannot capture another endpoint's explicit permission.

Example addition to an existing gRPC endpoint:

```yaml
grpc:
  services:
    engine.v1.EngineService:
      methods:
        Connect: {}
        SubscribeWebhooks:
          idleTimeoutSeconds: 3600
```

Example addition to an existing HTTP endpoint:

```yaml
http:
  routes:
    health: {path: /health, protocol: http, methods: [GET]}
    socket: {path: /ws, protocol: websocket, methods: [GET]}
    events: {path: /events, protocol: sse, methods: [GET]}
    mcp: {path: /mcp, protocol: streamable-http, methods: [GET, POST, DELETE]}
    quic: {path: /quic, protocol: http, methods: [GET], httpVersions: [h3]}
```

## WebSocket over HTTP/2 and HTTP/3

```yaml
http:
  routes:
    socket:
      path: /ws
      protocol: websocket
      methods: [GET, CONNECT]
      httpVersions: [h1, h2, h3]
```

Use `methods: [CONNECT]` and `httpVersions: [h2]` or `[h3]` for a single-protocol resource. Omitting `httpVersions` retains the existing HTTP/1 GET Upgrade behavior. Protocol negotiation uses RFC 8441 (H2) and RFC 9220 (H3), with actual WebSocket frames inside the open CONNECT stream. Ordinary GET requests cannot enter CONNECT-only routes, and extended CONNECT cannot enter ordinary HTTP, SSE or Streamable HTTP rules. Existing hostname/path ownership and exact case-sensitive path checks apply across all transports. Caddy 2.11.7 or newer is required; the distribution’s older default Caddy package must be upgraded before requesting this capability.

## Managed gateway for Nginx

Select `nginx-gateway` explicitly when Nginx must remain in the application path and clients need H2/H3 WebSockets. Caddy owns public TCP/UDP 443 and certificates, converts extended CONNECT into HTTP/1 WebSocket Upgrade, then forwards through a dedicated Nginx master on loopback. Nginx proxies to the application's private upstream. Native gRPC uses a separate private h2c listener; HTTP, SSE and Streamable HTTP use the private HTTP/1 listener.

On the controller server, first retain an existing Hostlier-managed Caddy installation and upgrade its executable to Caddy 2.11.7 or newer. Review `hostlier-controller init --domain example.com --proxy nginx-gateway --dry-run`, then run the same command without `--dry-run` to approve installation. `init` installs `hostlier-nginx-gateway.service`, retains Caddy's certificates and routes, and restarts the controller with the explicit mode. Publish an existing deployment through the normal approved CLI flow to render all owned routes through the new backend. The project policies above work without changes. This mode is never selected by `auto` and does not adopt an external Caddy or replace an external `nginx.service`.

The dedicated backend listens only on `127.0.0.1:14443` (HTTP/1) and `127.0.0.1:14444` (h2c), under `/etc/hostlier/nginx-gateway`. The gateway overwrites original-method, protocol and scheme headers; Nginx checks those claims against the approved resource policy again after bridging. These are trusted local transport fields: keep those ports private and do not insert an untrusted local proxy. All application traffic traverses Nginx; an unavailable backend fails closed. Generic h2c application upstreams remain unsupported by this Nginx mode. UDP 443 must be reachable for HTTP/3; the installer does not silently modify an existing firewall to permit QUIC.

Direct `nginx` and `nginx-existing` modes retain their existing HTTP/1 WebSocket behavior. Stock Nginx does not supply the extended CONNECT bridge; this mode explicitly manages the additional Caddy frontend.
