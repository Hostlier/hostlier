# Database recovery

Hostlier supports scheduled encrypted backups to S3-compatible storage, native restore rehearsals, retention, portable import, and explicitly approved database upgrades. Database data, controller metadata, and application volumes are separate recovery units. These commands cover managed databases; controller-state recovery is described in [controller recovery](controller-recovery.md).

## Configure storage on the data host

Create a bucket on an off-server S3-compatible service. The data host needs `PutObject`, `GetObject`, `DeleteObject` for its selected prefix and `ListBucket` restricted to that prefix. Hostlier does not create production buckets. The service must support conditional `PutObject` with `If-None-Match: *`; an existing recovery object is never overwritten. External bucket lifecycle rules and administrative deletion operate outside Hostlier's retention guarantees.

Create owner-only files for the S3 access key, secret key, and an independent random 32-byte encryption key encoded as 64 hexadecimal characters. For example, an administrator can generate the encryption key without displaying it:

```sh
umask 077
openssl rand -hex 32 > /etc/hostlier/recovery-key
```

Keep a separate protected copy of that encryption key outside the controller/server. Losing it makes the encrypted objects unrecoverable. The controller state key cannot replace it. Preserve previous encryption keys after rotation; an additional destination alias with the original key can import those older objects.

Save `/etc/hostlier/data.yaml`, mode `0600`:

```yaml
destinations:
  - id: offsite
    endpoint: https://s3.example.com
    region: us-east-1
    bucket: company-database-backups
    prefix: production/host-one
    accessKeyFile: s3-access-key
    secretKeyFile: s3-secret-key
    encryptionKeyFile: recovery-key
```

Relative file references resolve beside this configuration file. All referenced files must be private regular files; symlinks and group/world-readable files are rejected. Production endpoints require verified HTTPS, without embedded credentials, URL paths, query strings or redirects. No environment credential discovery or instance-metadata lookup is used.

Pass `--data-config /etc/hostlier/data.yaml` to `hostlier-controller run` or `hostlier-controller agent run`, or set `HOSTLIER_DATA_CONFIG` in the daemon environment. Configure the destination on the host that owns the database. Distinct host prefixes keep inventories separate. Credentials and encryption keys are never accepted through the remote API or included in approval reviews.

## Approve and observe a backup policy

```sh
hostlier backup destinations
hostlier backup policy daily --resource app-db --destination offsite --every 24h --retain 7
hostlier backup run daily --watch
hostlier backup list
hostlier backup job JOB_ID --watch
hostlier backup disable daily
```

The existing independent hardware-key approval flow covers policy creation, manual runs, disabling, import, upgrade and finalization. Email enrollment alone grants no approval authority. Scheduled runs reuse the exact approved source specification, package identity, destination fingerprint, interval and retention. They do not need a fresh hardware touch each time. Revoking the approving human key stops future scheduling. Changing the destination or encryption key invalidates old policy authority. Policy names are immutable: disable the old policy and approve a new name to change its scope.

Intervals range from one hour to 31 days; retention is 1–100 verified copies. Scheduling runs approximately once per minute and does not replay every missed interval after downtime. Each run reserves its source, takes a native snapshot, restores it into a separate temporary database, queries that database, encrypts its data and verification manifest with AES-256-GCM, uploads an immutable object, and downloads/authenticates it again. It purges the temporary database before retention. Provision capacity for that temporary copy.

Retention considers only known verified objects from the exact policy/destination and reauthenticates a surviving copy before deleting older ones. Upload or read-back failure never authorizes deletion of older recovery points. A failed run can leave an unverified object or reserved temporary resource for inspection; it is not advertised as a verified recovery point.

For an enrolled host, add `--on HOST` to commands. The central controller dispatches due runs over pinned mutual TLS and rechecks the original key's revocation status. A host cannot autonomously continue a centrally created schedule while the central controller is disconnected. An accepted in-flight run can finish after revocation; no new run is accepted on that revoked scheduling authority.

## Import and verify on a replacement controller

Configure the original destination and independently retained encryption key, then:

```sh
hostlier backup objects offsite
hostlier backup import OBJECT_ID --destination offsite
hostlier resource restore postgres recovered-db --storage 10Gi --backup LOCAL_BACKUP_ID
```

Use the engine type and storage appropriate to the source. Import authenticates the entire envelope, native payload digest, engine metadata and evidence before returning a local backup ID. Restore requires a new target and its own exact-plan approval. The stored image identity is preserved; install the matching current Hostlier resource package. The restored target must pass native evidence comparison before the operation reports success. Imported objects do not join a new policy's automatic retention history.

