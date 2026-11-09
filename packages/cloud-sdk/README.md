# Hostlier Cloud SDK

This package lets an application vendor use its bundled `.hostlier` as the template for customer installations. It calls the Hostlier controller API directly, using the same signed device requests, retained approvals, plans, and operations as the CLI.

The backend uses native gRPC on the existing controller URL. Approvals, planning and operations send updates on server streams; a dropped connection resumes observation of the same job. Upgrade the controller and its HTTP/2 proxy before reinstalling the SDK; there is no HTTP fallback.

Install the SDK from the Hostlier CLI into a Node project:

```sh
hostlier sdk install cloud --directory /path/to/your/backend
```

The CLI carries the SDK code and its YAML parser and gRPC client. Installation needs no npm, Go toolchain, registry, or controller connection. The backend needs Node.js 24 or newer, but does not need the CLI at runtime. Rerun the install command after changing Hostlier CLI versions; a package manager that recreates `node_modules` may also require rerunning it. The existing `hostlier sdk install --version ...` command remains the separate Go package-authoring SDK.

Enroll a dedicated service device through `hostlier connect` using the product author's owner account, grant it the required controller access, then export its credential once:

```sh
hostlier sdk credential export --server production --output /run/secrets/hostlier-sdk.json
```

Keep the exported file private (mode `0600`) and supply it to the backend as a secret. It contains the controller URL and a `device-v1` bearer token plus Ed25519 signing key. The SDK requires that signed credential; a bearer-only profile cannot authorize it. Remote controller URLs must use HTTPS. The CLI is needed only for enrollment, credential export, and future credential rotation.

```js
import { createHostlierCloud } from '@hostlier/cloud-sdk';

const hosting = await createHostlierCloud({
  bundlePath: new URL('./vendor/.hostlier', import.meta.url).pathname,
  stateDirectory: '/var/lib/my-backend/hostlier-instances',
  credentialFile: '/run/secrets/hostlier-sdk.json',
});

await hosting.create({
  name: 'acme-app',
  route: 'acme.apps.example.com',
  env: { CUSTOMER_ID: 'acme' },
  secrets: { API_KEY: 'secret://acme-api-key' },
  compute: { cpu: '1', memory: '1Gi' },
});
await hosting.setCompute('acme-app', { cpu: '0.5', memory: '512Mi' });
await hosting.setEnvironment('acme-app', { FEATURE_ENABLED: 'true' });
```

The SDK copies the complete template into its durable state directory after a successful create. It keeps image, health, security, inline managed configs, sidecars, volumes, dependencies, workflow and secret bindings in the generated project. `setCompute` submits a scaling plan; `setEnvironment` deploys the complete saved project with the changed environment. A new template digest cannot silently update an existing instance.

For a template with a workflow step named `database`, package `postgres`, and `config.mode: new-database`, `create({ databaseName: 'workspace_acme' })` sets only that step's `config.database`. The name must match PostgreSQL's portable identifier rule: a lowercase letter followed by at most 47 lowercase letters, digits, or underscores. Supply a name derived from a stable customer ID when database identity must survive app or route changes. The resolved name is saved in the instance project and in any pending deployment intent, so later updates and recovery use the same database. The template still controls the package, mode, administrative connection reference, and all other workflow settings.

If `databaseName` is omitted, leave `config.database` out of the template to let the PostgreSQL package derive a distinct stable name from the retained workflow key. The step must be an ordinary retained step, without a per-deployment `phase`:

```yaml
workflow:
  steps:
    - id: database
      package: postgres
      config:
        mode: new-database
      secrets:
        connection: postgres-admin
  bindings:
    DATABASE_URL: database.url
```

A literal `config.database` in the template remains literal for every customer; use the override or omit that field for shared templates.

For repeated desired-state delivery, call `ensure()` with the same create input. It creates a missing instance once; for a saved instance it compares the full requested project (including route, environment, secret references, and database identity) with the retained create intent, then checks the saved project against the controller before returning a status with `ready`. A changed request is rejected instead of changing an existing instance. `create()` remains create-only. Later SDK updates invalidate this creation retry identity; use the explicit update methods for those changes. If a request left `pending.json`, `ensure()` refuses to retry it; inspect the controller and use `recoverPending(name, reviewedImageDigest)` for a confirmed deployment.

`create.secrets` can replace a binding declared in the bundle with a per-instance `secret://NAME` reference. Register the value with `setInstanceSecret(app, name, value)` before provisioning; the SDK never stores a plaintext value in its instance state.
`create.sidecarSecrets` does the same for a declared sidecar's secret bindings. `setSecret(name, value)` writes a controller-wide secret through the controller's secret API. `setInstanceSecret(app, name, value)` registers an instance-owned secret with the controller; other apps cannot use it, and a successful purge removes its encrypted revisions and upload receipt. Values never appear in arguments or SDK state.

If the same controller identity repeats an instance-secret registration with the same owner and value while its completed approval is still recent, the controller returns the completed receipt without creating another approval or secret revision. A different value or identity follows the normal approval flow.

`deleteSecret(name)` is available for explicit controller-wide secret cleanup. The controller refuses deletion while an active deployment, retained rollback revision, or operation still references the secret. Repeating deletion is safe. Instance-owned secrets are removed automatically with app purge.

After each completed operation, the SDK reads Hostlier's saved project, including its resolved image digest. Before another change it checks that project against the local snapshot, so an out-of-band deployment cannot be overwritten silently.

The bundle must use a container image (v1 `image` or v2 `source.image`) and inline configuration. Local build sources, file-backed managed configs, and project package hooks need a separate packaging step and are rejected. The controller can use installed server-side workflow packages referenced by the project.

Each mutation runs through the ordinary Hostlier retained approval flow. The service device needs an owner-approved all-access grant for dynamic application, secret, and purge operations. Accepted work continues if the backend exits. On restart, inspect the retained approval or operation and reconcile the SDK’s pending state before retrying.

The state directory must be private and durable. Keep it shared with the backend's instance identity mapping and backup process. On a failed or interrupted mutation, `pending.json` retains the proposed complete project and blocks another mutation until an operator reconciles Hostlier status with the saved state. The package does not infer that a disconnected operation failed or automatically submit it again.
`statusSnapshot(name)` returns the controller's status, route, and a `ready` boolean. `recoverPending(name, expectedImageDigest)` only clears a pending intent after a ready controller status matches the exact desired project and a separately reviewed image digest; it never resubmits work.

`purge(name)` submits a permanent delete action through the controller API; an enrolled service device with an owner-approved all-access grant can run scheduled expiry cleanup unattended. The SDK retires local state only after success. If the request is interrupted, `confirmPurge(name, operationId)` requires the controller's exact successful purge operation. Retired names cannot be reused in the same state store.

`recoverPendingPurge(name)` compares operations with IDs captured before the original request. It confirms one new successful purge with the app absent, or retries only when the controller accepted no new action and the unchanged app still exists. Ambiguous outcomes remain pending.

`expandSidecarVolume(name, "storage", "data", "10Gi")` submits a complete Hostlier update for a declared sidecar volume. It only accepts expansion and preserves the original bundle identity. Hostlier runtime support varies, and the declared size is not an enforced Docker or local-path Kubernetes storage quota.

For maintainers: after changing this package, run `node scripts/build-cloud-sdk.mjs` from the Hostlier repository to refresh the self-contained asset embedded in the Go CLI. This build uses the checkout's installed esbuild and the dependencies in this package’s lockfile; users installing the finished CLI need neither dependency.
