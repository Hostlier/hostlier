# Fresh-agent CLI deployment — 30 October 2026

A subagent started with **no conversation history or Hostlier repository context**. It received the CLI executable path, an isolated workspace, the deployment objective and permission to read the CLI's bundled skills. The parent prepared the Linux VM/controller, private test CA, optional PostgreSQL package, local Registry license and the explicitly user-authorized disposable test authenticator. The agent used the CLI to discover/install skills, connect, upload secrets, validate, plan, deploy, inspect status/logs and update the application. It did not deploy through Docker/Compose or handwritten controller APIs.

## Infrastructure and outcome

- Fresh Lima VM: `hostlier-cli-deploy`, Ubuntu 24.04 arm64, 2 CPUs, 3 GiB RAM, 12 GiB disk, no host filesystem mounts.
- Controller: `https://hostlier.cli.127.0.0.1.nip.io:18444`; external port 18444 forwards to guest HTTPS 443. The previous test VM/controller was left untouched.
- Isolated CLI profile: `cold-cli`, under `/private/tmp/hostlier-fresh-cli-deploy/state`.
- Application: `fused-engine`, Engine 0.62.16, image digest `76b161d3819811848e939b34b1108ac1dae8b122311788ffa8b1d05bd46ce288`.
- Managed dependency: `fused-engine-database`, private PostgreSQL 16.11, requested storage 2 GiB. Docker storage quota enforcement remains false; ordinary deletion retains data.
- Initial deployment operation: `d23019f6897626bfecd5b32030f99a10671e3e058af8a4a661a95dc56863567b`.
- Corrective update operation: `1328a2ac52d731441279d0ad9de7a0efba04341a9dac3e0d0e705e5dc1efaa22`.

Both operations completed through the CLI after separate exact-plan approvals. The software test authenticator is opt-in, confined to this guest's root-owned fixture, excluded from the production CLI binary and was explicitly approved by the user. It is not a claim of physical FIDO verification. The PostgreSQL runtime identity remained unchanged across the application replacement.

## Verification

The agent verified external endpoints with the supplied private CA; no TLS verification was disabled.

- `https://engine.cli.127.0.0.1.nip.io:18444/health`: HTTP 200, Engine `status: ok`.
- `grpc.cli.127.0.0.1.nip.io:18444`: native HTTP/2 gRPC call to `/engine.v1.EngineService/Connect`, empty protobuf request and no credentials, returned `INVALID_ARGUMENT` with `SDK app ID must be a valid UUID`. This proves routing to the actual Engine handler; authenticated SDK execution was not tested.
- CLI application status: running, primary component ready.
- CLI database status: running, same runtime ID before/after the update.
- Engine logs: database initialization and Registry handshake succeeded; gRPC listener now binds `0.0.0.0:50051`.

Engine reports `unified_app_worker_ready: false`. Its optional sandbox workers were not configured or verified. This is a disposable development deployment using the temporary local Registry on the Mac at port 59904; it is not a production readiness assessment. Nonfatal Engine OTLP exporter attempts target an absent localhost collector.

## Issues found through cold-start use

Fixed during this exercise:

1. Command-specific help returned only the global command list. `COMMAND --help` and `help COMMAND` now expose the corresponding offline bundled skill.
2. The deploy skill lacked a complete version 2 schema. Bundled instructions now cover named HTTP/gRPC endpoints, managed files, encrypted environment references, database dependencies/bindings and sidecars.
3. Public namespace-image metadata redirected through a verified HTTPS mirror, but the registry client rejected every redirect. Anonymous HEAD metadata requests now permit bounded HTTPS redirects without credentials or cookies; authenticated requests and token exchanges retain the original no-redirect rule. Tests verify credential isolation, TLS-downgrade rejection and sanitized diagnostics.
4. Registry failures did not identify the failing image or HTTP status. Errors now include a valid image reference and safe status information, without provider bodies, signed redirect URLs or credentials.
5. Concurrent secret reviews could become stale when another mutation completed. The agent inspected metadata, retried only the missing write and then serialized protected mutations. Bundled deploy/secret guidance now explains that generation-bound approval behavior.

Remaining usability gaps:

- Resource plan/package metadata exposes the pinned PostgreSQL digest but not its human-readable major version. The parent independently checksum-verified the public OCI index, arm64 manifest and configuration: `PG_MAJOR=16`, `PG_VERSION=16.11`.
- Fused Engine's runtime skill did not document its default loopback gRPC bind or the application-specific probe. The agent diagnosed HTTP 502 through CLI logs, then applied the documented Engine setting `FUSED_ENGINE_GRPC_HOST=0.0.0.0` through another approved CLI update. Standard health/reflection RPCs are not registered in this Engine build, so verification used its actual Connect method.

## Reinspect the deployment

The local wrapper includes its isolated profile directory and private CA path:

```sh
/private/tmp/hostlier-fresh-cli-deploy/hostlier status fused-engine --server cold-cli
/private/tmp/hostlier-fresh-cli-deploy/hostlier resource status fused-engine-database --server cold-cli
```

Nonsecret execution artifacts are in `/private/tmp/hostlier-fresh-cli-deploy`: `.hostlier`, `deployment-report.txt`, `deployment-evidence.json`, `verification.json` and the read-only `verify-engine.py` probe. Credential files remain protected outside these reports. Relevant CLI/runtime tests pass under the race detector, vet passes, and the changed files introduce no new repository quality findings.
