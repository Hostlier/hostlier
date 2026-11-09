# Minion

Minion is Hostlier’s product for teams whose agents build deterministic apps with typed server actions, an optional browser UI, and third-party integrations. `@fused/minion` runs outside Fused Engine; its framework and shared runtime are maintained in this Hostlier repository. The backend calls Engine’s existing `EngineService` over TLS gRPC. Hostlier manages local development, packaging, deployment, and the deployed app’s execution-token secret.

## Install and run locally

Install Node.js 24+ and the complete Hostlier CLI release, preserving the `minion/` directory beside its binary. In your app directory:

```sh
hostlier minion install
hostlier minion dev
```

`dev` reads `minion.json` and automatically installs the bundled framework when it is missing. For an app without integrations, the minimum config is:

```json
{ "entry": "app.ts" }
```

Add `"ui": { "entry": "ui.tsx" }` to serve a browser UI. Development needs no Docker, Hostlier controller, deployment domain, or image configuration. It listens on loopback port 4317; `hostlier minion dev --port 0` chooses an available port and prints its URL. Source changes rebuild and restart the app while preserving that URL. A failed rebuild leaves the last working app running; Ctrl-C stops the runner and its child. Authored authentication and authorization still apply.

The installed package is recorded in the app’s package.json and lockfile. Retain `.minion/framework/` with them: it holds the exact framework archive needed to reproduce installation. Add ignore-rule exceptions if your app ignores `.minion/` or `*.tgz`; the framework archive contains code, not credentials.

For native Fused integrations, configure `fused.services` as described below. Preparation reads selected contracts using `FUSED_API_KEY`; execution separately requires `FUSED_ENGINE_GRPC_URL`, the immutable `FUSED_APP_ID`, and its `FUSED_APP_TOKEN`. Supply those through your protected local environment, plus any credentials required by your app’s own session adapter. No secret values belong in minion.json. The dev runner strips `FUSED_API_KEY` and `FUSED_LICENSE_KEY` from the application child.

```sh
# With authorized catalog and runtime credentials already in the environment:
hostlier minion prepare
hostlier minion dev --port 4317
```

`prepare` generates `.minion/services.d.ts` and contract metadata using Minion’s existing generator. It does not request or install a Fused-generated SDK package. Build and dev repeat preparation for native Fused selections. Commands accept a positional config path or `--file CONFIG` instead of the default `minion.json`.

## Author actions

```ts
import * as z from 'zod/mini';
import { buildMinion, defineAction, services } from '@fused/minion';

export default buildMinion({
  // Resolve your own authenticated application session here.
  authenticate: request => sessions.authenticate(request),
  actions: {
    createCustomer: defineAction({
      input: z.object({ name: z.string() }),
      output: z.object({ customerId: z.string() }),
      async execute({ input, user, signal }) {
        const customer = await services.crm.createCustomer(input);
        return { customerId: customer.id };
      },
    }),
  },
});
```

`sessions` above represents the application's own authentication adapter. Return `{ id, userRef?, serviceUserRefs? }` or `null`. `userRef` is a stable connected-user reference that Engine resolves against the published app's credential bucket. `serviceUserRefs` can override it for a particular service. Never derive these references directly from unverified browser parameters.

Actions require authentication by default. An intentionally anonymous action must declare `access: 'public'`. An action can additionally supply `authorize(user, input)` for application-level access checks. Integration bindings and execution contexts use `AsyncLocalStorage`; simultaneous requests do not share their user references. Service calls reject a per-call reference that conflicts with the authenticated binding.

Minion generates the typed `services` methods from the exact service versions and operations selected in its build configuration. Application code does not import generated files or repeat provider schemas, endpoint bindings, or SDK initialization. The generated startup installs only selected operations before loading the app. Calls require an active action and inherit its authenticated user and cancellation signal. Engine validates provider contracts and enforces the immutable app's selected capabilities on every call. Action input/output schemas still validate the application's public boundary. No provider credentials or provider base URLs are exposed by this client.

The older `defineService` / `defineServices` / `defineOperation` helpers remain available for existing applications; new integrations should use the native `services` export.

## Attach an optional UI

With `hostlier init --package minion --ui`, the server runs `load.ts` on each page request. The loader calls typed actions in-process and returns JSON data for the default React component in `ui.tsx`. The component receives `{ data, actions }`; after hydration, event handlers call the same methods over same-origin HTTP. Mark actions safe for page loads with `readOnly: true`. Authentication and authorization still apply. Only return data the current visitor may see: loader results are embedded in the HTML. Configure this with `"ui": { "entry": "ui.tsx", "load": "load.ts" }`. Omitting `ui.load` keeps the existing browser-only entry.

