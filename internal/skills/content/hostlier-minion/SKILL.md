---
name: hostlier-minion
description: Build Minion apps (@fused/minion typed server actions with an optional React UI) that make integration calls through Fused Engine, and connect them to Fused on Hostlier. Deployment is the same as any other Hostlier app.
---

# Minion apps on Hostlier

A Minion app is an ordinary Hostlier app: build a container, then validate, plan and deploy it
like any other workload (see `hostlier skills show deploy`). What makes it different is its
**Fused connection**. Its server actions call Fused Engine over TLS gRPC, so the app makes
integration calls through Fused without holding provider credentials. Hostlier supplies the
Engine identity and execution token through the project's `fused` block.

Minion is a Hostlier product, shipped with the CLI and maintained in Hostlier’s
`packages/minion` directory. `hostlier minion` manages installation, preparation, local
development, and packaging. Engine-hosted Unified Apps use a different framework.

## Write the app

Use Node 24+, an ESM app (`"type": "module"`) and locked dependencies. Preserve the complete
Hostlier CLI release, including `minion/` beside the binary, and run `hostlier minion install`
in the app directory. Hostlier installs its exact bundled `@fused/minion` package; it does not
look up a floating framework version. Keep `.minion/framework/` with the app’s package.json
and lockfile so npm can reproduce the install. Do not ignore this framework archive when
sharing the app; it contains code, not credentials.

`app.ts` default-exports `buildMinion({ actions, authenticate? })`, with each action declared by
`defineAction` and `zod/mini` schemas:

```ts
import * as z from 'zod/mini';
import { buildMinion, defineAction } from '@fused/minion';

export default buildMinion({
  authenticate: request => sessions.authenticate(request), // Your app's own session check.
  actions: {
    createCustomer: defineAction({
      input: z.object({ name: z.string() }),
      output: z.object({ customerId: z.string() }),
      async execute({ input, user, signal }) {
        const customer = await services.crm.createCustomer(input); // Through Fused; see below.
        return { customerId: customer.id };
      },
    }),
  },
});
```

- Actions require authentication by default. `authenticate` returns
  `{ id, userRef?, serviceUserRefs? }` or `null`. Use `access: 'public'` only for deliberately
  anonymous actions, and `authorize(user, input)` for ownership checks.
- `signal` cancellation is cooperative; it cannot undo a completed provider call.

With `hostlier init --package minion --ui`, the server runs `load.ts` on each page request. The loader calls typed actions in-process and returns JSON data for the default React component in `ui.tsx`. The component receives `{ data, actions }`; after hydration, event handlers call the same methods over same-origin HTTP. Mark actions safe for page loads with `readOnly: true`. Authentication and authorization still apply. Only return data the current visitor may see: loader results are embedded in the HTML. Configure this with `"ui": { "entry": "ui.tsx", "load": "load.ts" }`. Omitting `ui.load` keeps the existing browser-only entry.

A browser-only UI (`ui.tsx`) is a normal React component rendered into `#app`. It imports only
`createActionClient` from `@fused/minion/client` plus a type-only app import
(`import type app from './app.js'`). Server modules, `node:` imports and `@grpc/*` are rejected
from the UI build. Requests go to `/api/actions/<name>` with same-origin cookies; never put the
Fused token in the client. Omit the UI for an API-only app.

Typecheck separately (`NodeNext`, `ES2022`, `react-jsx`, `strict`). Test actions with
`createMinionServer(app, { transport, origin })` from `@fused/minion/server` and a fake transport
implementing `execute(request)`, asserting the endpoint, input and user selector it receives.

## Run locally

In an empty app directory, run `hostlier init --package minion` to create the app and enroll it under `packages.minion` in `.hostlier`. Run `hostlier minion dev` to start it. Add `--ui` to the scaffold command to include a React form. Open the URL printed by `dev` to try it. Call its public greeting action with `curl --json '{"name":"Ada"}' http://127.0.0.1:4317/api/actions/greet`.

`hostlier minion dev` reads `minion.json`, installs the framework if missing, and serves the
app on loopback port 4317. Use `--port PORT` to choose a port, or `--port 0` to request an
available one. Local development needs no Hostlier controller, Docker, or deployment profile.
A minimal config is `{ "entry": "app.ts" }`; an optional `ui.entry` adds the browser UI.

The runner rebuilds and restarts on source changes, retains the previous working app after
a failed rebuild, and shuts down its child process on Ctrl-C. Author-defined authentication
still applies. Integration calls require Fused configuration and server-side credentials;
the child inherits those from the environment, never from browser code.

`hostlier minion prepare` prepares selected Fused contracts for the editor. `dev`, `prepare`,
and `build` default to `minion.json` and accept either a positional config path or `--file CONFIG`.

## Connect Fused for integration calls

### Declare the operations

Declare each integration call with `defineServices` / `defineOperation`, using the exact
`endpoint_name` and schemas of an already published Engine SDK/API app version:

