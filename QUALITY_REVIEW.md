# Hostlier Go quality review — 2026-10-29

The review and refactor cover the standalone `hostlier/` Go module, including tests, the quality checker, the optional database/Aiven/Railway configuration packages and their standalone preview command. Sol 6.1 subagents reviewed and refactored separate areas; the parent integrated the changes and ran the combined checks. The parent Hosting SDK and iteration 3 scope were not changed.

## Measured result

The initial review found 65 functions above cyclomatic complexity 10. The largest was CLI `RunWithOptions` at 283. The reviewed core snapshot contained 95 Go files and 770 named functions/closures, with a maximum of 10 and zero violations. The optional package-preview code was initially preserved pending ownership clarification and was subsequently included in the manual review at the user’s request. The final whole-tree gate, including the compatibility and SSE regressions, checks 137 files and 1,059 functions/closures and still reports a maximum of 10, zero complexity violations and zero missing comments. Independent `gocyclo` v0.6.0 also reports zero functions above 10; closures contribute to their enclosing function in that tool and are additionally checked individually by the repository checker.

| Function | Before | After |
| --- | ---: | ---: |
| CLI `RunWithOptions` | 283 | 10 |
| Controller `dispatch` | 172 | 9 |
| Installer `Inspect` | 79 | 5 |
| Installer `Apply` | 63 | 8 |
| Docker runtime `Deploy` | 73 | 5 |
| k3s runtime `Deploy` | 42 | 5 |
| Configuration `ValidateProject` | 71 | 3 |

Complexity counts begin at one and add decisions for conditions, loops, nondefault switch/select branches and short-circuit boolean operators. Tests independently check those language constructs, nested closures, test-file inclusion, ignored build/dependency directories and invalid-source rejection.

## Changes

- Resumable SSE separates operation execution from progress observation, with durable cursors, bounded authenticated streams, replay, retention notices and verified TLS. The CLI can explicitly reattach to operations and preserves stderr progress versus stdout result boundaries. See [STREAMING.md](STREAMING.md).
- CLI command dispatch uses focused handlers. App/resource planning shares preview, interactive approval, stale-plan refresh, idempotent apply and operation watching. Lifecycle actions and log following share helpers.
- Controller transport, authentication, enrollment, secrets, domains, approval plans and runtime execution are separated. Operation execution makes state-lock boundaries and durable transitions explicit.
- Docker and k3s deployments use transaction state with small preparation, storage, workload, readiness, route and rollback stages. Installer inspection, assets, firewall analysis and application are separate.
- Resource provisioning, lifecycle, engine-native backup, offline restore and maintenance transport have focused helpers. Repeated ownership, readiness and credential checks are centralized where their contracts match.
- Controller snapshots, identity records, secret records, deployment schemas, client transport types and profile persistence live with their owning concern. Shared cross-package contracts remain in `internal/model`.
- Existing k3s resource Deployments retain their recognized immutable selector, supporting both historical ownership-only selectors and current selectors with the engine role. Pod and Service role labels remain intact; unexpected selectors and unowned objects are rejected before apply. Regression cases exercise both compatible layouts and both rejection paths.
- State and encrypted blobs share a protected atomic-write implementation. Profile storage is separate from HTTP transport. Test scenarios use named helpers while retaining their assertions and gate names.
- Optional Aiven/Railway packages share lifecycle-mode and token-reference validation through `packages/database`; provider placement and capability lists stay provider-owned. Configuration and preview types remain with their respective contracts, and the preview CLI owns its narrow planner interface.
- Offline previews explicitly require application credential isolation for every engine and ownership checks before reusing named databases in either mode. The command's file-to-error boundary is tested for secret-safe diagnostics, empty output on rejected input and output-write failures. These packages still perform no cloud provisioning or controller integration.
- Function/type documentation and comments on logical control-flow statements explain validation, persistence, approval, ownership and recovery decisions. The checker reports zero missing comments. It checks comment presence; reviewers remain responsible for their accuracy and usefulness. Ordinary assignments are not individually required to carry comments.

The review retained verified HTTPS, hidden secret input, per-deployment approval, single-use pairing, protected storage, immutable plans, idempotent acceptance, private resource networking and conservative interrupted-operation recovery. This is a maintainability review, not a claim that all possible defects or duplication have been eliminated.

## Repeatable gate

```sh
cd hostlier
make quality
make check
make build linux
```

`make quality` uses only the Go standard library. It includes production, tests and its own source, checks every function against the ceiling of 10, and requires nearby comments for functions, named types and explicit control-flow branches. `make check` also runs `go vet ./...` and `go test -race ./...`. Tests with HTTPS/Unix listeners require local socket permission.

For an independent complexity check, when gocyclo is installed:

```sh
gocyclo -over 10 .
```

## Verification during this review

- Whole-tree quality gate and independent gocyclo check: passed.
- `go vet ./...` and full `go test -race ./...`: passed, including real HTTPS client/controller disconnect-and-resume integration, parser/replay limits, revocation, persistence failures and terminal operation handling.
- Native macOS ARM64 and static Linux amd64/arm64 builds: passed.
- Built optional preview CLI: all four Aiven/Railway example configurations passed file-to-JSON execution. Package race tests and vet passed, including provider boundaries, immutable preview snapshots, strict YAML input and credential-publication ordering.
- Real local Docker/Caddy lifecycle E2E: passed (8.03 s).
- Real Docker PostgreSQL, Redis and Neo4j authentication, persistence and independent backup/restore: passed (134.21 s total).
- Packaged Go CLI over verified HTTPS, PTY-approved PostgreSQL provisioning, encrypted backup, protected export and approved separate-target restore: passed (9.10 s).
- Installed k3s server installer rerun with owned firewall rules: passed.
- Final SSE-enabled installed ARM64 k3s gate: approved deployment, default progress watch, restart, immediate HTTPS and Redis persistence passed. Explicit operation reattachment returned one terminal stdout result plus sanitized cursor-bearing stderr progress. The disposable VM was stopped after verification.
- Real k3s PostgreSQL, Redis and Neo4j authentication, lifecycle persistence and independent backup/restore: passed (433.62 s total; 69.64 s, 110.99 s and 252.98 s respectively).
- Installed k3s app redeployment with an existing Redis dependency: passed twice after the immutable-selector fix, preserving original data. The earlier failed attempt left the tracked resource status failed; an explicit start refreshed its status before retrying.
- Installed k3s restart readiness: **fixed and verified**. Start/restart now reload Caddy, wait for certificate-verified public HTTPS, then persist success. Kubernetes rollout readiness alone was insufficient while Service endpoints converged. Two consecutive full installed-server deploy/restart runs now pass immediate HTTPS verification and Redis persistence checks. Regressions cover transient 502 recovery, cancellation preserving durable state, and untrusted TLS rejection.

Prior fresh-server and reboot results remain in [VALIDATION.md](VALIDATION.md). Public DNS/ACME and fresh amd64 execution are outside this local review; iteration 3 scaling remains proposed rather than implemented.
