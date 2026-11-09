# Traffic policy verification — 2026-10-30

Implemented and deployed on the disposable `hostlier-cli-deploy` Linux VM. The normal CLI submitted reviewed `.hostlier` deployments, using the previously authorized VM-only software authenticator. No production approval requirement was removed.

## Real Fused Engine routing

The final Engine operation/revision is `82ed486832a6b7e495abea5e5c0a971c70285febc679260caa1bc50df19077e8`. The named `sdk-grpc` and `bridge-grpc` endpoints share Engine port 50051 and the authority `grpc.cli.127.0.0.1.nip.io`; the Mac forwards port 18444 to the VM's TLS listener. Bridge owns `/fused.bridge.v1.BridgeService`, and SDK owns the root fallback with an explicit `engine.v1.EngineService` method allowlist. All seven published SDK methods remain listed in the final deployment, including `SubscribeWebhooks`.

Both Caddy and an independently generated Nginx configuration reached Engine's real handlers:

- SDK `Connect`: expected `INVALID_ARGUMENT`, `SDK app ID must be a valid UUID` for an empty request.
- Bridge bidirectional `Connect`: expected `UNAUTHENTICATED`, `bridge credential required` without an installation credential.
- SDK bidirectional `SubscribeWebhooks`: expected `INVALID_ARGUMENT`, `expected initial subscribe message` after closing the request side without a subscription.
- Unlisted services/methods, wrong case and descendant RPC paths: edge-generated `PERMISSION_DENIED`, without falling through to the root endpoint.

These checks prove routing, framing, bidirectional method availability and preservation of application validation/authentication. They do not claim an authenticated Bridge installation, provider execution through a generated SDK, or a webhook delivery. An initial negative test deliberately omitted the real SDK webhook method and verified denial; that method was then explicitly restored and its handler was verified before finishing.

Engine's HTTP UI route remains available at `https://engine.cli.127.0.0.1.nip.io:18444`. Trusted-TLS health is `ok` with `unified_app_worker_ready: true`. The original PostgreSQL runtime ID is unchanged. Application credentials, database data and confinement settings were retained.

## Streaming and HTTP/3 policies

The separate `protocol-probe` application was deployed through Hostlier at `probe.cli.127.0.0.1.nip.io`, with a separate gRPC health endpoint. It uses the actual VM edge and containers, plus a deliberately simple transport fixture rather than claiming to be a production MCP application.

Passing checks include HTTP, real WebSocket upgrade and bidirectional echo, early SSE chunks, Streamable HTTP initialize/session headers/POST+GET streaming/DELETE cleanup, duplex uploads, native protobuf health Check and Watch with final trailers, and default-deny HTTP path/method/upgrade cases. A QUIC-only client reached `/quic` over actual HTTP/3; TCP requests to the same path were denied. There is no TCP fallback in that check.

Caddy 2.11.7 serves the installed edge. Nginx 1.24.0 validated and served the generated policies on isolated loopback ports 19080/19443, then shut down. The ordinary Nginx system service remains disabled. Nginx rejects HTTP/3 policy requirements before deployment; the isolated Nginx fixture omitted the QUIC-only rule rather than pretending to support it. WebSocket extended CONNECT over HTTP/2 or HTTP/3 is explicitly unsupported by this policy contract.

## Path ownership

A real CLI plan attempted to deploy `conflicting-service` onto the occupied SSE path and was rejected before deployment:

```text
Route https://probe.cli.127.0.0.1.nip.io/sse conflicts with application "protocol-probe" endpoint "http" at https://probe.cli.127.0.0.1.nip.io; choose another hostname or path
```

Controller tests cover pending reservations. Configuration tests reject sibling endpoints that would shadow explicit policy paths. Denied resources still remain inside the owning endpoint's full reservation. Switching HTTP methods or streaming protocols does not transfer ownership.

## Fixes found by live clients

- Corrected the Caddy HTTP/1 matcher to `http/1.1`; `http/1` did not admit valid WebSocket upgrades.
- Removed forced flushing from native gRPC proxies. Immediate header flushing split trailers-only Engine errors into a header block and empty DATA, causing grpc-go to report missing trailers. Native HTTP/2 streaming now owns its flush semantics; successful streaming and initial error statuses both pass.
- Corrected Nginx lowering for endpoints mixing WebSocket and HTTP/SSE/Streamable HTTP routes. Each lowered rule now validates its own transport settings, and rendering errors propagate instead of silently omitting allowed paths.

## Recorded checks and evidence

- Complete Hostlier Go suite passed; affected config/model/runtime/Nginx/controller/CLI packages passed under the race detector; affected-package vet passed.
- All 33 UI tests passed, and embedded assets were rebuilt. Deployment details show effective permission paths, methods, client protocol restrictions and inherited idle budgets.
- 11 live Caddy client tests passed; 10 live Nginx client tests passed after `nginx -t`.
- Repository quality still reports findings outside the traffic-policy code. Unrelated controller/CLI packaging edits appeared concurrently after the recorded full-suite run; they were left untouched. Binary hashes identify the actual tested/deployed builds.

See [evidence summary](verification/traffic-policies/summary.json), [Caddy results](verification/traffic-policies/caddy.log), [Nginx results](verification/traffic-policies/nginx.log), [full Go suite](verification/traffic-policies/go-tests.log), [race results](verification/traffic-policies/race-tests.log), [quality findings](verification/traffic-policies/quality.log), [path conflict](verification/traffic-policies/path-conflict.json), and [final Engine state](verification/traffic-policies/engine-status.json).

The installed Linux controller SHA256 is `38dcac9696ec168985ee1e7d75a00e849950abe671f1d46361f1752fdc1ff29e`. The matching local CLI build is `/private/tmp/hostlier-traffic-macos`, used by `/private/tmp/hostlier-fresh-cli-deploy/hostlier` and its isolated `cold-cli` profile. Test project files remain in that directory. The private CA and all credential files stay outside the repository.

Configuration reference: [traffic policies](guides/traffic-policies.md); complete Engine example: [deployment file](../examples/fused-engine/.hostlier).
