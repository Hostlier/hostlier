# Real Unified App worker execution — 30 October 2026

The same subagent that deployed Fused Engine without prior Hostlier context configured its workers through the Hostlier CLI, then used `fused-cli` to import a private read-only GitHub operation, compile/deploy a TypeScript Unified App, and execute it against actual GitHub responses. The parent handled VM preparation, the user-authorized disposable authenticator, implementation of missing Hostlier settings and an independent kernel/runtime audit.

## Result

- Fresh VM: `hostlier-cli-deploy`; existing controller/database retained.
- Engine `/health`: HTTP 200, `status: ok`, **`unified_app_worker_ready: true`**, verified with the private CA.
- Native Engine gRPC handler still passes its expected unauthenticated UUID rejection check.
- Worker update: `5ab2f7ce18715aac814028239bf6cbc4f0be98848caae3b8410a011ac5a6dcb1`.
- Active app: `cold-cli-worker-proof@1.0.0`, ID `83618b2a-7328-5d7c-832c-723d0ec72095`.
- The original PostgreSQL runtime and Engine license/encryption secret revisions were preserved.

The app calls the selected `getPublicRepository` operation, validates the returned data, and returns a transformed repository name, public/private flag and source URL. Actual successful execution receipts:

| Execution | Input | Returned repository |
| --- | --- | --- |
| `30c783f8-6d0f-48f8-be6c-aad49f96f24b` | `octocat/Hello-World` | `octocat/Hello-World` |
| `2d8fc741-b576-401a-9676-c341063648e0` | `Usefused/engine` | `Usefused/engine` |
| `3776e95b-5e92-4411-bf47-33347feade74` | `octocat/Hello-World` | `octocat/Hello-World` |

Provider diagnostics confirm actual GitHub response fields, including `full_name`, `private` and `html_url`. These were not mocked responses or readiness-only checks. Worker logs show PID 56 loaded before the latter two executions with no intervening load for their eight-second interval, supporting persistent worker reuse. No external messages or provider writes were sent.

## Observed confinement

The parent inspected the running container and kernel process status independently of the submitted plan:

- UID/GID `999:999`; effective and bounding capability masks both zero.
- Read-only root filesystem; no privileged mode or added capabilities; `NoNewPrivs=1`.
- `Seccomp=2`; inline default-deny profile semantically matches the public profile extracted from the exact pinned Engine image. Original profile SHA-256: `6a6d6aa15153377ed85adf31d6b6c4901d86f64ae64149e1eb64712679a88d03`.
- AppArmor remains confined under `docker-default`. The host's unprivileged-user-namespace restriction remains enabled; no global policy was disabled.
- 1 CPU, 2 GiB RAM and the same total RAM-plus-swap limit, allowing no extra swap; PID limit 1024.
- `/tmp`: 256 MiB tmpfs, mode 1777, `nosuid,nodev`; persistent `/app/data`; profile mount read-only. No Docker socket mount.
- Engine worker and execution concurrency are both one for this functional test, not a load benchmark.

## Hostlier changes needed

Version 2 gained an optional restricted Docker `security` contract: explicit nonroot IDs, mandatory read-only root/no-new-privileges/capability drop/no-swap, finite PID limits, bounded tmpfs and a `seccompConfig` reference to immutable reviewed configuration bytes. The CLI's bundled deploy skill documents those fields and top-level literal `command`/`args` overrides. Unsupported Kubernetes security declarations fail explicitly before mutation. No privileged, unconfined or host-namespace escape option was introduced.

Configuration/runtime tests cover rejected root identities, writable roots, missing privilege restrictions, capability misuse, unconfined/default-allow policy, unsafe scratch mounts and actual daemon payload settings. Relevant config/runtime/CLI/controller race tests and vet pass. Existing unrelated repository quality findings remain; changed files introduce none.

## Recorded limitations and failures

One provider call failed with a transport error and no provider response; after diagnosis, a fresh execution succeeded. Its underlying transient cause was not established. One concurrent gRPC verification returned `UNKNOWN / Stream removed`; the sequential check passed without changing configuration. These are recorded rather than claimed fixed.

Fused CLI's generic execution failure omitted the failed execution ID, so activity and authorized private diagnostics were required. Activity also labels these receipts `sdk` while app metadata correctly identifies `unified_app`; those Fused metadata/diagnostic gaps were not changed in this Hostlier task.

Local nonsecret artifacts: `/private/tmp/hostlier-fresh-cli-deploy/unified-app-report.txt`, `unified-app-evidence.json`, `provider-success-evidence.json`, `worker-isolation.json` and the authored `.fused/unified_app/cold-cli-worker-proof.{yaml,tsx}`. Tokens, credentials, read handles and raw private diagnostics remain in protected state and are omitted from reports.
