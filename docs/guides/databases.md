# Database resources and bindings

[All guides](../../GUIDE.md)

Optional provider packages also support ordered provisioning steps in `.hostlier` through `workflow.steps`, `needs`, private inputs and output bindings. The controller executes these before app deployment under the same CLI approval. See [package workflows](../../packages/WORKFLOWS.md) for local PostgreSQL/Redis, existing connections, direct provider adapters, installation commands and examples. Packages install with `hostlier package install NAME --file BINARY`; the controller stays independent of engine drivers. Aiven cloud execution awaits live verification; direct Railway instance creation remains unavailable. A user-connected Fused Engine adapter is deferred; it will enable reuse of existing Fused API operations and workflows.

The built-in engines are PostgreSQL, Redis, and Neo4j. Creation requires review of the engine, pinned image, private network, storage, and retention plan:

```sh
hostlier resource create postgres reporting --storage 10Gi
hostlier resource list
hostlier resource status reporting
hostlier resource logs reporting
```

Attach an existing resource in `.hostlier`:

```yaml
dependencies:
  database:
    type: postgres
    source: existing
    instance: reporting
bindings:
  DATABASE_URL: database.url
```

To create a resource with the application's approved plan, use `source: provision` and `storage: 10Gi` instead of `instance`. Its identity is `<application>-<dependency>`. PostgreSQL and Redis expose the `url` binding; Neo4j exposes `uri`, `username`, and `password`. Credentials are encrypted before provisioning and passed to applications transiently. Database ports are private, and active application references block resource stop/deletion.

Redis uses AOF with `appendfsync everysec`; abrupt host failure can lose approximately one second of acknowledged writes. Neo4j Community backup requires an offline maintenance window. The controller blocks this backup while active applications depend on the resource.

```sh
hostlier resource backup reporting --output /secure/path/reporting.backup
hostlier resource export BACKUP_ID --output /secure/path/reporting-copy.backup
hostlier resource restore postgres reporting-restored --storage 10Gi --backup BACKUP_ID
```

Restore requires a fresh target of the same engine type and a new approved plan. Server backup files are encrypted; exported files contain sensitive database data and are created mode 0600. Current engine backup transport is bounded to 64 MiB. Portable encrypted S3 objects can be discovered and imported on a replacement controller with the independent recovery key; raw exported-file import is not implemented.

`resource start|stop|restart|delete NAME` manages a resource. Ordinary deletion retains data and credentials. `resource delete NAME --purge` explicitly removes owned data after confirmation. Explicit database upgrades support PostgreSQL 16→17, Redis 7.4→8.2 and Neo4j 5.26→2025.01 on Docker. Each workflow creates and verifies a separate target, retains the stopped source, and requires independent cutover approval. Runtime migration and Kubernetes major-version upgrades remain unsupported.

See [IMPLEMENTATION.md](../../IMPLEMENTATION.md) for architecture and recovery details, and [RESOURCE_API.md](../../RESOURCE_API.md) for resource wire contracts.

Scheduled encrypted S3 backups, retention, portable import and verified upgrade procedures are documented in [database recovery](../database-recovery.md). Install their agent instructions with `hostlier skills install backup` (or omit `backup` to install every bundled skill).
