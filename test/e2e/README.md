# Local integration tests

For the installed Go binary on fresh Docker and k3s servers, including real CLI approval, local-CA HTTPS, resource bindings and reboot recovery, use [VM_TESTS.md](VM_TESTS.md). Recorded results and remaining gates are in [../../VALIDATION.md](../../VALIDATION.md).

## Docker and Caddy

Run the Go integration test against a disposable Docker context and a trusted local Caddy binary:

```sh
HOSTLIER_DOCKER_E2E=1 \
HOSTLIER_DOCKER_SOCKET=/path/to/docker.sock \
HOSTLIER_CADDY_BINARY=/path/to/caddy \
go test ./test/e2e -run TestDockerCaddyLifecycle -v -count=1
```

The test starts a separate Caddy process on loopback high ports, creates a temporary test certificate authority, and verifies HTTPS hostname and certificate trust with a dedicated certificate pool. It creates uniquely labeled Docker containers, networks, and volumes and removes only resources with its state-directory owner label. It requires the `docker` CLI and a cached `nginx:1.28-alpine` image with a portable repository digest. Normal deployment still performs an approved image pull.

It checks read-only image inspection, real deployment, private workload port binding, CPU/memory settings, volume writes, lifecycle and update persistence, failed readiness rollback, segment-aware path routing and stripping, default data retention, and explicit purge. Runtime public HTTPS probing is disabled only inside this local harness because its private hostname/high port cannot resolve publicly; the independent HTTPS client verifies the actual Caddy response with normal certificate verification.

This is an adapter integration test. It does not certify public DNS, ACME issuance or renewal, provider firewalls, reboot recovery, fresh-server installation, k3s, private-registry authentication, streaming/WebSockets, or packaged CLI approval and pairing. Those require separate gates. Passing it does not complete either proposal iteration.

## Database resources

From the `hostlier` module, the following opt-in tests use a disposable local Docker Engine at `/var/run/docker.sock` and the pinned engine images:

```sh
HOSTLIER_LIVE_RESOURCES=1 \
go test ./internal/resources -run TestLiveDockerResourceProtocolsAndPersistence -v -count=1

HOSTLIER_LIVE_RESOURCE_CLI=1 \
go test ./internal/resources -run TestLivePackagedCLIResourceBackupAndRestore -v -count=1
```

The engine suite checks authenticated write/read, rejected bad credentials, private ports/networks, restart persistence, reconciliation identity, native backup and restore into separate instances, retained deletion and purge for PostgreSQL, Redis and Neo4j. The packaged CLI resource test uses PostgreSQL, a trusted local HTTPS certificate and a PTY to exercise real approval, provisioning, backup export and restore. It requires Python 3 for the PTY harness in addition to Go and Docker. The runtime used for non-resource application methods in that test is a test adapter; this is evidence for the real resource/controller/CLI path, not application deployment parity.

Neo4j Community backup stops the source temporarily and restores its prior running state. Controller backups are blocked while active applications depend on that instance. Exported files contain sensitive data; server copies are encrypted and the CLI writes new output files mode 0600. Current adapter backup transport is limited to 64 MiB.

K3s manifest and command tests run with the normal Go suite. They do not substitute for actual k3s installation, protocol/persistence, network-isolation and backup/restore tests in a disposable Linux VM. Public ACME is outside the user-selected local-only run. VM installation, firewall and reboot evidence should be reported separately from these tests, with the exact architecture and runtime identified.

## Isolated k3s resource gate

Inside a disposable Linux k3s server, run:

```sh
HOSTLIER_LIVE_K3S_RESOURCES=1 \
go test ./internal/resources -run TestLiveK3sResources -v -count=1 -timeout=30m
```

A Go test binary may instead be cross-compiled with `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c ./internal/resources`, copied into the disposable VM, and run with the same environment variable and `-test.run TestLiveK3sResources -test.v -test.timeout 30m`. The test needs the installed `/usr/local/bin/k3s` and protected `/etc/rancher/k3s/k3s.yaml`. It creates its own temporary state directory and uniquely owned namespaces; it does not edit the installed controller state or Caddy routes.

Observed local ARM64 Ubuntu 24.04 gates passed for PostgreSQL (68.11 seconds), Redis (116.08 seconds), and Neo4j (350.53 seconds). Each checks real authenticated operations, wrong-password rejection, writes, lifecycle persistence, engine-native backup, restoration into a separate instance with a different credential, restart durability after restoration, ordinary deletion retaining its PVC, and explicit namespace/data purge. Neo4j Community backup/restore takes an offline maintenance window. Backup exports are capped at 64 MiB while the process output is read.

These individual engine gates supplement the adapter and installed CLI tests. They do not claim the proposal's entire acceptance matrix, external ACME, storage failure/failover, or major engine upgrades are complete.

## Optional package workflows

The following tests exercise the executable-package path and actual engine permissions without cloud accounts:

```sh
HOSTLIER_TEST_DOCKER=1 \
go test ./packages/engines -run TestDockerTLSWorkflows -v -count=1

HOSTLIER_DOCKER_E2E=1 \
HOSTLIER_DOCKER_SOCKET=/path/to/docker.sock \
go test -race ./test/e2e -run '^TestDockerWorkflowNewInstancesApplicationAttachment$' -v -count=1
```