In a browser-only entry, import only the client module and a type-only reference to the server:

```ts
import { createActionClient } from '@fused/minion/client';
import type app from './app.js';

const actions = createActionClient<typeof app>();
const customer = await actions.createCustomer({ name: 'Ada' });
```

The client sends JSON POST requests to `/api/actions/<name>` with same-origin cookies. A `headers` callback supports your app's bearer-session mechanism. Never put the Fused execution token in this callback. Action failures expose a stable `ActionError.code`, without provider errors or stack traces. Input schemas determine browser argument types; output schemas determine result types.

UI entries support `.jsx` and `.tsx` with React’s automatic JSX transform, so JSX does not require a global `React` import. React and React DOM are included. Use ordinary components, hooks, and `createRoot` to render into `#app`; the customer example is a typed React component. Plain JavaScript/TypeScript entries still work, and omitting `ui` still produces an API-only app. CSS imports are bundled. The build rejects server imports in the browser graph. The generated shell has a self-origin CSP, and action requests reject foreign browser origins. The public hostname is explicit configuration, never inferred from forwarded headers.

See [the customer example](examples/customer-app/app.ts), [its UI](examples/customer-app/ui.tsx), and [build configuration](examples/customer-app/minion.json). It uses a separate single-operator app access token. Replace that adapter with your session system for a multi-user application. Its illustrative `createCustomer` operation must match your own published Engine selection.

## Build and deploy with Hostlier

Use `hostlier minion install` for app installation. Framework contributors can build the same release payload from the Hostlier repository root:

```sh
./scripts/package-minion.sh dist/minion
```

The packaging script installs locked dependencies, builds Minion and its shared runtime, and stages the runtime as ordinary package files. This avoids parent-relative archive paths from development dependency links. Direct `npm pack`/`npm publish` from the package source is rejected; publish a built tarball only through your authorized release workflow. Minion does not depend on the Hosting SDK.

Create `minion.json`:

```json
{
  "entry": "app.ts",
  "ui": { "entry": "ui.tsx", "title": "Customers" },
  "outdir": "build-1.0.0",
  "name": "customers",
  "image": "registry.example.com/customers:1.0.0",
  "baseImage": "node:24.21.0-bookworm-slim",
  "route": "customers.example.com",
  "fused": {
    "engineUrl": "https://engine.example.com",
    "grpcUrl": "https://grpc.example.com",
    "version": "1.0.0",
    "bucket": "demo",
    "tokenSecret": "customers-engine-token-v1",
    "services": {
      "crm": { "service": "@example/crm", "version": "2026-01-01", "operations": ["createCustomer"] }
    }
  },
  "secrets": { "CUSTOMER_APP_ACCESS_TOKEN": "customers-access-token" }
}
```

`outdir` is optional and defaults to `build` beside `minion.json`. It holds the packaged server, UI assets, and Docker context under `build/artifact`; it is not a deployment destination. Set a fresh `outdir` for subsequent builds because existing build output is preserved. Paths resolve relative to the config file, even when the command runs from another directory.

Replace the illustrative service identity, exact provider version, operation and deployment addresses with your selected Registry contract. The key `crm` becomes `services.crm` and the key used by `serviceUserRefs`. The Engine HTTP origin and gRPC endpoint may differ. Minion supplies a pinned Node base image. Set `baseImage` to override it. Building without a route produces a portable Docker context. Supplying a route also generates Hostlier deployment metadata. A local container build needs no registry tag. Omit `ui` for an API-only app; the server still exposes actions and health. Keep the catalog-reading credential in the build process's `FUSED_API_KEY` environment, never in `minion.json` or the container.

```sh
# Generate editor types from the selected contracts (build also does this automatically).
hostlier minion prepare minion.json
hostlier minion build minion.json
# Output: build-1.0.0/artifact, including fused-access.json and hostlier-pending.json.
hostlier fused provision customers --file build-1.0.0/artifact/hostlier-pending.json
# Complete Fused consent; Hostlier stores the runtime token and writes .hostlier.
docker build -t registry.example.com/customers:1.0.0 build-1.0.0/artifact
docker push registry.example.com/customers:1.0.0

# Hidden prompts store credentials; the config contains reference names only.
hostlier secret set customers-access-token
hostlier validate --file build-1.0.0/artifact/.hostlier
hostlier plan --file build-1.0.0/artifact/.hostlier
hostlier deploy --file build-1.0.0/artifact/.hostlier
```

