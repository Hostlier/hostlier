# Packages and deployment workflows

Hostlier runs optional Go executables for database and provider work. The controller owns approval, secrets, operation progress, and encrypted state; it does not link database drivers or provider clients. Docker remains the default runtime, with k3s selected at server initialization. Server `config.yaml` still defines supported domains only.

## Install on the server

Build from `hostlier/` for the server's architecture:

```sh
make build packages             # current OS/architecture
make linux packages-linux      # Linux amd64 and arm64
sudo hostlier package install postgres --file ./dist/hostlier-postgres-linux-arm64
sudo hostlier package install redis --file ./dist/hostlier-redis-linux-arm64
sudo hostlier package list
```

Use the matching amd64 artifacts on an x86 server. Add `--state-dir DIR` when the controller uses a custom state directory. Installation is a local administrator command; it requires no controller profile. The controller can start without any optional packages. Existing resource API commands also require the corresponding `postgres`, `redis`, or `neo4j` executable after upgrading to this version.

Packages are trusted code running with controller privileges. Installation validates paths and permissions, stages owner-only files, and atomically activates the executable. Planning pins its SHA-256 digest; replacing it before apply requires a fresh review. Executables receive fixed arguments and bounded private stdin/stdout JSON, with a minimal environment. No project can specify an executable path or shell command.

## Supported execution

| Package | Local instance | Existing administrative connection | Managed instance |
| --- | --- | --- | --- |
| `postgres` | Docker or k3s, persistent storage | Create owned database and scoped login over verified TLS | — |
| `redis` | Docker or k3s, persistent storage | Create ACL user restricted to an explicit key prefix over verified TLS | — |
| `neo4j` | Existing resource API only | — | — |
| `aiven` | — | Delegates to PostgreSQL/Redis engine workflows | Direct PostgreSQL/Valkey API adapter; cloud E2E pending |
| `railway` | — | Delegates to PostgreSQL/Redis engine workflows | Unavailable in the direct adapter pending verified template capabilities |

For PostgreSQL `new-database`, `config.database` may be omitted on a retained step without `phase`. The package then derives a portable `hl_` name from the controller's stable workflow key, which includes the controller namespace, application, and step identity. The same key resolves to the same database through redeploy and recovery; different applications resolve to different names. Set an explicit `config.database` when database identity must remain independent of the application name. The package validates explicit names as a lowercase letter followed by up to 47 lowercase letters, digits, or underscores.

Redis numbered databases are not application isolation. Existing Redis uses a scoped ACL and returns `keyPrefix`; applications must use it. PostgreSQL role privileges are scoped, but an existing server's unrelated grants to `PUBLIC` remain the administrator's responsibility. Existing connections require TLS certificate verification (`sslmode=verify-full` for PostgreSQL, `rediss://` for Redis). An optional `caCertificatePEM` secret/input extends trust. The scoped result returns that CA for application trust configuration; bind it only when the app understands how to use PEM content.

PostgreSQL connection URLs may require SCRAM channel binding with `channel_binding=require`; the package preserves it in the scoped application URL. `disable`, `prefer`, duplicate channel-binding values, and other connection overrides are rejected.

## Local database workflow

```yaml
version: 1
name: analytics
image: ghcr.io/acme/analytics:1.0
port: 3000
route: analytics.example.com
workflow:
  steps:
    - id: database
      package: postgres
      config:
        mode: new-instance
        storage: 1Gi
  bindings:
    DATABASE_URL: database.url
```

Run `hostlier plan --file .hostlier`, then `hostlier deploy --file .hostlier` and approve the displayed plan. The package creates a private resource network/namespace; Hostlier attaches the app before its health check. PostgreSQL local instance database is `app`. `redis` uses the same local schema without a database name. Local connections use the owned private container network; remote existing connections require TLS.

For an existing PostgreSQL server, upload an administrative connection using `hostlier secret set postgres-admin` (hidden prompt, or the existing environment/stdin options), then use:

```yaml
workflow:
  steps:
    - id: database
      package: postgres
      config:
        mode: new-database
        database: analytics
      secrets:
        connection: postgres-admin
  bindings:
    DATABASE_URL: database.url
```

For Redis, use `package: redis`, `mode: existing`, optional `keyPrefix: 'analytics:'`, and an administrative connection secret. Bind `CACHE_URL: cache.url` and `CACHE_KEY_PREFIX: cache.keyPrefix` for a step named `cache`. Hostlier creates the ACL user; merely changing a URL does not create isolated access.

## Direct providers and private step inputs

