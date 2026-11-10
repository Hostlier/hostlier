# Docker to k3s migration

Hostlier can migrate a standalone Ubuntu 24.04 server from Docker to its pinned,
single-node k3s runtime. It covers running applications, primary and sidecar
volumes, immutable images, exact secret revisions, managed config files, and
public routes. The existing Caddy or Nginx installation is preserved.
Applications have a maintenance window while writers stop and data is copied.
The Docker daemon and unrelated containers remain operator-managed.

To inspect the server using your saved CLI login before scheduling downtime, run
`hostlier server controller migrate plan --to k3s --backup-dir NEW_SERVER_DIRECTORY`.
See [controller maintenance](controller-maintenance.md) for remote preview,
controller updates and the one-time upgrade requirement for older controllers.

Upgrade the installed controller binary to a release containing the migrate
command first. Run as root **on the server**, choosing a new backup directory
whose parent already exists:

    sudo hostlier-controller migrate plan --to k3s --backup-dir /var/backups/hostlier-k3s
    sudo hostlier-controller migrate apply --to k3s --backup-dir /var/backups/hostlier-k3s
    sudo hostlier-controller migrate status

The plan command and apply --dry-run are read-only. Apply prints the current
plan and requires terminal approval; --yes explicitly approves that displayed
plan for automation. Use --json for structured output, and --state-dir / --config
for custom installation paths. The approval fingerprint binds applications,
generation, runtime metadata, installation, systemd unit and server configuration.
It is rechecked after stopping the controller and acquiring its exclusive lock.

## Backups and the writer handoff

The backup directory has mode 0700; archives and the recovery journal have mode
0600. The metadata archive contains the original encryption key and encrypted
credentials. Keep the entire directory private.

1. Persist the recovery journal and startup guard. Create and read back a metadata
   backup through the same validation used by restore.
2. Save each source container's restart policy before disabling automatic restart.
   Stop every owned application revision, including sidecars and superseded
   containers, and verify that all writers stopped.
3. Archive every primary and sidecar volume independently. Verify checksums,
   entry types and paths before restoring anything.
4. Install or repair reviewed k3s assets and wait for its single node to be Ready.
   Restore independent data directories and prebind retained local PVs to normal
   application PVCs. Original Docker volumes remain intact.
5. Deploy private providers before consumers with unchanged immutable images and
   pinned credentials. Verify process readiness and public HTTPS.
6. Commit k3s deployment identities and the recorded runtime, update the
   controller service, release the guard, and restart the controller.

Cold archives preserve numeric UID/GID, permissions, ACLs, extended attributes,
sparse files and contained links. Volumes must use Docker's local driver without
external mount options. Unmanaged containers and other applications cannot share
the migration volumes. Absolute or parent-traversal links and special filesystem
objects require normalization before migration.

Space checks use actual volume allocation and controller metadata, independent
backup/target/rollback copies, and additional headroom. Docker volume size requests
are not quotas. These are local migration recovery copies; copy backups to
independently protected storage for protection against loss of the server.
Application data under migration-data is excluded from metadata-only backups
because it has independent cold archives.

## Margin security and data

Margin-style nonroot services and storage sidecars retain their data and security
declarations. Migration does not strip their security requirements.

| Docker declaration | Managed k3s path |
| --- | --- |
| Numeric nonroot user/group | runAsUser, runAsGroup, runAsNonRoot |
| Read-only root | readOnlyRootFilesystem |
| No new privileges; drop all capabilities | allowPrivilegeEscalation: false; capabilities.drop: ALL |
| Bounded tmpfs and mode | Component-scoped memory emptyDir with sizeLimit; a digest-pinned setup container applies numeric ownership and mode before sidecars start |
| Process limit | Shared Pod PID ceiling no larger than the smallest declared container ceiling |
| No swap | No active host swap, kubelet failSwapOn, and live kubelet PID/swap verification before deployment |
| Custom seccomp | Content-addressed reviewed OCI policy on the node, referenced as Localhost |
| Named AppArmor | Same host-installed profile referenced as Localhost; an absent profile fails startup |

