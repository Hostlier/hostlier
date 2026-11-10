# Ubuntu 26.04 migration and prerequisite verification

Verified on 2026-11-10 in the disposable `hostlier-ubuntu2604` Lima VM,
with no host filesystem mounts. Guest: Ubuntu 26.04.1 LTS arm64,
kernel 7.0.0-38-generic, 2 vCPUs, 4 GiB RAM, 14 GiB disk. Official Ubuntu
cloud image SHA-256:
`8eb876c2ccc4b6ff89e4a65d511815923b285e04503ba465809bf587f763da24`.

`test/e2e/migration_vm.py` passed actual Docker → pinned k3s
`v1.34.12+k3s1` → Docker migration. Its Margin-style primary and storage
sidecar use nonroot numeric IDs, a read-only root, dropped capabilities,
no privilege escalation, bounded memory scratch and no-swap declarations.
It checked immutable environment/file secret revisions, independent primary
and sidecar volumes, HTTPS readiness, live kubelet PID/failSwapOn settings,
controller restart, cold backups and rollback preserving writes made on k3s.
Private test TLS certificates include CA key-usage extensions required by
Ubuntu 26.04's Python 3.14 strict certificate validation.

`test/e2e/migration_preparation_vm.py` then passed real prerequisite plan,
apply and rollback with an actual active swap file and the test-owned
inactive cluster. It verified that:

- A plan leaves the backup directory absent and does not change swap.
- Apply refuses a missing separately reviewed fingerprint.
- All independent cold archives match their journaled SHA-256 digests.
- Preparation disables swap across reboot through fstab policy and exact
  systemd masks, while retaining the swap backing file.
- Quarantine retains original cluster files through same-filesystem renames.
- The subsequent migration preview has no swap, k3s-asset or OS blockers.
- Docker's Margin fixture continues serving data throughout preparation.
- Corrupt archives and replacement cluster paths block rollback before restore.
- An interrupted mixed original/quarantine arrangement can be recovered.
- Rollback restores exact fstab bytes and the original active swap mapping,
  leaves the old cluster inactive, retains backups and supports repeated recovery.

The fixture explicitly stops, disables and unmounts only its own disposable
cluster before preparation. Those actions are not performed by the production
workflow, which rejects live, enabled, mounted or uncertain external clusters.
Stale Unix socket names are retained in quarantine; GNU tar omits these
ephemeral objects from independent archives. Devices and FIFOs remain blocked.

Installer platform regression tests cover Ubuntu 24.04 and 26.04 on both
amd64 and arm64, and reject unsupported OS/CPU combinations. Actual Ubuntu
26.04 migration execution was exercised on arm64; amd64 is covered by the
shared installer/runtime implementation and Linux release build checks.
