# Hostlier database configuration packages

Package development now has a dedicated private repository: [Usefused/hostlier-packages](https://github.com/Usefused/hostlier-packages). It contains independently buildable executables, their SDK, tests and sample workflows. The copies in this checkout remain for compatibility with existing builds during migration.

This page describes the older standalone **offline configuration previews** in `packages/aiven` and `packages/railway`. The capability matrix below expresses schema intent, not implemented provisioning. Use [deployment workflows](WORKFLOWS.md) for executable PostgreSQL/Redis packages, direct provider adapters, installation and supported operations. Direct APIs are the default; the user-connected Fused Engine adapter is deferred. Direct Railway managed creation is not yet available.

`hostlier-package plan` prints `executable: false` and establishes schema validity only. Its standalone schema differs from `.hostlier` workflow steps. Server `config.yaml` remains domain-only.

## Try the packages locally

From `hostlier/`:

```sh
go run ./cmd/hostlier-package plan aiven packages/aiven/examples/new-instance.yaml
go run ./cmd/hostlier-package plan aiven packages/aiven/examples/new-database.yaml
go run ./cmd/hostlier-package plan railway packages/railway/examples/new-instance.yaml
go run ./cmd/hostlier-package plan railway packages/railway/examples/new-database.yaml
go test ./packages/... ./cmd/hostlier-package
```

Build a separate optional validator with `go build -o dist/hostlier-package ./cmd/hostlier-package`. Commands only read the supplied file and print JSON. They require no accounts, network, Docker or database server. Example provider IDs, plans and template IDs are placeholders to replace with actual reviewed provider selections.

## Configuration modes

Each dependency declares `version: 1`, an application `app`, dependency `name`, `engine`, `mode` and destination `outputSecret`.

| Mode | Required configuration | Intended deploy behavior |
| --- | --- | --- |
| `new-instance` | Provider-specific `instance` settings | Create an owned service before the first app deployment, then reuse its persisted provider identity on redeploy. |
| `new-database` | `connectionSecret` and `database` | Create a named database and scoped application user through an existing administrative connection. Preserve the shared parent instance. |

For engines supporting named databases, `database` is also required for new instances. MongoDB additionally requires an initial `collection`. Database and collection identifiers are deliberately restricted to lowercase letters, digits and underscores, starting with a letter, with a maximum of 48 characters. App and dependency names use lowercase letters, digits and hyphens, up to 63 characters.

Secret fields contain **names of secrets**, never passwords, tokens or connection URLs. `connectionSecret` refers to a complete administrative connection with enough privileges to create the database and scoped user. `outputSecret` identifies the resulting application connection and must differ from source connection and API-token references. Source credentials must never become app bindings. The future executor must resolve secrets transiently from the controller's encrypted store and verify database TLS certificates.

New-instance settings and existing connections are mutually exclusive. Unknown fields, duplicate YAML keys, aliases, multiple documents and oversized input are rejected. Parser diagnostics do not echo input values.

## Engine capabilities

| Package | Engine | New instance | New database within existing instance |
| --- | --- | --- | --- |
| Aiven | `postgres` | Yes | Yes |
| Aiven | `mysql` | Yes | Yes |
| Aiven | `clickhouse` | Yes | Yes |
| Aiven | `valkey` | Yes | Unsupported |
| Aiven | `opensearch` | Yes | Unsupported |
| Railway | `postgres` | Yes | Yes |
| Railway | `mysql` | Yes | Yes |
| Railway | `mongodb` | Yes | Yes, with an initial collection |
| Railway | `redis` | Yes | Unsupported |

“Yes” describes supported configuration and planning intent, not implemented cloud execution. Aiven's current Redis-compatible offering is named Valkey. These engine choices follow [Aiven's service catalog](https://aiven.io/docs/products/services) and [Railway's official database templates](https://docs.railway.com/databases). Other Railway marketplace databases are outside this initial package contract.

Redis/Valkey numbered namespaces and OpenSearch indexes are not treated as independent named application databases. [Redis explicitly advises against using logical databases for unrelated applications](https://redis.io/docs/latest/commands/select/). MongoDB's initial collection makes creation concrete before the app writes data; see [MongoDB database and collection creation](https://www.mongodb.com/docs/manual/core/databases-and-collections/).

## Provider configuration

Aiven new instances require `instance.tokenSecret`, `project`, `service`, and `plan`; `cloud` is optional because some tiers choose their own placement. The eventual provider preflight must validate the actual engine, plan, region and price before approval. See the [new-instance](aiven/examples/new-instance.yaml) and [existing-instance](aiven/examples/new-database.yaml) examples. Creating a ClickHouse database must honor Aiven's [Replicated engine and administrative privilege requirements](https://aiven.io/docs/products/clickhouse/howto/manage-databases-tables).

Railway new instances require `instance.tokenSecret`, `tokenType` (`account`, `workspace`, or `project`), `project`, `environment`, `service`, `template`, and explicit `publicAccess: true`. Template identifiers remain explicit; the eventual preflight must verify the engine, resolve a reviewed immutable deployment specification, and show volume capacity, networking and cost. See the [new-instance](railway/examples/new-instance.yaml) and [existing-instance](railway/examples/new-database.yaml) examples. Account/workspace credentials and project tokens have different [API authentication headers](https://docs.railway.com/integrations/api).

An ordinary Hostlier server is outside Railway's private network. Railway previews therefore include external connectivity and egress costs: a private Railway hostname is insufficient. A TCP proxy does not itself establish verified database TLS; the eventual adapter must configure and verify TLS or reject the plan. See [Railway external PostgreSQL connections](https://docs.railway.com/databases/postgresql) and [database networking](https://docs.railway.com/databases/reference). Existing connections must already be reachable; `new-database` does not modify provider networking.

## Workflow and execution contract

Previews describe `before-app-deploy` steps: provider preflight and instance creation when needed, readiness, verified engine connection, ownership checks, database creation, MongoDB collection initialization, application credential isolation, and protected output publication. Named database ownership is checked on reuse in both modes. Every engine, including caches and search, requires application-scoped credentials before publication; a future executor must reject engines or configurations that cannot provide that isolation. Previews include the complete configuration, a fingerprint, and stable resource key `<package>/<app>/<dependency>`. Changes alter the fingerprint but do not allocate a new dependency identity. Resource keys must be namespaced by controller identity when persisted externally.

The offline preview steps below remain declarative intent. The implemented controller workflow layer is documented in [WORKFLOWS.md](WORKFLOWS.md); the following provider execution obligations also apply to the hosted deployment Unified App:

1. Resolve provider/engine capabilities and current desired state into a concrete plan, including provider IDs, exact template specification, storage, exposure and cost. Require the existing CLI approval flow; offline fingerprints are not controller apply tokens.
2. Persist ownership and operation state before side effects, reconcile uncertain API results, and reuse instances and databases on retries/redeploys. Refuse to adopt matching unowned resources. A changed engine, connection, project, or environment requires an explicit migration/replacement plan.
3. Use provider-specific API adapters for instances and engine-specific drivers for databases/users. Verify administrative privileges, TLS, readiness, and scoped app credentials before publishing the output secret. Do not substitute a connection URL rewrite for actual database creation.
4. Bind the protected output into the application only after successful provisioning, then run the existing app deployment. A failed app deploy retains database resources for recovery. App deletion retains data; any future purge must require separate approval and must never delete a shared parent instance.
5. Implement local fake-provider and real-engine integration tests for retries, lost responses, duplicate ownership, scoped access, TLS failures and secret redaction before claiming live support. Cloud E2E requires disposable provider projects and credentials.

Current tests cover the provider/engine/mode matrix, invalid combinations, strict YAML, secret-reference collisions, fingerprint stability, and complete local file-to-plan execution for all four standalone examples. Workflow tests additionally cover controller execution, CLI approval, private bindings, restart reuse, failure handling and the Usefused HTTP contract. They do not certify live provider provisioning.