The shared PID budget is **stricter** than independent Docker container budgets:
the primary, sidecars and setup containers share it. The preview explicitly
describes this change and the pinned scratch helper. Application containers keep
all capabilities dropped; the short-lived scratch helper has only the
ownership/permission capabilities needed for its private volume.

The node policy survives later init/update runs. Deployment verifies live node
settings rather than trusting that file alone. Docker-specific seccomp archMap
and conditional includes/excludes must be resolved into explicit OCI architecture
and syscall rules; incompatible extensions are rejected rather than ignored.

These paths use Kubernetes [security contexts](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/),
[Pod PID limits](https://kubernetes.io/docs/concepts/policy/pid-limiting/),
[memory-backed scratch volumes](https://kubernetes.io/docs/concepts/storage/volumes/#emptydir),
and retained, node-affine [local persistent volumes](https://kubernetes.io/docs/concepts/storage/volumes/#local).

## Rollback and interruption recovery

    sudo hostlier-controller migrate rollback --dry-run
    sudo hostlier-controller migrate rollback

Rollback stops the controller and fences both runtimes. It removes target
Deployments and waits for every target Pod to disappear. It then archives the
**current** k3s volume contents before replacing any original Docker data.
Those snapshots are copied back, and Docker groups, probes, current loopback
ports, routes and restart policies are restored. Writes made after cutover are
preserved. Both archive sets, the metadata backup and target data remain available.

An interrupted data copy reuses already verified target snapshots on retry.
The journal records copy completion before starting Docker, preventing a later
retry from overwriting subsequent Docker writes. Failure to prove target shutdown
keeps the guard in place and does not start source writers.

Migration failures automatically attempt rollback with an independent deadline.
After process death, power loss or incomplete rollback, inspect status and run
rollback. A persistent systemd condition and daemon startup check prevent normal
controller startup during unresolved recovery. Do not delete the journal or
migration-active marker to bypass recovery.

Rollback requires application specifications and pinned secret revisions to
remain unchanged. Added resources/fleet hosts or unfinished operations must be
reconciled first. Unrelated current controller metadata is preserved: an old
database snapshot is not restored over newer authorizations or event records.

After a complete rollback, review another attempt with a **new** backup directory:

    sudo hostlier-controller migrate plan --retry --to k3s --backup-dir /var/backups/hostlier-k3s-retry
    sudo hostlier-controller migrate apply --retry --to k3s --backup-dir /var/backups/hostlier-k3s-retry

Retry binds the prior resolved journal, checks cluster assets for drift, retains
previous metadata/data copies, retires fenced previous namespaces, and restores
fresh copies of the current Docker data.

## Scope and validation

The workflow supports standalone, running single-replica applications. Disable
idle scaling and resolve unfinished deployment/drain work first. Managed database
resources and their bindings, fleet controllers/agents, Bridge tunnels, and
retained deleted workloads are blockers. Databases in application sidecars are
copied cold with the application using their existing immutable images.

Tests cover failures at external-effect boundaries, crashes, archive corruption
and traversal, foreign writers, dependency order and data-preserving rollback.
The [disposable VM gate](../test/e2e/VM_TESTS.md#runtime-migration-and-rollback)
uses actual Docker, k3s, systemd, Nginx and verified private HTTPS with a
Margin-style service and independent storage sidecar. Public DNS, ACME issuance
and cloud firewalls remain separate gates.

Verified on 2026-11-10 using a disposable Ubuntu 24.04 arm64 VM: actual
Docker → k3s → Docker transfer, independent primary/sidecar data, pinned
environment/file credentials, live confinement/PID/no-swap checks, controller
restart, and preservation of newer k3s writes during rollback. SIGKILL during
restore left the startup guard effective; journal-driven rollback restored both
volumes. The Linux GNU tar gate verified sparse files, modes, hard links and
symlinks. The full Go suite passed with package concurrency limited to one;
affected-package race checks and vet passed. The repository-wide quality gate
still reports unrelated existing findings; the new migration code has no
findings under its default complexity and comment rules.
