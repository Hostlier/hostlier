# Local validation — 2026-10-29

Hostlier is implemented in Go. The JavaScript prototype has been removed from the product tree. This report records observed local results, not completion of every acceptance case in the proposal.

## Environment

- Build host: macOS ARM64, Go 1.27.1; module minimum Go 1.26.
- Installed-server tests: two disposable Ubuntu 24.04 ARM64 Lima VMs, each 2 CPUs, 4 GiB RAM, 12 GiB disk, no host directory mounts.
- Docker VM: Ubuntu `docker.io` 29.1.3 and Caddy 2.6.2.
- k3s VM: pinned SHA256-verified v1.34.11+k3s1 with bundled containerd, Caddy 2.6.2, no Docker installation.
- Separate Docker adapter tests: Docker Desktop 28.2.2 and Caddy 2.11.7.
- Local HTTPS uses a test CA and verifies trust and hostname. No public domain/server was used, as requested.

## Passed gates

| Gate | Evidence |
| --- | --- |
| Go unit/integration suite and race detection | `go test -race ./...`; CLI/controller/store/config/runtime/installer/resource packages |
| Static analysis | `go vet ./...` |
| Standalone packaging | Native macOS binary and static Linux amd64/arm64 builds; installed Linux ARM64 binary executed without Go or Node.js |
| Secure CLI pairing and approval | Packaged CLI HTTPS tests, both pairing directions, invalid certificate rejection, hidden input, PTY approval, rejection of deployment without a terminal or with `--yes` |
| Docker+Caddy adapter lifecycle | Real HTTPS, immutable image, private port, limits, volume data, update, failed readiness restoration, path routing/stripping, retain/purge |
| Fresh installed Docker server | Dependency installation, enabled systemd services, loopback controller/Caddy admin, HTTP/HTTPS listeners, UFW SSH/web rules, idempotent rerun |
| Fresh installed k3s server | SHA-verified installation, enabled services, no Traefik/ServiceLB conflict, private management firewall configuration, successful rerun with owned k3s rules preserved |
| Full installed app workflow, both runtimes | Actual CLI → HTTPS controller → Docker or k3s → Caddy; secret upload, approved nginx deployment, verified HTTPS |
| Installed CLI lifecycle completion | Docker restart returns only after its durable operation succeeds and readiness passes; explicit `--detach` remains available |
| Domain configuration | Installed CLI changes persisted to `config.yaml`; direct file edits applied on restart; removal of a domain serving an active app rejected |
| App and controller reboot recovery, both runtimes | Actual VM reboot followed by service/controller/HTTPS/log checks |
| Full installed resource binding, both runtimes | Approved Redis creation, app binding, authenticated SET/GET from the app over its private network; active-dependent stop/delete rejection |
| Resource persistence across host reboot, both runtimes | Previously written Redis data queried through the app after actual Docker and k3s VM reboots |
| PostgreSQL, Redis, Neo4j on Docker | Real authenticated reads/writes, incorrect-password rejection, private ports/networks, lifecycle persistence, reuse, retained deletion/purge, native backup and independent restore with distinct credentials |
| PostgreSQL, Redis and Neo4j on k3s | Real protocol authentication, private ClusterIP, lifecycle persistence, native backup, independent restore with distinct credentials, restart verification, retained PVC and scoped purge; PostgreSQL 68 s, Redis 116 s, Neo4j 351 s |
| Packaged resource CLI on Docker | HTTPS pairing, PTY-approved PostgreSQL provisioning, real data, encrypted server backup, mode-0600 export, approved separate-target restore and data query |
| Authenticated long-operation transport | Actual TCP+TLS controller with a short normal write deadline; delayed backup response completes and concurrent status stays responsive |
| Installed Neo4j offline backup | Real approved backup and mode-0600 export through the installed Docker controller and verified Caddy HTTPS |

The reproducible installed-server harness and VM instructions are in [test/e2e/VM_TESTS.md](test/e2e/VM_TESTS.md). Adapter/resource commands are in [test/e2e/README.md](test/e2e/README.md). Ordinary `go test` skips explicitly opted-in infrastructure tests; its success alone does not imply those gates ran.

## Practical limits and remaining gates

Public DNS, public ACME issuance/renewal and cloud firewall behavior remain untested. Fresh-server execution was ARM64; Linux amd64 was cross-compiled, not boot-tested. The proposal also contains broader fault-injection, resource-exhaustion, streaming/WebSocket, upgrade and parity cases beyond the specific evidence above.

Docker and k3s application pulls can reuse the installing server user’s existing Docker login, including configured credential helpers; see [private images](docs/guides/private-images.md) for service requirements and test scope. On k3s the login is delivered through a namespace-scoped pull secret; this path has unit coverage only and still needs a real-cluster private-registry run. Runtime migration, Kubernetes database major-version upgrades, OS credential-store integration, and enforced local-volume capacity quotas are not implemented. Explicit Docker database upgrades and S3 backup import are covered in [database recovery](docs/database-recovery.md). Backup payloads are bounded to 64 MiB. A single server and local storage do not provide host-failure failover. HTTPS tunnels are documented in [Bridge tunnels](docs/guides/bridge-tunnels.md). Local product composition is covered by the iteration 3 results below.