Use a Hostlier build containing the native `fused` declaration. Preparation and build read selected contracts from Engine, generate the SDK bindings and types, and typecheck the app. They do not execute the app, publish an Engine app, issue credentials, push an image, or deploy. It refuses an existing output directory. The image installs locked dependencies, runs as the Node non-root user, and accepts SIGTERM for bounded graceful shutdown. Runtime secrets are injected after image creation.

Hostlier lowers `fused` into `FUSED_ENGINE_GRPC_URL`, `FUSED_APP_ID`, and the `FUSED_APP_TOKEN` secret binding. It rejects conflicting literal, dependency, workflow, or secret producers. Each sidecar can have its own connection. Connection identity participates in ordinary review fingerprints and captured rollback specifications.

The generated bindings are internal Minion packaging artifacts, not a second package application authors install or initialize. Consent provisions an ordinary Fused API app using the generated selection; deployment supplies the matching immutable version and execution token. The legacy explicit `appId` mode remains available for an existing Engine SDK/API app and execution-token secret. Deleting a Hostlier workload does not revoke a potentially shared Engine token. Deliberately revoke a dedicated token through Fused when retiring its app.

## Native Fused contracts and consent

`fused.services` is the single selection for generated methods, TypeScript contracts and the consent manifest. Each alias pins a real service, provider version and exact operation list. No provider input/output schemas are handwritten in application code. `hostlier minion prepare minion.json` writes `.minion/services.d.ts` and `.minion/fused-contract.json` beside the configuration; `hostlier minion build` performs the same preparation automatically and checks the app against those generated types. Include `.minion/services.d.ts` in your editor's TypeScript project when its `include` rules omit hidden directories. The app continues importing only `services` from `@fused/minion`.

`fused.bucket` optionally supplies the bucket name or canonical UUID already selected in Minion. It is included in the hashed manifest and displayed read-only on Fused consent. Fused validates current bucket-use access and rejects substitutions or extra buckets. Omit it to let the owner choose through Fused’s searchable credential picker. Changing a caller-selected bucket requires fresh consent.

Build emits `fused-access.json` and `hostlier-pending.json` from that same selection. The pending artifact deliberately has no deployable placeholder identity. The source may use only the selected typed methods; undeclared operations do not exist in the runtime facade. Engine remains the authority for execution scope and provider credentials.

```sh
hostlier fused provision customers --file build-1.0.0/artifact/hostlier-pending.json
# Review and approve the actual requested app, operations and credential buckets.
# Hostlier stores runtime credentials and exports artifact/.hostlier.
hostlier plan --file build-1.0.0/artifact/.hostlier
hostlier deploy --file build-1.0.0/artifact/.hostlier
```

The manifest uses the existing API app model (`kind: sdk`, `generate: false` internally), not a new Engine app kind. PKCE and the compiler-manifest digest bind the ceremony. Credentials remain in the controller and runtime secret store; application code and browser bundles receive no management credential. On Engine versions supporting managed Minion deployment, the controller-only OAuth scope is `app.api.deploy`, bound to the specific Fused app family and its approved operation/bucket ceiling. It is not a workspace-wide app-management grant. The controller uses that grant to publish subsequent immutable versions and stores a separate execution credential for each deployed release; the Minion container receives only execution authority.

`hostlier fused status` includes approved bucket IDs and the default. Actions may pass `{ bucketId: approvedBucketId }` when selecting among approved buckets. Engine enforces finite scope and live authority; application code must authorize choices influenced by its users.

If consent succeeded but exporting `.hostlier` failed, `hostlier fused bind customers --file .../hostlier-pending.json` recovers the stored identity without repeating consent. Existing deployment files are not overwritten. Use one stable connection name such as `customers` across releases. A code-only build with an unchanged manifest can bind the existing identity. For a new app version, increment `fused.version`, choose a new `tokenSecret`, and run `fused provision customers` against its new pending artifact. The scoped controller grant permits versions within the previously approved operation/bucket ceiling; broader requirements need new consent. Provider versions stay explicitly pinned. Retain every release's immutable Engine version and execution-secret binding for rollback.

`defineService` / `defineServices` / `defineOperation` declarations and explicit `appId` builds remain a compatibility path. For native builds, use `fused.services` rather than a second declaration list in source. Endpoint IDs must currently be unique across different selected providers because the gRPC protocol dispatches by endpoint name. The manifest is bounded to 64 services, 1000 operations and 64 KiB.

