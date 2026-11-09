# Hostlier implementation

Hostlier is a standalone Go module (`hostlier`, Go 1.27.3 or later). The CLI and controller build into the same static executable. Production use requires no Node.js runtime. The parent TypeScript Hosting SDK remains a separate project. Local Go product definitions compose Hostlier packages through `pkg/product`.

## Packages

| Package | Responsibility |
| --- | --- |
| `cmd/hostlier` | Client executable, local dashboard, and installation tooling |
| `cmd/hostlier-controller` | Independent daemon and offline recovery executable |
| `internal/cli`, `internal/client` | Commands, interactive approval, HTTPS transport, local profiles and credentials |
| `internal/config`, `internal/model` | Strict YAML/JSON contracts, domain and route validation, shared runtime interfaces |
| `internal/controller` | Authentication, pairing, approval plans, durable operations, lifecycle, secrets and resource references |
| `internal/store` | Transactional embedded SQLite state, offline snapshots, OS advisory writer lock, AES-GCM encryption |
| `internal/runtime` | Owned Docker and k3s workloads, Caddy/Nginx routing, scaling, measurements and persistence |
| `internal/resources`, `internal/resourcepackage` | PostgreSQL, Redis and Neo4j engine implementations, linked into optional package executables only |
| `internal/resourceclient`, `internal/packageexec` | Generic resource RPC, executable installation, digest verification and private workflow transport |
| `pkg/workflow`, `packages/engines`, `packages/providers` | Workflow contract, native scoped database access, and direct optional provider adapters |
| `pkg/packagekit`, `pkg/product` | Bounded optional package lifecycle protocol, contract tests and local product composition |
| `internal/nginx` | Owned Nginx configuration, certificate issuance/renewal and validated reloads |
| `internal/installer` | Read-only installation inspection and approval-scoped Ubuntu/systemd/firewall setup |
| `test/e2e` | Opt-in real Docker/Caddy lifecycle checks |
| `internal/quality`, `cmd/quality` | Dependency-free complexity and comment coverage gate, including test functions and closures |
| `packages/database` | Shared offline dependency validation, strict decoding and preview contracts |
| `packages/aiven`, `packages/railway` | Provider-specific configuration, engine capabilities and declarative preview steps |
| `cmd/hostlier-package` | Offline file-to-preview command and experimental legacy Usefused installer |

The module uses `yaml.v3` for strict YAML parsing and `x/term` for terminal input. All public transport uses normal TLS verification; a supplied private CA extends trust rather than bypassing verification.

CLI invocation state lives in a session with separate command groups for enrollment, installation, deployment, resources and administration. App and resource provisioning share the same plan preview, approval, stale-plan refresh and operation-watching workflow. Lifecycle actions and log following also share their execution paths.

Controller request routing, identity, secrets, plans, domain changes and operation execution are separate concerns. Durable schemas are grouped by identity, secrets, deployments and controller state; adapter-specific execution snapshots stay near their owning runtime. Docker and k3s deployment transactions have explicit preparation, activation and rollback stages. Engine-specific backup/restore helpers retain their own authentication and persistence rules.

The optional [provider configuration packages](packages/README.md) remain outside the controller. Their previews validate local configuration and describe intended steps; they do not resolve credentials, call cloud APIs, execute SQL or provision dependencies. Their offline fingerprints are not controller approval tokens. Executable workflows are separate: see [package workflows](packages/WORKFLOWS.md). PostgreSQL/Redis drivers and direct provider clients stay out of the controller dependency graph.

## Controller and state

The controller exposes a protected Unix socket for local administration and a loopback HTTP listener behind the selected Caddy or Nginx edge. Direct TLS listeners are also supported. TCP management requires a per-client bearer credential; health and bounded enrollment endpoints are explicit exceptions. Local authority comes from the Unix listener, never a request header.

Server defaults are `/etc/hostlier/config.yaml`, `/var/lib/hostlier`, `/run/hostlier/controller.sock`, and `/var/log/hostlier/hostlier.log`. Installation records the selected runtime and controller hostname. `config.yaml` contains supported domains only.