Direct provider APIs are the default. Install `aiven` and `postgres` separately, upload an Aiven API token as `aiven-api-token`, and use [aiven-direct.hostlier](../examples/aiven-direct.hostlier). Aiven planning reads the explicit region, plan capacity and hourly base price, and apply rechecks the reviewed snapshot before a single create request. No provider credentials are needed by the controller itself beyond encrypted secret transport.

The Aiven step returns `connection` and `caCertificatePEM` as **private outputs**. A subsequent database package can consume them only through an explicitly declared dependency:

```yaml
- id: database
  package: postgres
  needs: [instance]
  config:
    mode: new-database
    database: analytics
  inputs:
    connection: instance.connection
    caCertificatePEM: instance.caCertificatePEM
```

Only ordinary `outputs` can be selected by final application `workflow.bindings`. Administrative `privateOutputs` cannot be bound to the application. Both output classes are encrypted at rest and used for log redaction. The provider step does not return an application URL; the database step returns the scoped URL. See [direct provider details](providers/README.md) for exact supported operations and cloud verification limits.

## Optional infrastructure packages

The separate `hostlier-packages` repository implements Terraform and Pulumi adapters through the typed `packagekit` workflow contract. Both require an administrator-installed, checksum-pinned native bundle catalog before capability discovery or execution. Terraform is in the default package catalog; Pulumi is a separate Go module and optional release build, so it is not yet available from the default Git catalog. Hostlier handles approval and workflow coordination; each infrastructure tool retains its own state and locking. Outputs feed subsequent steps through the same `inputs` and `bindings` contract. VM provisioning also needs explicit Hostlier bootstrap/enrollment before the server can receive deployments. See [the roadmap](../PROPOSAL.md#deferred-infrastructure-packages-terraform-pulumi-and-cloud-providers) for further integration work.

## Deferred: user-connected Fused Engine adapter

Fused integration is deferred. Direct provider packages are the current path and require no Fused Engine.

A future optional `fused` package will let a user connect their own Fused Engine and reuse API operations or workflows they already maintain there. It should fit the same `.hostlier` step model: explicit operation selection, protected credential references, predecessor inputs, and declared output bindings. This extends beyond database provisioning; it is not a prerequisite for Aiven or Railway support.

Hostlier will continue to own deployment approval, operation tracking, encrypted state and application binding. The adapter will invoke the user's explicitly selected Fused operations. Its implementation must verify the Engine's actual execution/versioning contract, distinguish read-only preview from mutations, and define how lost responses are reconciled before retrying. Reconnection must not repeat an external mutation.

The existing `packages/usefused` code and [legacy contract draft](usefused/UNIFIED_APP.md) are experimental groundwork for a narrower database-specific integration, not the supported user-connected Engine feature. Its installer and [Aiven](../examples/aiven.hostlier)/[Railway](../examples/railway.hostlier) examples remain prototype references; they are not part of current setup instructions. No compatible Unified App has been published or tested by this implementation. The eventual adapter may replace that contract.

## Approval, persistence and recovery

Every deployment still requires CLI approval, including workflows. The review contains step configuration, secret revision references, executable digests, and package summaries. Graph validation rejects cycles, missing producers, conflicting bindings and duplicate identities. Steps run in dependency order, then ordinary dependencies and app deployment run.

Local and remote credentials, prepared execution payloads and results are encrypted before reuse. A controller namespace, application name and step ID establish ownership. Successful unchanged steps reuse retained outputs across redeploys and controller restarts. Changed step definitions require a deliberate new identity; application failure/deletion retains databases.

Before invoking a mutating package, Hostlier persists an `uncertain` marker. Lost responses, cancellation, invalid results and failed terminal persistence block automatic retries. There is no automatic reconciliation or workflow resource purge command yet. Inspect the actual resource and retained ownership before recovery; a new step ID is not a safe way to bypass an unknown outcome. No distributed exactly-once guarantee is claimed.

The CLI watches the existing resumable SSE operation stream. Package progress currently consists of controller step-start/step-complete events; resource RPC progress is buffered until that call completes.

## Verification

`make check` runs quality, vet and race tests. Local tests cover real executable packages, CLI approval, graph ordering, private output isolation, encrypted persistence, retained networks, restart reuse, uncertain failures, and executable replacement. Opt-in Docker tests exercise PostgreSQL/Redis TLS and permissions, the packaged resource backup/restore path, and local workflow application attachment. See [E2E commands](../test/e2e/README.md).

Provider tests mock HTTP contracts. Real Aiven provisioning and Railway template deployment require separate credentialed cloud verification. Local TLS tests do not establish public DNS or ACME readiness.