See [the native readiness example](examples/native-readiness/README.md) for a minimal synthetic read using the native import and one configuration selection. The older customer and stock examples demonstrate the compatibility API.

## Runtime API and behavior

`@fused/minion/server` exports `createMinionServer(app, options)`. Options supply `transport`, an exact `origin`, an optional `uiDirectory`, and bounded body, concurrency, and action-timeout settings. Listeners are not started on import. `onError` is an optional private server diagnostic callback. `/healthz` confirms the local process is ready; generated startup waits for its initial Engine handshake. Later Engine outages fail dependent actions without hiding the UI or replaying side effects.

`@fused/minion/grpc` re-exports `EngineTransport` from `@fused/runtime`. It provides `connect`, `execute`, `startConnectSession`, `getConnection`, `listConnectionResources`, and `close`. OAuth connection/resource metadata uses the wire's snake_case fields; `startConnectSession` returns camelCase URL/session fields. Calls use one shared channel, exact app identity, per-RPC authentication, deadlines, cancellation, response limits, and idempotency hashes. The transport repeats cache admission after channel loss, but never automatically resends an `Execute`. Engine retains ownership of provider retries, quotas, pagination, and credentials. gRPC requires TLS except for explicit loopback IPs used in local tests.

Actions receive an abort signal on timeout/disconnect. Cancellation is cooperative; it does not undo a completed provider effect or interrupt synchronous JavaScript. Keep blocking CPU work outside the web process. Timed-out work continues occupying its concurrency slot until it settles.

This release supports bounded JSON provider responses, selected unary Bridge aliases (`bridge:<alias>`), and the existing provider OAuth metadata RPCs. Streaming/binary responses fail explicitly. It does not claim Engine-worker snapshot/replay or `fused.db` semantics. Hosted Unified App attachments currently use Fused's separate HTTP execution API and are not implemented by this gRPC-only runtime. Durable webhook subscriptions and exposing mini app actions back to Engine over Bridge's reverse stream are separate extensions.

## Validation

From a fresh Hostlier repository checkout, install Minion and its pinned Engine-owned runtime archive:

```sh
npm ci --prefix packages/minion
npm --prefix packages/minion test
```

`npm test` compiles the public types and runs real loopback gRPC/HTTP tests, including concurrent user isolation, authentication/authorization, origin checks, bounded responses, cancellation, non-replay of uncertain calls, browser/server bundling, headless builds, and execution of the generated server artifact. The tests use disposable fixtures, never live credentials. Hostlier tests exercise native configuration lowering, review stability, conflicting producers, and sidecar isolation.

The protocol now lives in `@fused/runtime` and is exported as `@fused/runtime/protocol`. Minion no longer owns a separate proto or gRPC implementation. Engine builds the protocol directly from its canonical schema. Hostlier keeps only a compiled distribution snapshot in `packages/vendor/fused-runtime`; source development and runtime tests live in Engine's `runtime/sdk`. See the vendor README to refresh it. Copied declarations alone do not enable unsupported RPCs.


### IBM stock prices sample

`examples/stocks-app` is a React/JSX dashboard with a typed public `getDashboard` action. It calls Alpha Vantage's IBM demo through Fused's authenticated TLS gRPC runtime. The latest two daily sessions supply the current close and daily change; the UI shows the provider's trading date and up to 100 actual daily closes. It is end-of-day demo data, with a 60-second cache, not a real-time market feed. No provider credential or Engine execution token is included in the browser bundle.

The `fused.yaml` selects the service operation; `minion.json` binds an immutable Engine app version and a Hostlier secret reference. Replace the example deployment identities with your own. Build with `hostlier minion build packages/minion/examples/stocks-app/minion.json` and deploy its generated `.hostlier` through a [scoped Hostlier connection](../../docs/guides/controller-access.md).

## Coding-agent skills

The package includes three skills under `skills/`:

- `minion-app`: typed actions, app authentication and optional JSX/React UI.
- `minion-fused`: published operation contracts, connected users and gRPC identity.
- `minion-deploy`: container packaging, Hostlier secrets, deployment and verification.

Copy the skill folders into your agent's skill directory. For Codex, use
`${CODEX_HOME:-$HOME/.codex}/skills/`. Invoke `$minion-app`, `$minion-fused` or
`$minion-deploy`, or let the agent select the relevant skill automatically.