An interrupted accepted job is retained with an actionable recovery state rather than silently replayed. This preserves its operation identity and requires inspection/new approval before retrying uncertain work. Successful recovery deployment or deletion supersedes the old job's route reservations. This behavior is covered in controller tests; it is not a claim of arbitrary crash-point automatic reconciliation.

## Optional packages and workflows — 2026-10-29

Local execution evidence for the package implementation:

- Actual TLS PostgreSQL/Redis fixtures passed: scoped writes, forbidden role/ACL operations, wrong-CA rejection, and deferred provider inputs with a private CA.
- Actual installed-package PostgreSQL/Redis workflow application attachment passed under the race detector (8.41s): CLI `.hostlier` review, one approval, native provisioning, forwarded private networks, and authenticated app-container SQL/Redis writes and reads. The app-runtime adapter launches real probes; it does not simulate Caddy ingress.
- Generic installed-package resource CLI passed (18.93s): package installation, changed-executable rejection before mutation, approved PostgreSQL create, writes, encrypted backup, protected export, independent restore and data verification.
- Installed Ubuntu ARM64 k3s regression passed after fixing package fingerprint migration: real deployment, approval enforcement, restart, trusted local HTTPS, services/log permissions and preserved app-to-Redis data. The existing fixture was recovered with its normal `resource start` command, never by editing state. VM stopped with fixtures retained.
- Provider HTTP contract tests passed with race detection. No live cloud resources were created. Aiven direct provisioning awaits credentialed cloud E2E; Railway direct managed-instance creation remains unavailable.

The integration tests found and fixed two concrete defects: stale/missing package digests on existing dependencies after upgrade, and workflow resource names exceeding the 63-byte DNS label limit. Private administrative outputs are encrypted and may feed only dependent package steps, not application bindings.

Final verification for this change: `make check` passed (quality, vet, full race suite); final quality scan checked 171 Go files and 1,270 functions, maximum complexity 10, zero missing-comment findings. Native and Linux amd64/arm64 controller/package builds passed. The production controller dependency graph excludes engine implementations, PostgreSQL/Redis drivers and provider adapters. The final approved executable-pinned restore regression also passed (13.38s).

## Iteration 3 — local feature gates

Implemented in Go using Sol 6.1 subagents. The following gates ran against actual local services or the installed CLI; fixture-based tests supplement them.

| Feature | Observed evidence |
| --- | --- |
| Scale CPU/memory and replicas | Docker daemon inspection and installed k3s desired/ready counts match approved limits; k3s 1 → 3 → 1 serves three distinct pod identities through one HTTPS URL |
| Approval and scale-to-zero | Both installed backends reject missing TTY, `--yes`, declined/interrupted approval; zero survives full VM stop/start with HTTP 503; approved up restores the app and previously written Redis data |
| Replacement and drain recovery | Real Docker source keeps serving during candidate readiness, then stops after cutover; unhealthy replacement restores prior state. A restarted retained predecessor with a durable pending-drain marker is stopped by a new approved deployment, with one healthy app remaining and Redis data intact |
| SQLite and controller recovery | Original JSON/key migration, incompatible schema, symlink and encrypted-result failures are tested. Both installed controllers pass stopped backup → original metadata moved aside → restore to the original absolute path → restart, authenticated app access and retained Redis reads |
| Workflow recovery | Actual package subprocess lost response, controller restart, original credential/digest inspection, explicit approval and successful output reuse without duplicate provisioning. Native and remote PostgreSQL/Redis inspection tests verify actual owned database/ACL state |
| Local products and package SDK | CLI composition and approval with installed PostgreSQL/Redis packages and real app-container writes/reads. Compiled PostgreSQL package install/describe tests verify schema discovery without inherited secrets or extra files |
| Usage | Actual Docker CPU/memory samples and externally changed limits; stopped/unavailable observations omit measurements. Installed k3s metrics API returns real usage and desired/ready counts |
| Optional Nginx | Fresh Ubuntu installation, actual app deployment, repeated init preserving configuration/routes, and full reboot with trusted local HTTPS. Separate live tests cover path matching, SSE, WebSocket, rejected config, certificate replacement/reload and zero-capacity 503 |

The Nginx gates found and fixed asynchronous TLS-listener readiness, removed-package detection, and safe recognition of the installed Docker 29 firewall rules. Recovery testing found and fixed Linux path recursion and preserved original resource ownership when restoring. All three test VMs were stopped with fixtures retained.

Detailed evidence: [scaling and restore](docs/iteration3-scaling-progress.md), [Nginx and migration](docs/iteration3-nginx-progress.md), [SDK discovery](docs/iteration3-sdk-progress.md). Reproduction commands are in [the E2E guide](test/e2e/README.md).

