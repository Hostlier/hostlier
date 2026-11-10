# Installed Go binary tests

## Runtime migration and rollback

Use a fresh, disposable VM named exactly hostlier-runtime-migration-test, with the
checked-in Ubuntu 24.04 Lima configuration and no host mounts. Build current Linux
arm64 hostlier-controller, hostlier, and these test binaries:

    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/migration-fixture ./test/e2e/migration-fixture
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o /tmp/hostlier-migration.test ./internal/migration

Copy those four binaries (named hostlier-controller, hostlier, migration-fixture,
hostlier-migration.test), collector/dist/collector-image/, and migration_vm.py to
the guest's /tmp. Install Docker, Nginx, Certbot, OpenSSL and Python 3 in the guest.
Inside this VM only:

    sudo python3 /tmp/migration_vm.py setup
    sudo python3 /tmp/migration_vm.py exercise
    sudo python3 /tmp/migration_vm.py crash
    sudo /tmp/hostlier-migration.test -test.v

The harness refuses any hostname except the specified fixture (including Lima's
lima- prefix). Setup installs a stateful nonroot service and storage sidecar with
independent volumes, pinned environment/file secrets, read-only roots, dropped
capabilities, no privilege escalation, PID/no-swap declarations, and bounded
sticky scratch space. Private HTTPS remains verified through the guest's test CA.

Exercise checks a read-only preview, Docker → k3s cutover, both volumes and live
security settings, controller restart, and rollback preserving newer k3s writes.
It uses the reviewed retry path after a prior complete rollback. Crash kills a
new migration during restore, verifies the systemd startup guard, and checks
journal-driven rollback. The Go filesystem gate uses GNU tar to verify sparse
data, permissions, hard links and symlinks.

Stop and remove only the disposable VM created for this test when finished.
No production server or public certificate issuer participates.

## Fleet handoff on fresh VMs

From the Hostlier module on an Apple Silicon Mac, run:

```sh
make e2e-fleet
```

The test needs Lima 2.2+, Go, Python 3, OpenSSH, OpenSSL, network access for Ubuntu packages, and room for four 1 GiB VMs. It builds both Linux binaries and the laptop CLI from the working tree. The first run also builds the bundled collector image if missing, which requires Docker and the collector build dependencies. [Collector build](../../collector/README.md)

The command creates two masters and two boxes, approves disposable device credentials using the masters’ protected sockets, and exercises the production SSH installer. It counts one SSH invocation per box, checks the hostname returned by each laptop selection, and moves Blue to Secondary with the installed controller. It checks that Primary still has the correct enrollment and can reach Blue’s fleet port, but receives a fresh TLS identity rejection. Green remains accessible from Primary. It repeats the access checks after restarting Blue. It deletes only the VMs it created and removes its temporary keys and profiles on exit. Physical passkey interaction and public ACME issuance are outside this test.

Run `FLEET_TEST_IDLE=1 make e2e-fleet` to include idle scaling on Docker and k3s. The fleet guests are removed before the k3s guest starts. To run just the k3s test:

```sh
make e2e-k3s-idle
```

This target creates a fresh 2-CPU, 4 GiB Ubuntu VM, installs the working-tree controller with `--runtime k3s`, and deploys a scratch echo service with `sleepAfter: 20s`. Docker packages the fixture image. Hostlier imports it into k3s containerd and runs it as a Kubernetes Deployment. The guest needs network access for the pinned k3s binary and its system images.

The test observes one ready replica, then zero desired replicas and zero Pods. It sends one HTTPS POST, checks the echoed body and counts the matching request in the new Pod’s application log. It repeats the cycle after restarting `hostlier.service` while the Deployment is at zero, checking that the controller restart preserves the Deployment UID, generation, and zero capacity until traffic arrives. The VM and temporary credentials are removed on exit.

## Existing Docker and Nginx coexistence

