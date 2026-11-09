# Local custom products

`hostlier product plan --file product.yaml` expands a local declaration and displays the ordinary controller deployment plan. `hostlier product deploy --file product.yaml` requires interactive approval of that exact plan before packages or the application change. Package binaries must already be installed by the server administrator. Product documents cannot supply executable paths, scripts, new providers, or plaintext secrets.

```yaml
version: 1
product:
  version: 1
  name: local-data-app
  revision: "1.0.0"
  policy:
    maxReplicas: 1
    allowZero: false
    maxCPU: 2
    maxMemoryBytes: 536870912
  inputs:
    storage:
      type: string
      required: true
      enum: [1Gi, 2Gi]
  steps:
    - id: database
      package: postgres
      config:
        mode: new-instance
      configInputs:
        storage: storage
    - id: cache
      package: redis
      config:
        mode: new-instance
        storage: 1Gi
  bindings:
    DATABASE_URL: database.url
    REDIS_URL: cache.url
inputs:
  storage: 1Gi
app:
  version: 1
  name: example
  image: example/app:latest
  port: 8080
  resources:
    cpu: "1"
    memory: 256Mi
  route: example.your-domain.com
```

Public inputs support `string`, `integer`, and `boolean`, optional `default`, and optional `enum`. A step's `configInputs` maps a package configuration field to an input name. `needs` and `inputs` retain the ordinary workflow dependency and private-output reference rules. Environment bindings select package outputs.

For an installed package requiring a stored credential, declare `product.secrets: [admin]`, map the step's `secretInputs.connection: admin`, and supply `secretRefs.admin: stored-secret-name`. Only the stored name enters the local file; the controller resolves and protects the value using its existing secret workflow.

Go authors can construct `pkg/product.Definition` and call `product.Compose` to produce a `workflow.Spec`. This is a pure composition API. The resulting ordinary workflow retains installed-package snapshotting, approval fingerprints, encrypted state, dependency ordering, and environment binding checks.

## Package lifecycle

`pkg/packagekit.Serve` implements the bounded stdin/stdout JSON boundary for a `Handle(context.Context, workflow.Request)` implementation. `pkg/packagekit/contracttest` supplies reusable exchange and malformed-request checks. `workflow.Capability` describes a package's configuration schema, secret aliases, outputs, and optional inspection support.

The version-one protocol supports `plan`, `apply`, and optional read-only `inspect` (`reconcile` is an observation alias for the native engines). An inspection returns `completed` with a validated result only after actual ownership and the approved credentials/outcome have been verified. `not_started` requires authoritative absence of every package effect. `uncertain` contains no result and must not cause blind mutation replay.

Native PostgreSQL/Redis inspection authenticates against an existing owned runtime object. Missing native metadata remains uncertain because retained storage or a partially created runtime object may exist. Remote PostgreSQL inspection checks role/database absence or authenticates the approved role and verifies database ownership, restricted privileges, and revoked PUBLIC access. Remote Redis inspection verifies the exact generated password hash, flags, commands, key namespace, empty channels/selectors, and authenticated PONG. None of these inspection paths issue provisioning commands.

Optional native PostgreSQL and Redis binaries expose `describe` as a static version 1 JSON contract. `hostlier package describe postgres --state-dir DIR` discovers an installed executable through the same protected, bounded snapshot runner as workflow execution. Discovery supplies no project configuration, credentials, or private inputs and returns configuration and output schemas plus supported secret alias names.

Product inputs can configure a package's supported storage options. Retained resource resizing remains an independently reviewed resource operation.

## Durable scaling policy

`product.policy` declares `maxReplicas` (required, 1–100), `allowZero` (default false), optional `maxCPU` (cores), and optional `maxMemoryBytes` (bytes). A maximum requires an explicit corresponding application resource limit. The controller captures the policy in durable application state, includes it in the review, and checks deployment, scaling, and resolved rollback inputs before package planning. Omitting the workflow or policy from a subsequent direct API deployment does not remove the captured limits. Restart does not remove them.

An explicitly declared replacement policy enters the ordinary exact-plan approval. The current operation must satisfy both the retained and proposed constraints; a successful deployment captures the new policy for subsequent operations. This allows deliberate reviewed policy updates without a silent omission bypass.

## Lifecycle hooks

Any installed-package step can declare `phase: beforeDeploy` or `phase: afterSuccess`. Omitted phase retains the dependency's stable resource key and reusable result. Before hooks run before application startup. After hooks run only after the runtime reports successful readiness and cutover; they cannot provide application environment bindings or feed startup steps. Ordinary `needs`, private `inputs`, installed-executable snapshots and the same exact-plan approval apply. Projects cannot introduce shell commands or executables. The complete graph remains bounded to 32 steps and package execution retains its existing time/output limits.

Hook keys belong to one reviewed deployment (`STEP@PLAN_ID` in `workflow list APP`). Each new approved deployment runs its hooks once; repeated requests for the same accepted operation do not rerun its effects. A lost response leaves the exact hook uncertain. Use `workflow inspect APP STEP@PLAN_ID`, then the explicitly reviewed `workflow reconcile APP STEP@PLAN_ID`. Read-only inspection must verify the original outcome and approved credentials before publishing outputs. Unknown outcomes stay blocked. A new deployment is a new lifecycle event, so its hooks run again. Recovery inspects the original operation's predecessor keys, including hook predecessors.

A failed after-success hook fails the operation while retaining the actual healthy application deployment and its revision. It does not roll back successful application readiness or replay a potentially completed hook. Inspect and reconcile the retained hook before choosing any subsequent deployment.

## Hosting SDK authoring adapter

The parent SDK exports `exportHostlierProduct(template, options)` from its main entry point. It calls `resolveProductDefinition` once in the authoring process and emits an ordinary JSON product document accepted by `hostlier product plan|deploy --file`. The Go controller needs no Node runtime. Options select portable `name`, `revision`, public `inputs`, logical `secretRefs`, URL inputs, and optional policy. Required secret inputs are resolved using stored reference names; supplying secret values through `inputs` is rejected.

The current adapter supports `kind: container` with an explicit public URL policy, public environment/input bindings, resource limits, a single application data volume, and one or more PostgreSQL `provision` / `database` dependencies through the installed native package. It rejects private URL policies (the version-one deployment document cannot preserve private access), unsupported native product kinds, runtime options, storage-class overrides, table/existing ownership modes, custom database images/passwords and unsupported transport/deletion overrides instead of dropping their intent. Hostlier retains resources on ordinary deletion and requires separately approved purge. Authoring templates execute only in the trusted SDK caller; exported documents are declarative.