## Supported Docker upgrades

| Source | CLI target | Real integration versions |
|---|---|---|
| PostgreSQL 16 | `--to 17` | 16.11 → 17.11 |
| Redis 7.4 | `--to 8.2` | 7.4.2 → 8.2.11 |
| Neo4j 5.26 | `--to 2025.01` | 5.26.5 → 2025.01.0 |

Target images are pinned by digest in `internal/model/database_upgrades.go`; arbitrary image replacement is rejected. These are validated transitions, not a promise of arbitrary-version migration. Neo4j uses its native product version: early 2025 releases report the older `5.27.0` kernel alias through `dbms.components()` ([Neo4j changelog](https://github.com/neo4j/neo4j/wiki/Neo4j-2025-changelog)). The migration path follows the [Neo4j 5.26 checkpoint](https://neo4j.com/docs/upgrade-migration-guide/current/version-2025-2026/upgrade/).

Detach application dependencies before upgrade maintenance. Then:

```sh
hostlier resource upgrade app-db --target app-db-next --to 17 --destination offsite --watch
hostlier resource upgrade-status JOB_ID --watch
hostlier resource cutover JOB_ID --watch
```

Preflight creates an independent target and verifies both actual versions. Each database is fenced onto its own internal Docker network containing no other container, with automatic restart disabled. Published database host ports are rejected. The source snapshot is taken behind that fence, the source is stopped, and the new target is restored. PostgreSQL uses native dump/restore, Redis loads its RDB and re-establishes durable AOF, and Neo4j loads and explicitly migrates its offline dump. Native source/target evidence must match, and the encrypted off-server recovery copy must pass read-back verification.

The job then waits at `awaiting-cutover`. A separate hardware approval authorizes cutover, which rechecks the remote recovery object, the stopped source and target evidence before reconnecting the new target. Rebind applications to its new resource name through their normal approved deployment plans. The original source remains stopped, reserved and retained; these workflows never automatically purge that original data.

Before cutover starts, a separately approved abort removes the temporary target before reconnecting and starting the original source:

```sh
hostlier resource abort-upgrade JOB_ID --watch
```

This command also cleans up failed backup verification jobs. Once a cutover attempt starts, abort is blocked because the target might have accepted writes. An uncertain cutover can be retried with a fresh `resource cutover JOB_ID` approval only while the original evidence still matches. If writes changed it, operator inspection is required; restarting the old source is not a data-compatible rollback.

## Failure and coverage limits

- Restarted queued/running jobs become `uncertain` and retain reservations. They are never blindly replayed. Inspect the retained job phase before approving abort or cutover reconciliation.
- Native snapshot transport is bounded to 64 MiB; the complete encoded encrypted bundle is also bounded. Streaming multipart snapshots are not implemented. S3 discovery scans at most ten pages of 1,000 entries under the prefix.
- PostgreSQL covers the managed application database, with table content, column metadata and sequence-state comparison. It does not back up cluster-wide roles or other databases, and it is not point-in-time recovery. Custom extensions must be available in the pinned target or restore fails.
- Redis verification covers databases 0–15 and string, list, set, sorted-set and hash values, including absolute key expiration. Streams and module-defined types fail verification. Expired keys may disappear; unexpired keys or changed values may not.
- Neo4j Community snapshots require offline maintenance and detached application dependencies. Recovery covers the `neo4j` graph, relationships and constraints, not system users or other databases.
- Docker upgrades have real-engine/S3 integration coverage. Kubernetes backups and imports use the existing native Kubernetes adapter and the same evidence checks; Kubernetes major-version upgrades are rejected before source mutation because exclusive-writer fencing is not implemented. This test run did not exercise a live Kubernetes cluster.
- Root access to the data host/runtime remains the administrative trust boundary. Hardware approval cannot protect against an agent that already controls that root boundary.

Run the isolated integration matrix against an explicitly selected local Docker daemon:

```sh
HOSTLIER_RECOVERY_DOCKER=1 \
HOSTLIER_DOCKER_SOCKET=/var/run/docker.sock \
go test ./internal/resources -run TestLiveRecoveryTransitions -v -count=1 -timeout 15m
```

The fixture uses a separate S3-compatible gateway container and disposable database volumes. It checks encrypted download, immutable object protection, native restore, corrupt-snapshot rejection, content equality and stopped-source cutover behavior. Controller tests cover approval isolation, central scheduling/revocation, retention and uncertain outcomes separately.
