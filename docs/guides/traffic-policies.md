# Traffic policies

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

The dashboard shows effective permissions and timeout inheritance on deployment details. Policies participate in immutable revision fingerprints, approval review and rollback. Existing endpoints without policies keep their forwarding behavior. All policy paths must lie within the enclosing endpoint route. A path appears in only one HTTP rule; method wildcards and path prefixes are not supported. Explicit method/path changes require a new reviewed deployment.

To route Bridge and SDK traffic separately on the same Engine port, retain the primary HTTP endpoint and configure:

```yaml
endpoints:
  sdk-grpc:
    exposure: public
    port: 50051
    route: https://grpc.example.com
    protocol: grpc
    grpc:
      services:
        engine.v1.EngineService:
          methods:
            Connect: {}
            SubscribeWebhooks: {}
  bridge-grpc:
    exposure: public
    port: 50051
    route: https://grpc.example.com/fused.bridge.v1.BridgeService
    protocol: grpc
    grpc:
      services:
        fused.bridge.v1.BridgeService:
          idleTimeoutSeconds: 3600
          methods:
            Connect: {}
```

Both clients use `grpc.example.com:443`; generated clients supply the full service/method path. The endpoint route is an ownership boundary, not a prefix to add to client methods. Service, method and HTTP route paths are case-sensitive. There is no proto upload or reflection dependency.

Per-route `httpVersions` restricts the incoming client protocol, while endpoint `httpVersions` declares shared edge capabilities. A route requiring h3 enables the Caddy UDP listener and rejects TCP requests to that resource. Permit UDP 443 through the server firewall. Use a current Caddy release: the earlier VM test found QUIC reload failures in 2.6.2. Caddy bridges HTTP/2 and HTTP/3 extended CONNECT to the application’s existing HTTP/1 WebSocket server; the client connection retains its selected H2/H3 protocol. Set `GODEBUG=http2xconnect=1` on `caddy.service` and restart Caddy to advertise HTTP/2 SETTINGS_ENABLE_CONNECT_PROTOCOL. Managed installation units include this setting; older installations must add it. The controller verifies the edge version and actual HTTP/2 SETTINGS before replacing a workload. This does not enable generic CONNECT tunnels.

SSE and Streamable HTTP flush responses incrementally. Streamable HTTP forwards content negotiation, protocol/session headers, request bodies and the application's response unchanged; the application owns session semantics. Explicit OPTIONS forwarding does not install CORS. Ordinary HTTP and SSE/Streamable HTTP rules suppress WebSocket upgrades. Idle budgets are upstream I/O budgets rather than application deadlines or rate limits; WebSocket tunnel lifetime is not controlled by a whole-call deadline.

## Path ownership

Another application cannot claim an endpoint's path, a parent path or a descendant path by selecting a different HTTP verb or streaming protocol. The full enclosing endpoint scope remains reserved, including denied paths. Active and accepted pending deployments hold the same reservations. A conflict names the existing application, endpoint, existing URL and requested URL; choose another host or path. Within one application, a more specific endpoint cannot intercept another endpoint's explicitly allowed policy path. Validation names both endpoints and the exact shadowed resource. Existing unrestricted parent/child routes within one application remain compatible; inspect all scopes because a child policy does not restrict an unrestricted parent.

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
