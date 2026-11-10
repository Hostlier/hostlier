# Controller maintenance from the CLI

Controller updates and Docker → k3s migration previews use the existing signed
device login over native gRPC, through `Controller.Command`. They do not open an
SSH connection. Select the saved controller profile with `--server`; otherwise
the current profile is used. Maintenance acts on that controller, not a delegated
fleet host. Use an owner login with all-access permissions. Application-scoped
deployment credentials cannot inspect or update the host.

## Preview migration blockers

```sh
hostlier server controller migrate plan --to k3s \
  --backup-dir /var/backups/hostlier/docker-to-k3s-2026-11-10 \
  --server default
```

The backup path is a **new directory on the server**, outside controller state.
Preview checks the live Docker inventory, committed application definitions,
primary and sidecar volumes, security mappings, host prerequisites and rollback
requirements. It reports the complete plan and blockers without stopping the
controller, installing k3s, creating backups or changing workload data. The plan
can become stale; the local migration command rechecks it before applying.

See [runtime migration](runtime-migration.md) for Margin security and data paths,
verified cold backups, application downtime, rollback and interruption recovery.
Remote migration execution is not exposed by these commands.

## Review and update the controller

```sh
hostlier server controller update --dry-run --server default
hostlier server controller update --server default
```

Preview downloads and retains the latest stable controller bundle from the fixed
official `Hostlier/hostlier` GitHub repository. It verifies the official API
coordinates, GitHub asset digest and published SHA-256 file before extracting or
running the target installer's read-only inspection. It stages files on the
server but does not apply the installation or restart services. Arbitrary release
URLs, uploads, shell commands and execution paths are not accepted.

The review includes the release, architecture, checksum, controller hostname,
runtime, proxy, installer fingerprint, warnings and proposed changes. Reviews
expire after ten minutes and bind execution to the requesting login and source
generation. Changed server conditions require a fresh review. At most three
unconsumed reviews are retained; later previews reclaim expired reviews.

Applying uses the existing server-retained approval protocol. A live all-access
grant can authorize the exact request; other owner devices use the existing
independent owner approval. `--yes` cannot bypass that protocol. The verified
target release must support fingerprint-bound installation; older target
releases return `RELEASE_UNSUPPORTED` before any service change.

An accepted update returns a durable job ID. A separate systemd timer launches a
private copy of the initiating controller outside the API service's cgroup. It
stops `hostlier.service`, verifies the committed reservation and generation, and
runs the staged release's installer with the exact approved fingerprint. It
attempts to start the controller again with a fresh deadline even if installation
fails. The installer preserves the selected runtime and public proxy mode;
workload data is not migrated by a controller update. Follow the displayed
installer changes for any dependency or proxy configuration work.

Active or interrupted operations and active or uncertain recovery jobs block
updates. While an accepted update is running, new protected workload changes,
planning jobs and scheduled backups are blocked. The CLI reconnects across API
downtime and observes the original job without resubmitting installation.

```sh
# Return after durable acceptance, without waiting for API recovery.
hostlier server controller update --detach --server default

# Inspect or resume an already accepted update.
hostlier server controller update-status JOB_ID --server default
hostlier server controller update-status JOB_ID --watch --server default
```

Ctrl-C stops CLI observation; it does not cancel or replay an accepted update.
Queued/running jobs with a lost systemd runner become `interrupted`, including
after reboot. Failed jobs are not automatically retried. Root-only records,
the old runner, staged release and `installer.log` remain under
`STATE_DIR/controller-updates/JOB_ID/` for inspection. An administrator can repair
an incomplete installation with the verified release bundle and the existing
local installer. Controller updates do not promise automatic rollback of package
or proxy changes. Runtime migration has its own backup and rollback workflow.

## Older controllers need one initial upgrade

Updating the client cannot add server-side maintenance support. If the current
controller predates this feature, the CLI returns `CONTROLLER_UPGRADE_REQUIRED`
and leaves it unchanged. An administrator must first install a maintenance-capable
controller through the server console or an existing administrative channel.
The local `hostlier-controller update --dry-run`, followed by a reviewed local
update, remains available where that updater is installed. Subsequent maintenance
can use the saved CLI login without SSH.

## Logical API routes carried by gRPC

| Method | Route | Purpose |
| --- | --- | --- |
| GET | `/v1/controller/capabilities` | Discover installed maintenance support |
| GET | `/v1/controller/migration/plan?to=k3s&backupDir=...` | Read-only migration inspection |
| GET | `/v1/controller/update/plan` | Stage a verified release and retain its review |
| POST | `/v1/controller/update/apply` | Apply an exact approved review (`id`, `fingerprint`, `requestId`) |
| GET | `/v1/controller/update/jobs/ID` | Read the requesting login's durable job receipt |

Remote mutations must be submitted through `/v1/approvals` and executed through
the retained approval receipt. Direct remote calls to the apply route are denied.
There are no new protobuf methods or separate maintenance credentials.

Controller metadata backups retain update receipts for interruption diagnosis but
exclude staged executables, installer logs and collector payloads. Restoring a
backup does not restore or replay a systemd update unit. Keep the original source
stopped as required by controller recovery.

### Prerequisite preparation preview

`hostlier server controller migrate prepare --backup-dir NEW_PREPARATION_DIRECTORY
--server default` reads the reversible swap/k3s quarantine plan using the same
owner-only signed gRPC login. It needs the `migrationPreparation` capability.
The fixed logical GET route is `/v1/controller/migration/preparation?backupDir=...`;
there is no remote preparation apply, rollback, uninstall or runtime-migration
execution route. See [prerequisite preparation](runtime-migration.md#reversible-prerequisite-preparation)
for the separate local administrator workflow and its retained backups.