The complete race suite passed during integration. Final affected-package race checks cover the subsequent drain, SDK, installer and migration corrections. The whole-tree complexity check currently includes a separate, concurrently edited version-2 workload/sidecar implementation; its findings are outside this iteration's edits and are being kept separate at the user's request. Iteration 3 functions satisfy complexity ≤10 and the comment gate.

The completion pass below supersedes the earlier local ACME, clean-host recovery, database resizing and product-integration limits. Public ACME and external DNS/cloud firewall validation still require a public release environment. Provider integrations, Terraform and Pulumi remain deferred without an iteration number. The optional TypeScript exporter is a bounded authoring adapter, not a replacement for the parent dashboard runtime.

Final verification after feature fixes: affected controller/runtime/CLI/installer/store/package-runner race tests passed, `go vet ./...` passed, and Linux amd64/arm64 controller plus native/Linux optional-package builds passed. The final archive regression also verifies restored Certbot links, re-export at the original state path, fresh configuration collection, omitted Nginx PID files, and Caddy account/certificate bytes. Caddy must be stopped for its consistent certificate-state snapshot, as documented. The concurrent version-2 work continues changing shared source, so these results identify the tested integration snapshot rather than certifying that separate feature.


### Remaining iteration 3 completion pass — 2026-10-29

| Feature | Actual local evidence |
| --- | --- |
| Clean-server recovery | A new Ubuntu VM began with no controller state or containers. The original server was stopped; the destination restored the controller bundle, original key/config/package inventory/TLS, a separately archived application volume, and native Redis data through approved restore. Trusted HTTPS independently verified original app data, Redis data and secret continuity, including after destination reboot. |
| ACME and renewal | Pinned Pebble performed actual HTTP-01 validation; Certbot issued a certificate and ran a real forced renewal with the generated Nginx validation/reload hook. A hostname/CA-verifying application client observed different certificate serials and healthy HTTP before/after. No insecure production trust settings were added. |
| Database CPU/memory | All three engines passed real Docker and k3s resizing with native authenticated reads/writes and observed runtime limits. Docker rejected impossible allocations and preserved prior limits/data. A k3s patch succeeded but lost its acknowledgement; rollback restored the exact old allocation and data. The compiled CLI/HTTPS controller/independent PostgreSQL package path also passed explicit maintenance approval. |
| Product hooks and policies | Compiled CLI terminal approval with real PostgreSQL/Redis packages, authenticated app writes/reads, a completed PostgreSQL after-success hook whose response was lost, controller restart, policy-denied scaling and exact hook reconciliation. The same three actual database container IDs remained; no duplicate hook effect or app execution. |
| Optional parent SDK authoring | `exportHostlierProduct` builds and passes dedicated/shared PostgreSQL dependency, secret-reference and unsupported-contract tests. It explicitly rejects unsupported private/native/runtime/storage-class/table/existing-ownership features. |
| Docker process crashes | Real runtime subprocesses exited without deferred cleanup before pull, with a running candidate, after Caddy switched routes and after durable commit before source drain. Stateless and volume-backed cases recovered with a fresh adapter and approved operation, one running app and preserved volume data. |
| Capacity and rollback | Real Docker daemon capacity rejection and kernel OOM of a 16 MiB candidate restored the original healthy writer. Restart was disabled only on that test candidate to make the first OOM terminal. Real k3s unschedulable CPU requests preserved a stateless source or restored the retained PVC writer and data. Installed CLI rollback on both backends restored a selected scaled revision's actual limits/replicas and existing Redis data. |

Focused evidence: [fresh recovery and ACME](docs/iteration3-recovery-acme-progress.md), [database resizing](../docs/iteration3-database-resize.md), [product contract](docs/iteration3-product-progress.md), [scaling crash recovery](docs/iteration3-scaling-recovery-progress.md). These gates use local servers and owner-scoped fixtures; no Aiven, Railway or public-cloud account is needed. The concurrent workload/sidecar implementation remains a separate acceptance scope.

The complete Go race suite passed during this completion pass (including the controller, CLI, runtime, resources, store, packages and product/workflow suites). Final safety-review changes receive affected-package checks below; whole-tree findings in the separate workload/sidecar work are reported separately.

Final integration checks: `go vet ./...` and Linux amd64/arm64 builds of all Go packages passed. The final runtime race suite passed after readiness diagnostics. Real diagnostic gates assert `APPLICATION_OOM` on Docker and `UNSCHEDULABLE` on k3s; the latter independently observed `InsufficientCPU` while 102 source HTTPS/data reads succeeded. Reviewed rollback safety now refuses to restart a prior persistent-volume writer until candidate removal/termination is confirmed.

The previously reported workload/sidecar quality findings are resolved: runtime directory validation was extracted from `appendRuntime`, and the health monitor loop/select now have explanatory comments. Rechecked on 2026-10-29: 295 Go files and 1,949 functions, maximum complexity 10, zero complexity violations and zero missing comments. This quality result does not replace the separate workload/sidecar functional acceptance tests.