`state.db` is a mode-0600 SQLite database using full synchronization and an exclusive controller writer lock. Schema version 1 stores the controller aggregate in one JSON payload row: each save atomically commits operation/workflow state with its events and cursors. This is transactional persistence, not a normalized per-entity SQL schema. Runtime and package metadata retain their own protected files and are included in controller recovery bundles.

The original `state.json` migrates with its original encryption key, ownership IDs and event sequence, and is retained after successful migration. SQLite is authoritative thereafter. Incompatible controller/schema versions and missing or wrong keys fail closed. External runtime actions remain outside database transactions; SQLite cannot atomically commit a container or database mutation. See [controller recovery](docs/controller-recovery.md).

Secrets and database credentials are encrypted with AES-GCM using a separate mode-0600 `secret.key`. Metadata contains only encrypted revisions and references. A missing key is an error; the controller never replaces it while existing state is present. Preserve the original key alongside an independently protected backup if server recovery is required. Metadata alone cannot recover secret values.

Execution logs record sanitized transitions with operation identifiers, bounded retention, and mode-0600 files. Startup, listener readiness, recovery and shutdown are logged. Log write failures produce stderr/journal diagnostics. Application logs remain separate and are redacted before API delivery.

## Approval and recovery

Planning validates the project, route ownership, secret revisions, resource specifications, image digest and backend capability without provisioning. Plans are bound to the authenticated client, fingerprint, expiry and server generation. Applying requires matching explicit approval and an idempotency request ID. A replay returns the original operation; altered, expired or stale plans fail.

Accepted work executes independently of the CLI connection. Credentials for approved resources are generated, encrypted and committed before provisioning. Bindings and dependency network access are delivered transiently to the runtime. Resource managers establish authenticated protocol readiness before application activation.

The authenticated [operation event stream](STREAMING.md) exposes durable log cursors and operation snapshots independently of job execution. Subscriptions rotate, reauthenticate and replay retained events after reconnect; slow writes do not hold the controller lock. The CLI sends progress to stderr and final operation JSON to stdout, with polling compatibility for older controllers.

After controller restart, an operation previously marked queued/running becomes `interrupted` with `RECOVERY_REQUIRED`. Operators inspect the runtime and approve a fresh plan before retrying. For uncertain package steps, `workflow inspect` and `workflow reconcile` use the exact approved executable digest, original prepared credentials and verified predecessor outputs; only independently verified completion or absence can change recovery state. This conservative policy avoids repeating uncertain mutations. Retained resource credentials are reused; they do not rotate during reconciliation.

Ordinary application/resource deletion retains owned data. Purge is explicit. Active resource references block resource stop/deletion; offline Neo4j backups also require detaching active dependents. Restore is approved as provisioning to a fresh target. See [RESOURCE_API.md](RESOURCE_API.md) for endpoints and current backup transport limits.

## Verification and remaining scope

`make check` enforces complexity and comment coverage, runs static analysis and executes unit/integration tests with race detection; live Docker/resource gates are opt-in. Tests using injected runtime adapters establish controller invariants, not actual deployment readiness. [The E2E guide](test/e2e/README.md) identifies the real-service commands and their limits.

The current requested verification scope is local: Docker/Caddy with a trusted local CA, database protocol/persistence/backup tests, and disposable Ubuntu VM checks. Public DNS, public ACME issuance/renewal and browser trust are unrun. Full k3s installation, resource parity, reboot, firewall and recovery acceptance must be established by the corresponding real VM gates. Passing a unit suite does not complete either proposal iteration.

Implementation exclusions include source builds, hosted registry services, runtime migration, untrusted tenants, automated database major-version upgrades (explicit, approved Docker upgrades are supported) and importing a locally exported backup file into another controller (S3 backup objects can be imported; see [database recovery](docs/database-recovery.md)).

## Deferred Fused integration

A future optional adapter will connect to a user's existing Fused Engine to reuse API operations and workflows through Hostlier's approved step model. The checked-in database-specific `packages/usefused` executor is experimental groundwork, not the completed general integration. Direct provider packages remain the current default. See [the deferred scope](packages/WORKFLOWS.md#deferred-user-connected-fused-engine-adapter).
