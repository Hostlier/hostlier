# WebSocket extended CONNECT verification — 2026-10-30

Implemented and verified on the existing `hostlier-cli-deploy` Linux VM using Caddy 2.11.7. The controller and the normal CLI now support explicit HTTP/2 (RFC 8441) and HTTP/3 (RFC 9220) WebSocket policies. Caddy bridges client CONNECT streams to an application's existing HTTP/1 WebSocket server; the client remains on its selected H2/H3 transport.

```yaml
http:
  routes:
    socket:
      path: /ws
      protocol: websocket
      methods: [GET, CONNECT]
      httpVersions: [h1, h2, h3]
```

H1 requires GET; H2/H3 require explicit CONNECT permission. Omitting `httpVersions` preserves the existing HTTP/1 GET-only behavior. H2-only and H3-only routes use `methods: [CONNECT]`. Hostname/path ownership, exact case-sensitive resources and default-deny behavior are unchanged. Generic CONNECT is never enabled by these rules.

The VM's `protocol-probe` fixture was updated through the authenticated CLI and the previously authorized disposable test authenticator. `/ws` retains HTTP/1, `/ws-h2` requires HTTP/2, and `/ws-h3` requires HTTP/3. No Fused Engine configuration or data was replaced. Engine health remains `ok`, Unified App workers remain ready, and the fixture's final revision is recorded in [state.json](verification/websocket-extended/state.json).

## Live results

- HTTP/2-only client: real CONNECT handshake with HTTP/2 200, masked text and binary frames, ping/pong, close handshake, and a second successfully opened tunnel.
- QUIC-only client: the same frame/control assertions with HTTP/3 200; no TCP or HTTP/1 fallback is available in this client.
- Wrong `:protocol`, cross-version paths, wrong case, descendants and CONNECT on an ordinary HTTP resource: HTTP 403 over the requested transport.
- Existing HTTP/1 WebSocket echo, SSE chunking, Streamable HTTP sessions, duplex uploads, native gRPC health streams, real Engine SDK/Bridge routes and policy denials still pass.

See the [complete VM log](verification/websocket-extended/vm.log). The probes use actual HTTP/2 and HTTP/3 pseudo-headers and exchange RFC 6455 frames while both CONNECT halves remain open. They do not infer support from Alt-Svc or a successful ordinary HTTP request.

## Capability checks and compatibility

The managed Caddy service now sets `GODEBUG=http2xconnect=1`. The existing VM received the corresponding Caddy service drop-in and restart, preserving other service configuration. Before replacing a workload that requests H2/H3 WebSockets, the controller requires Caddy 2.11.7 or newer; H2 requests additionally require a verified-TLS, actual SETTINGS_ENABLE_CONNECT_PROTOCOL=1 response. That probe sends no application request and has a bounded timeout. Older installations must upgrade their edge and enable this setting; unsupported deployments fail before stopping the existing workload.

Direct `nginx` and `nginx-existing` modes explicitly reject H2/H3 WebSocket requirements both during runtime preflight and standalone rendering. HTTP/1 Upgrade remains supported. The separately selected `nginx-gateway` mode now supports H2/H3 through a managed frontend: see [gateway verification](nginx-gateway-verification.md).

The live client is built with `go test -tags=http2legacy -c` because Go 1.27's replacement for x/net/http2 rejects the legacy documented `Header[":protocol"]` API. The tag selects x/net's maintained RFC 8441 transport. This is a client-build compatibility choice; it does not bypass server SETTINGS, TLS verification or policy admission.

## Local validation

Affected configuration, runtime, Nginx and installer packages passed with the race detector; targeted capability/preflight tests and vet passed. New source files satisfy repository quality rules. The repository-wide quality check continues to report unrelated findings outside this implementation; its output is retained alongside the [race results](verification/websocket-extended/race.log).

Configuration reference: [traffic policies](guides/traffic-policies.md). Test implementation: [extended WebSocket probes](../internal/runtime/testdata/endpoint-probe/websocket_extended_test.go).