```ts
import { defineOperation, defineServices } from '@fused/minion';

const services = defineServices({
  crm: {
    createCustomer: defineOperation({
      endpoint: 'EXACT_PUBLISHED_ENDPOINT',
      input: z.object({ name: z.string() }),
      output: z.object({ id: z.string() }),
    }),
  },
});
```

Call them only inside an action. Options include `selector` (`resourceId`, `endUserRef`,
`authType`, `authName`), `idempotencyKey` and `{ maxPages }` where the operation paginates.
Local declarations only validate and type calls. They do not register services, publish
capabilities or widen permissions; Engine enforces the selected app version's scope on every
call, and a new selection needs a new app version. Never guess endpoint names.

### Keep three credentials separate

1. **App session**: signs people into the Minion app (`authenticate`).
2. **Engine execution token**: authenticates the backend's calls to Engine. It is a Hostlier
   secret, not an Engine management key or Bridge installation credential.
3. **Provider connection**: stays in Fused's credential bucket. Route users with
   session-derived `userRef` / `serviceUserRefs` (keys match your `defineServices` keys).

### Bind the app on Hostlier

Add a `fused` block to the app's `.hostlier`:

```yaml
version: 2
name: customers
source:
  image: registry.example.com/customers:1.0.0
endpoint:
  exposure: public
  port: 8080
  route: customers.example.com
health:
  path: /healthz
fused:
  grpcUrl: https://engine.example.com:443   # Native gRPC endpoint; may differ from Engine's web origin.
  appId: 11111111-2222-3333-4444-555555555555  # Immutable Engine SDK/API app version UUID.
  tokenSecret: customers-engine-token        # Existing Hostlier secret name.
env:
  MINION_ORIGIN: https://customers.example.com
```

Hostlier validates the connection when planning and turns `fused` into
`FUSED_ENGINE_GRPC_URL`, `FUSED_APP_ID` and a `FUSED_APP_TOKEN` secret binding. Don't set those
variables yourself; conflicting producers are rejected. Each sidecar can declare its own
connection. The connection is part of the reviewed plan and of captured rollbacks.

Store the execution token before planning (owner authority required). Either issue a dedicated
one through Hostlier's Fused connection:

```sh
hostlier fused connect engine --engine-url https://ENGINE --server PROFILE
hostlier fused token engine --family-id SDK_FAMILY_UUID --secret-name customers-engine-token \
  --name UNIQUE_TOKEN_NAME --allow OPERATION[,OPERATION] --server PROFILE
```

or store an existing token with `hostlier secret set customers-engine-token` (hidden prompt).
The family UUID scopes the token and differs from the app version UUID in `appId`. Never paste
token values into chat, files, flags or images. Don't mint tokens, create buckets or select
extra operations just to get past a missing capability; report the gap. See
`hostlier skills show fused`.

### Calling behaviour and limits

- The transport reconnects after a lost channel but never resends `Execute`. A failed mutation
  may already have happened: check the outcome or use the provider's idempotency support before
  retrying.
- Supported: bounded JSON responses, unary `bridge:<alias>` calls and OAuth connection metadata.
  Not supported: streaming or binary results, `fused.db`, worker replay, durable webhooks,
  reverse Bridge serving and Engine-hosted Unified App attachments.

## Build and deploy

Run `hostlier deploy` from the initialized Minion directory. Deploy handles installation, building, and Fused provisioning automatically. Explicit `deploy --file` skips this preparation.

For manual packaging, `hostlier minion build` writes a Docker context in a new output directory.
Minion supplies the Node base image. Set `baseImage` to override it. When a deployment hostname
is supplied, Minion also writes the Hostlier project or the pending Fused consent descriptor.
Set a versioned `image` for registry deployments. Hostlier builds from local source on ordinary deploys.

Then deploy it like any other app, using either a registry image or
`source.container.context` for a local build (see `hostlier skills show deploy`):

```sh
hostlier validate --file build-1.0.0/artifact/.hostlier
hostlier plan --file build-1.0.0/artifact/.hostlier --server PROFILE
hostlier deploy --file build-1.0.0/artifact/.hostlier --server PROFILE
```

## Verify the Fused connection

Startup waits for Engine to admit the app, but `/healthz` does not prove integration calls
work, and later Engine outages leave it healthy. After deploying, call one real read-only action
and report separately: deployed, Engine admitted the app, and a provider call succeeded.

| Symptom | Check |
| --- | --- |
| gRPC connection or TLS failure | `grpcUrl` host and port, trusted certificate; Engine's web health URL doesn't prove gRPC access. |
| Admission rejected | `appId` version, matching execution token, token expiry or revocation. |
| Operation unavailable | Exact published endpoint and selected scope. |
| Provider auth or resource error | Connected account and the user/resource selectors derived from the session. |
| Action returns 401/403 | The app's own session and authorization, not the Engine token. |

Rollbacks need their captured Engine version and token to stay valid. Deleting an app does not
revoke a possibly shared Engine token; retire dedicated tokens through Fused deliberately. The
Minion package also ships deeper `minion-app`, `minion-fused` and `minion-deploy` skills.