The TLS fixture generates a private CA, starts disposable PostgreSQL/Redis containers on loopback, creates scoped users, verifies real writes and denied privileges, and rejects an incorrect CA. It also tests a deferred predecessor connection and CA. Alternatively, `HOSTLIER_TEST_POSTGRES_URL` and `HOSTLIER_TEST_REDIS_URL` enable tests against explicitly supplied disposable TLS endpoints.

The application-attachment integration builds and installs actual PostgreSQL/Redis executables, reads `.hostlier` through the CLI, requires one approval, and runs the production controller/package runner. Its application runtime starts real probe containers on the returned private resource networks and authenticates SQL and Redis writes/reads. The app-runtime test adapter does not exercise Caddy; run the separate Docker/Caddy and installed VM gates for ingress. Engine packages currently use the server's default Docker socket for workflow provisioning, so a custom test Docker socket must identify that same daemon.

The packaged CLI resource test above now installs all three engine binaries and uses the generic resource broker. It additionally replaces an approved executable and checks rejection before any container or metadata creation, then exercises actual PostgreSQL backup and separate-target restore. All fixtures remove only their own resources.

## Iteration 3 feature gates

Run the actual Docker scaling, usage, and local product attachment tests with the same disposable Docker context and trusted Caddy binary:

```sh
HOSTLIER_DOCKER_E2E=1 \
HOSTLIER_DOCKER_SOCKET=/path/to/docker.sock \
HOSTLIER_CADDY_BINARY=/path/to/caddy \
go test ./test/e2e -run 'TestDocker(Scaling|UsageObservedMetrics|CustomProductApplicationAttachment)$' -v -count=1

HOSTLIER_TEST_DOCKER=1 \
go test ./packages/engines -run 'TestDocker(NativeInterruptedRecovery|TLSWorkflows)$' -v -count=1

HOSTLIER_NGINX_E2E=1 \
go test ./internal/nginx -run TestLiveNginxRoutingTLSAndReload -v -count=1
```

The Nginx gate requires an installed Linux Nginx and OpenSSL and uses isolated configuration, local certificates and high loopback ports. It does not test public ACME issuance. Installer, controller migration/recovery, CLI approval and package discovery regressions also run in the normal Go suite.

After establishing the installed VM fixture described in [VM_TESTS.md](VM_TESTS.md), copy both `installed_vm.py` and `scaling_vm.py` to `/tmp` in that VM. Run `sudo python3 /tmp/scaling_vm.py exercise docker` (or `k3s`), then `zero`, stop/start the VM, copy both scripts back if `/tmp` was cleared, run `verify-zero`, and finally `up`. These gates inspect actual runtime counts/limits and query retained Redis data. Run modes sequentially against a disposable fixture.

For controller recovery, follow [the offline backup/restore procedure](../../docs/controller-recovery.md), retaining the stopped original metadata separately and restoring into its original absolute path. Then run `installed_vm.py verify docker` or `verify k3s` to independently check authenticated app access, services, protected logs and existing database data. This is metadata recovery with runtime data retained, not reconstruction of lost volumes.

### Iteration 3 completion gates

Use the same Docker/Caddy environment above for the complete crash, capacity and product-contract pass:

```sh
HOSTLIER_DOCKER_E2E=1 \
HOSTLIER_DOCKER_SOCKET=/path/to/docker.sock \
HOSTLIER_CADDY_BINARY=/path/to/caddy \
go test ./test/e2e -run '^TestDocker(Scaling|ScalingProcessCrash|ScalingCapacityFailure|ProductContract)$' -v -count=1
```

The crash test exits actual runtime subprocesses without deferred rollback, then reopens retained state. The capacity test observes a real Docker OOM flag and verifies the old writer and route recover. Product testing compiles the CLI and package executables, uses terminal approval and real PostgreSQL/Redis data, and reconciles a lost after-success response without duplicating its effect.

Inside the disposable installed k3s fixture, run sequentially to avoid exhausting the small VM with unrelated concurrent test baselines:

```sh
HOSTLIER_K3S_SCALING_FAULTS=1 HOSTLIER_CADDY_BINARY=/usr/bin/caddy \
go test ./test/e2e -run '^TestK3sScalingCapacityAndRollback$' -v -count=1
HOSTLIER_K3S_SCALING_FAULTS=1 HOSTLIER_CADDY_BINARY=/usr/bin/caddy \
go test ./test/e2e -run '^TestK3sScalingProcessCrash$' -v -count=1
```

These gates use temporary owner-scoped namespaces and a private Caddy fixture. They read the installed fixture's immutable nginx image and require `/usr/local/bin/k3s` and its protected kubeconfig. Cross-compile the E2E test binary for the VM architecture if Go is unavailable there.

For actual installed CLI rollback, copy `installed_vm.py`, `scaling_vm.py` and `scaled_rollback_vm.py` beside each other in the VM, then run `sudo python3 /tmp/scaled_rollback_vm.py rollback docker` (or `k3s`). The script restores the original fixture revision through terminal approval afterward.

Actual local ACME issuance/renewal and clean-new-server restoration, including separately backed-up app and database data, are documented in [VM_TESTS.md](VM_TESTS.md) and [controller recovery](../../docs/controller-recovery.md). These supersede the earlier same-server metadata-only recovery limit; public ACME validation remains a separate release gate.