`coexistence_vm.py` creates two manually managed Docker apps behind host Nginx
**before** Hostlier is installed. It records file hashes, certificate fingerprints,
container identities, and service process identities, then continuously probes both
HTTPS sites during automatic installation, deployment, restart, a conflicting
hostname attempt, repeated initialization, external certificate renewal, and deletion.
See [observed results](../../docs/coexistence-validation.md).

Use a newly created, disposable VM with exactly the following name. The harness
refuses another hostname or an already initialized fixture. Commands below run
from the Hostlier module directory on Apple Silicon with Lima installed:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/hostlier-coexistence ./cmd/hostlier
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/hostlier-controller ./cmd/hostlier-controller
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o /tmp/hostlier-coexistence-cli.test ./internal/cli
# If collector/dist/collector-image is absent, build it as described in collector/README.md.
limactl start --tty=false --name=hostlier-coexistence-test test/e2e/lima-arm64.yaml
limactl shell hostlier-coexistence-test sudo env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l bash -c 'apt-get update && apt-get install -y docker.io nginx certbot openssl python3 && systemctl enable --now docker nginx && docker pull nginx:1.28-alpine'
limactl copy /tmp/hostlier-coexistence hostlier-coexistence-test:/tmp/hostlier
limactl copy /tmp/hostlier-controller hostlier-coexistence-test:/tmp/hostlier-controller
limactl copy -r collector/dist/collector-image hostlier-coexistence-test:/tmp/collector-image
limactl copy /tmp/hostlier-coexistence-cli.test hostlier-coexistence-test:/tmp/hostlier-coexistence-cli.test
limactl copy test/e2e/coexistence_vm.py hostlier-coexistence-test:/tmp/coexistence_vm.py
limactl shell hostlier-coexistence-test sudo python3 /tmp/coexistence_vm.py setup
limactl shell hostlier-coexistence-test sudo python3 /tmp/coexistence_vm.py exercise
limactl shell hostlier-coexistence-test sudo cat /root/hostlier-coexistence-e2e/result.json
limactl stop hostlier-coexistence-test
```

The setup phase configures private CA trust, fixture DNS, explicit Nginx default
listeners, external certificate references, and an operator-owned renewal timer.
All of these precede the preserved baseline. HTTPS trust and hostname validation
remain enabled. Deployment approvals use a separate, opt-in test executable that
signs only fixture app requests through the installed controller's normal approval
API; this is not evidence of physical hardware-key interaction. No production
approval bypass is installed.

Only `setup` accepts a fresh fixture; `exercise` is intended for one run. Evidence
stays in `/root/hostlier-coexistence-e2e` inside the stopped VM. It contains a
disposable test authenticator private key, so export only the report and selected
nonsecret snapshots, never the whole directory. Public ACME issuance, public DNS,
and cloud firewall behavior are separate gates.

## Fresh server installation

Run only in disposable Ubuntu 24.04 servers. `installed_vm.py` modifies the guest's hosts file and CA trust for private `.test` hostnames. It never disables HTTPS verification. This fixture does not test public ACME.

The checked-in Lima configuration uses an ARM64 Ubuntu cloud image, two CPUs, 4 GiB RAM, a 12 GiB disk, and no host filesystem mounts. On Apple Silicon with Lima installed, from the Hostlier directory:

```sh
make linux
limactl start --name=hostlier-docker-test test/e2e/lima-arm64.yaml
limactl copy dist/hostlier-linux-arm64 hostlier-docker-test:/tmp/hostlier
limactl copy dist/hostlier-controller-linux-arm64 hostlier-docker-test:/tmp/hostlier-controller
# Build collector assets as described in collector/README.md before installation.
limactl copy -r collector/dist/collector-image hostlier-docker-test:/tmp/collector-image
limactl shell hostlier-docker-test sudo /tmp/hostlier init --domain apps.hostlier.test --yes
limactl copy test/e2e/installed_vm.py hostlier-docker-test:/tmp/installed_vm.py
limactl shell hostlier-docker-test sudo python3 /tmp/installed_vm.py setup docker
limactl shell hostlier-docker-test sudo python3 /tmp/installed_vm.py deploy docker
limactl shell hostlier-docker-test sudo python3 /tmp/installed_vm.py dependencies docker
limactl shell hostlier-docker-test sudo python3 /tmp/installed_vm.py domains docker
limactl shell hostlier-docker-test sudo reboot
```

After SSH becomes available again, copy the harness back because the guest may clear `/tmp`, then verify recovery:

```sh
limactl copy test/e2e/installed_vm.py hostlier-docker-test:/tmp/installed_vm.py
limactl shell hostlier-docker-test sudo python3 /tmp/installed_vm.py verify docker
```

For k3s, use a separate fresh VM. Substitute a distinct VM name, add `--runtime k3s` to `init`, and pass `k3s` to the Python harness. Do not switch runtimes on an existing installation. `init --dry-run` can inspect the changes first; this test's `--yes` authorizes installation only. Deployments and resource provisioning still use a real PTY to approve the displayed plan.

The harness exercises the actual installed CLI, systemd controller, Caddy and runtime: secure token pairing, secret upload, read-only plan, rejection of missing/noninteractive deployment approval, nginx deployment, verified HTTPS, restart, approved Redis creation, dependency credential binding, authenticated private app-to-Redis SET/GET, active-dependent protection, and domain file/API persistence. `verify` checks enabled/running services, controller access, live HTTPS, protected logs, and previously written Redis data when dependencies are configured. Invoke it after reboot to test recovery. Run modes sequentially; `domains` intentionally restarts the controller.

The optional `slow-backup` mode provisions Neo4j and exercises its approved offline backup/export through the installed HTTPS endpoint. It creates a mode-0600 backup file inside the guest and verifies the original app still works afterward.

The private CA and host mappings are fixture configuration, not production defaults. The production controller still performs its normal public-URL readiness probe; inside this fixture the URL resolves to the VM and its certificate chains to the guest's trusted test CA.

The separate k3s resource suite cross-compiles into a Linux test executable:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o /tmp/hostlier-resources.test ./internal/resources
limactl copy /tmp/hostlier-resources.test hostlier-k3s-test:/tmp/hostlier-resources.test
limactl shell hostlier-k3s-test sudo env HOSTLIER_LIVE_K3S_RESOURCES=1 /tmp/hostlier-resources.test -test.run TestLiveK3sResources -test.v
```

