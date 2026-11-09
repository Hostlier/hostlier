# Go resource controller API

`controller.Options.ResourceManager` implements `model.ResourceManager` from `internal/model/resources.go`. Its `Name()` must match the selected runtime. Dependency planning also requires the runtime's optional `SupportsDependencies() bool` capability.

- POST `/v1/resource-plans` `{spec:{name,type,storage,image?},restoreBackupId?}` returns `{id,fingerprint,expiresAt,generation,spec,changes}`. Planning is read-only. The shared POST `/v1/plans/:id/apply` requires `{approved:true,fingerprint,requestId}` and returns `{operationId}`. CLI review is mandatory.
- GET `/v1/resources` returns `{resources}`; GET `/v1/resources/:name` returns metadata and dependents. Connection credentials are never returned.
- POST `/v1/resources/:name/action` `{action:'start'|'stop'|'restart'|'delete',purge?:boolean}` returns `{operationId}`. Stop/delete are blocked while active applications depend on the resource. Offline Neo4j backup is also blocked while active dependents exist. Ordinary deletion retains credentials and data. Successful explicit purge releases credentials and the resource record.
- GET `/v1/resources/:name/logs` returns bounded `{text}` with uploaded secrets and resource passwords redacted.
- POST `/v1/resources/:name/backups` `{}` takes a logical backup and returns `{backupId,name,type,createdAt,size}`. The optional manager interface is `Backup(context.Context,string,model.ResourceContext)([]byte,error)` and `Restore(context.Context,string,[]byte,model.ResourceContext)error`.
- GET `/v1/backups/:id/export` returns authenticated `{data:<base64>,name,type,backupId}` for off-server export. Server backup files are encrypted, durable, and mode 0600. The controller transport is capped at 256 MiB, while current engine adapters impose a tighter 64 MiB bound; transfers use JSON and streaming large backups is not implemented.
- Restoration is part of a resource plan using `restoreBackupId`. It requires a fresh target name distinct from the source, of the same engine type. The approved operation provisions the target and invokes restore. There is no direct overwrite/restore endpoint.

Credentials `{username,password,database}` are generated only during approved execution, encrypted, and durably committed before provisioning. Passwords have 256 bits of entropy. General records retain specs/status and reference names only. Connection outputs are transient runtime secret bindings.

Application dependency schema follows `internal/model` and configuration validation. Provisioned identity is `<application>-<dependency>`; existing dependencies use `instance`. Changes to a retained resource's type/storage/image fail before mutation. The resource manager must establish authenticated protocol readiness before returning from provisioning. Its returned `Resource.Network` is passed to the runtime only for explicitly declared dependencies.

Interrupted approved jobs are marked `RECOVERY_REQUIRED`; automatic replay is deliberately conservative. A new plan/review is required to retry, and retained encrypted credentials are reused.
