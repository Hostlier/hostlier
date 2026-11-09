# Managed Nginx gateway verification — 2026-10-30

Implemented the explicitly selected `nginx-gateway` proxy mode and exercised it on the existing `hostlier-cli-deploy` Ubuntu 24.04 ARM64 VM. Public Caddy 2.11.7 terminates TLS, HTTP/2 and QUIC/HTTP/3; a dedicated stock Nginx 1.24.0 master performs the application proxying. No patched Nginx distribution is required. Direct `nginx` and `nginx-existing` modes retain HTTP/1 WebSockets and continue rejecting unsupported extended CONNECT policies.

```text
H2/H3 WebSocket client → Caddy public TLS/QUIC → private Nginx HTTP/1 Upgrade → application
Native gRPC client    → Caddy public TLS/H2   → private Nginx h2c           → application
```

## Implementation and setup

`hostlier-controller init --proxy nginx-gateway` is an explicit addition of a private Nginx backend to an existing Hostlier-owned Caddy installation. The installer checks ownership and actual Caddy executable compatibility, presents both services in its review, retains existing Caddy route/certificate state, creates `hostlier-nginx-gateway.service`, and selects the same mode in the separate controller executable. It never selects this topology through `auto`. See [configuration and prerequisites](guides/traffic-policies.md#managed-gateway-for-nginx).

The backend owns only `/etc/hostlier/nginx-gateway` and loopback TCP 14443 (HTTP/1) and 14444 (h2c). Invalid/public listener addresses and invalid ports are rejected. The frontend overwrites original-method, client-protocol and public-scheme metadata. Nginx evaluates the approved exact-path/method/protocol policy after conversion, preserves the public authority/scheme, and retains verified upstream TLS. Policy publication updates Nginx first, then the frontend, restoring the previous backend if frontend publication fails. Health checks and deployment preflight require both private listeners. Generic h2c application upstreams remain unsupported by this Nginx mode.

The existing disposable VM used the production-generated private service/bootstrap assets, then the rebuilt controller and the authenticated CLI to publish `protocol-probe`. Its existing authorized test-only authenticator approved the exact deployment. The final successful operation is recorded in [state.json](verification/nginx-gateway/state.json). The public distro `nginx.service` was not adopted or changed.

## Real VM checks

The [complete final log](verification/nginx-gateway/vm.log) records:

- Protocol-pinned HTTP/2 and HTTP/3 extended CONNECT returning 200, with masked text and binary WebSocket frames, ping/pong, close handshakes and a reopened tunnel.
- A forged CONNECT/H3 header request to the GET/H1-only WebSocket route succeeding only after the frontend overwrote the false claims with the actual GET/H1 metadata.
- An actual private Nginx outage: both protocol-pinned WebSocket requests returned HTTP 502 while Nginx was stopped. The gateway never routed directly to the application. Independent bounded cleanup restarted Nginx, and all subsequent protocol tests passed.
- Wrong extended protocol, mismatched HTTP version, wrong case, descendants, ordinary CONNECT and unlisted HTTP resources denied.
- Existing HTTP/1 WebSockets, HTTP, SSE incremental delivery, Streamable HTTP session/header/method behavior, duplex requests, native gRPC health streams and HTTP/3-only routing passing through both layers.
- Fused Engine SDK and Bridge routes reaching their respective handlers, and unlisted RPCs denied. These checks assert Engine's expected validation/authentication errors, rather than claiming authenticated SDK/Bridge app execution.

Fused Engine's original revision and PostgreSQL container identity were retained. Its trusted-TLS health response remains `ok` with Unified App workers ready. The deployment probe is disposable and separate from Engine's data.

## Local checks

The Nginx, runtime, installer and controller-command packages passed under the race detector; affected-package vet passed. Both the separate Linux controller and local CLI built successfully. The repository quality gate reports existing findings in other files; it reports none in the gateway implementation. Evidence: [race results](verification/nginx-gateway/race.log), [vet result](verification/nginx-gateway/vet.log), [quality output](verification/nginx-gateway/quality.log).

The protocol-pinned live client uses `go test -tags=http2legacy -c` for the documented x/net/http2 extended CONNECT API, as explained in [the initial WebSocket verification](websocket-extended-verification.md). All live requests verify TLS against the VM test CA. HTTP/3 tests use QUIC directly and cannot fall back to TCP.
