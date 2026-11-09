# Named endpoint verification — 30 October 2026

The disposable Lima VM `hostlier-ui-outage` ran the actual Docker runtime, controller, approval flow and edge. Existing `coexist-new` and collector containers stayed running. Endpoint rendering and file manifests for Kubernetes were tested in Go; no live Kubernetes cluster was used for this task.

## Live routing matrix

| Check | Caddy 2.11.7, port 443 | Nginx, isolated port 18443 |
| --- | --- | --- |
| HTTP and mounted revision bytes | Pass | Pass |
| WebSocket upgrade and bidirectional echo | Pass | Pass |
| SSE first event before delayed completion | Pass | Pass |
| Streamable HTTP initialize/session header, streaming POST and GET, DELETE | Pass | Pass |
| Exact `/mcp` and native RPC method routes without slash redirects | Pass | Pass |
| Duplex response before request upload completes | Pass | Pass |
| Native gRPC health unary response | Pass | Pass |
| gRPC server streaming, early message and final trailers | Pass | Pass |
| HTTP/3 using QUIC-only client, verified TLS | Pass, including after update/rollback reload | Explicitly unsupported by adapter |

The protocol fixture is a separately deployed workload with its script and revision file delivered through managed configuration. Its transport tests use grpc-go, coder/websocket and quic-go in an isolated test module. The Streamable HTTP fixture tests forwarding semantics, rather than claiming full MCP application conformance.

Source: [protocol probe](../internal/runtime/testdata/endpoint-probe/) and [live Nginx test](../internal/nginx/endpoint_live_test.go).

## File lifecycle

The actual CLI deployed `revision-one`, updated the managed file to `revision-two`, and rolled back to the original retained revision through independent fixture-authenticator approval. The served bytes returned to `revision-one`; both HTTP and gRPC routes remained available. The complete Caddy protocol matrix passed after rollback.

Unit/integration checks cover project-relative sources and escaping symlinks, source size bounds, route reservations from pending operations, strict JSON request capacity, file mode/mount validation, immutable Docker source inodes, component-scoped secret references, missing Kubernetes file-secret rejection, and immutable ConfigMap/Secret mounts, and preservation of read-only configuration modes behind private directories during controller recovery.

## Fused Engine

Image: `ghcr.io/usefused/engine@sha256:76b161d3819811848e939b34b1108ac1dae8b122311788ffa8b1d05bd46ce288`.

Hostlier deployed Engine and PostgreSQL through its normal approved operation workflow. Engine consumes `/app/engine.yaml` as a managed file and encrypted license, database and encryption references. Its two public named endpoints are `https://engine.apps.hostlier.test` and `https://grpc.apps.hostlier.test`, with database port 5432 remaining loopback inside the application group.

A dedicated license was issued through the user's existing local Registry setup API. Its licensing-only fixture lacks the GraphQL catalogue required for Engine startup. With explicit user approval, a temporary loopback-only full-catalogue Registry harness runs on the Mac at `127.0.0.1:59904`, against the same existing local Registry database. Engine accesses it through the Lima host address in development mode. The ordinary local Registry fixture remains running. No private credential values are stored in this report or repository.

Engine completed workspace/bootstrap Owner initialization and embedded NATS startup. Certificate-verified `/health` returned `status: ok`. A native gRPC client reached `/engine.v1.EngineService/Connect`; Engine returned its expected rejection for missing app credentials. This verifies the real handler and gRPC status path, not successful authenticated SDK execution. Engine health also passed over a QUIC-only HTTP/3 client. Its native gRPC handler was verified through generated Nginx routing on port 18443 as well.

Engine reports `unified_app_worker_ready: false`. The matching container seccomp profile/host namespace support has not been configured through Hostlier; Unified App execution is not verified. This is separate from the working Engine HTTP and native gRPC listeners.

## Local checks

The changed config/model/controller/CLI/runtime/Nginx packages pass normal tests and the race detector. UI tests pass (31), embedded UI assets build, and targeted `go vet` passes. The repository-wide quality scan has no findings in this change; it still reports pre-existing findings in workflow, package-executor fixture and observation-recovery tests.