The resource suite uses unique namespaces and cleans up only its owned test resources. Stop or delete only the disposable VMs you created when finished. No external server, domain, provider firewall, or public certificate issuer is needed for these local gates.

The Nginx local ACME gate uses actual Certbot and pinned Pebble with HTTP-01 checks enabled:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o /tmp/hostlier-nginx.test ./internal/nginx
limactl copy /tmp/hostlier-nginx.test hostlier-nginx-test:/tmp/hostlier-nginx.test
limactl copy test/e2e/acme_vm.py hostlier-nginx-test:/tmp/acme_vm.py
limactl shell hostlier-nginx-test sudo python3 /tmp/acme_vm.py
```

Use an owned disposable Linux VM with Docker, Nginx and Certbot installed. This gate uses isolated high ports and its own Nginx master; it does not replace the installed edge. Its new CA material remains under `/root/hostlier-acme-e2e` for inspection, and its Pebble container is removed after the test. See [controller recovery](../../docs/controller-recovery.md) and `recovery_vm.py` for the separate two-server backup/native-data-restore rehearsal. That rehearsal requires a source initialized for the private `nginx.example.test` fixture with trusted controller/app certificates, and a genuinely fresh destination VM with runtime and edge packages. Transfer only the generated archive and current binary; never copy the source Docker directory or VM disk.

For the clean-server rehearsal, build the current controller and optional Redis package for Linux arm64 first. Use `hostlier-nginx-e2e` only if it is your owned, initialized Nginx fixture, and create a distinct new VM (for example `hostlier-recovery-e2e`) from `lima-arm64.yaml`. Install Docker/Nginx/Certbot on the destination before running `restore`.

```sh
limactl copy dist/hostlier-redis-linux-arm64 hostlier-nginx-e2e:/tmp/hostlier-redis
limactl shell hostlier-nginx-e2e sudo hostlier package install redis --file /tmp/hostlier-redis
limactl copy test/e2e/recovery_vm.py hostlier-nginx-e2e:/tmp/recovery_vm.py
limactl shell hostlier-nginx-e2e sudo python3 /tmp/recovery_vm.py setup
limactl shell hostlier-nginx-e2e sudo python3 /tmp/recovery_vm.py backup
umask 077
limactl shell hostlier-nginx-e2e sudo cat /tmp/hostlier-clean-recovery.tar > /tmp/hostlier-clean-recovery.tar
limactl stop hostlier-nginx-e2e
limactl copy /tmp/hostlier-clean-recovery.tar hostlier-recovery-e2e:/tmp/hostlier-clean-recovery.tar
limactl copy dist/hostlier-linux-arm64 hostlier-recovery-e2e:/tmp/hostlier-recovery
limactl copy dist/hostlier-controller-linux-arm64 hostlier-recovery-e2e:/tmp/hostlier-controller-recovery
limactl copy test/e2e/recovery_vm.py hostlier-recovery-e2e:/tmp/recovery_vm.py
limactl shell hostlier-recovery-e2e sudo python3 /tmp/recovery_vm.py restore
limactl shell hostlier-recovery-e2e sudo python3 /tmp/recovery_vm.py activate
limactl shell hostlier-recovery-e2e sudo python3 /tmp/recovery_vm.py check
```

Run `check` again after reboot (copy the script back if `/tmp` was cleared). Keep the source stopped. Stop the owned destination VM after collecting evidence. These fixture scripts use a private CA and `.test` names; no external provider account is needed.

### v1 k3s scaling failure and historical rollback gates

`scaling_fault_vm.py rollback k3s` runs inside the installed disposable local VM beside
`installed_vm.py` and `scaling_vm.py`. It uses interactive exact-plan approval, scales
the existing fixture twice, rolls back by the first scale's immutable revision,
inspects actual ready replicas and limits, verifies Redis data, and restores the
original approved allocation. It preserves `.hostlier`.

For isolated runtime failure/crash gates, cross-compile the `test/e2e` Go test binary
for the VM architecture and run it as root with
`HOSTLIER_K3S_SCALING_FAULTS=1 HOSTLIER_CADDY_BINARY=/usr/bin/caddy` and
`-test.run '^TestK3sScaling(CapacityAndRollback|ProcessCrash)$' -test.v`.
The tests create independent owner-scoped namespaces and private Caddy listeners.
They observe real scheduler `Insufficient cpu`, retain source HTTPS and PVC data,
restore historical scaled limits/replicas, and hard-exit separate processes after
actual candidate creation and Service cutover to prove fresh-runtime intent recovery.
Run sequentially on small VMs; leave installed fixture and Redis namespaces intact.

### Detached controller update runner

Run `controller_update_vm.py` only in a fresh disposable Ubuntu VM with no
`hostlier.service` or `/var/lib/hostlier` installation. Copy the current Linux
`hostlier-controller` executable to `/tmp/hostlier-controller`, then copy the
script and run it as root. It creates a harmless placeholder controller service
and staged installer fixtures, then exercises the real production detached
runner through systemd timer/service units. Both success and installer failure
must restart the API service, preserve a truthful durable receipt and reject
terminal-job replay. This gate tests service isolation and recovery; official
release download/checksum tests and signed gRPC approval tests run in the Go
suite. It does not install packages, Docker, k3s or a production release.

For example, using a newly created `hostlier-controller-maintenance-test` VM:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/hostlier-controller ./cmd/hostlier-controller
limactl copy /tmp/hostlier-controller hostlier-controller-maintenance-test:/tmp/hostlier-controller
limactl copy test/e2e/controller_update_vm.py hostlier-controller-maintenance-test:/tmp/controller_update_vm.py
limactl shell hostlier-controller-maintenance-test sudo python3 /tmp/controller_update_vm.py
```

Remove the disposable VM after collecting the two terminal receipts. The fixture
never accepts a production server address or login.
