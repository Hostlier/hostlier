---
name: minion-app
description: Create or modify Minion apps using @fused/minion typed server actions and optional JSX/React UIs. Use for Minion app logic, browser action clients, app authentication and local testing; Engine-hosted Unified Apps use a different framework.
---

# Build a Minion app

Minion is a Hostlier product maintained in Hostlier’s `packages/minion` directory. It runs application code outside Fused Engine. Its backend exposes typed actions; optional browser components call those actions. Fused integrations execute through the backend's authenticated gRPC connection. The Unified App-like coding style does not imply Engine-hosted execution or identical APIs.

## Establish the app

Start from the user's requested outcome and existing app. Examples demonstrate APIs; their business domain is not the default requirement. Use JSX/TSX and ordinary React components when a UI is wanted. Omit the UI for an API-only app.

Locate `@fused/minion` in the app dependencies or `packages/minion` in a Hostlier checkout. Read its exports and README when an API or version is uncertain. Use Node 24+ and `hostlier minion install` to install the framework bundled with the CLI; keep the release’s `minion/` directory beside the binary. Retain the app’s `.minion/framework/` archive, package.json and lockfile for reproducible installation. Add React type declarations and TypeScript to the app's dev dependencies when needed for TSX checking; dependencies of Minion do not supply an application's dev dependencies.

Read [authoring.md](references/authoring.md) for the concrete action and JSX pattern. Choose action names, input/output schemas and UI states for the actual task, then implement them together.

- Export `buildMinion({ actions, authenticate? })` as the backend's default export. Declare individual actions with `defineAction` and `zod/mini` schemas.
- Actions require authentication by default. Supply an adapter that verifies the application's session and returns a principal or `null`. Use `access: 'public'` only for deliberately anonymous capabilities; read-only behavior alone does not make sensitive data public.
- Add `authorize(user, input)` for app-level ownership or permission checks. Identity cannot be trusted merely because it appeared in an input field or request header.
- The UI imports `createActionClient` from `@fused/minion/client`, plus a **type-only** backend import. Render with `createRoot` into `#app`. CSS imports and React's automatic JSX transform are supported.
- Keep provider calls and Engine credentials on the server. Browser action requests use same-origin cookies; a headers callback can carry a separate application session token, never the Fused execution token.
- Use `signal` for cancellable work and handle loading, failure, empty and successful UI states. Cancellation cannot undo completed provider mutations.

For new integrations, import `services` from `@fused/minion`, select provider services/versions/operations once in `fused.services`, and let `hostlier minion prepare` / `hostlier minion build` generate types and bindings. Do not handwrite provider schemas or make app code import generated files. For real integrations, use `$minion-fused` when available; it covers exact published operation contracts and connected-user routing. For container packaging or an authorized deployment, use `$minion-deploy` when available. Do not deploy merely because an app was created.

## Develop locally

Create `minion.json` with `{ "entry": "app.ts" }`; add `"ui": { "entry": "ui.tsx" }` for a UI. Run `hostlier minion dev` to install the framework if missing and serve the app on loopback port 4317. `--port 0` chooses an available port. No controller, Docker, image, or route is needed. The runner rebuilds on source changes, keeps the last working build after errors, and stops on Ctrl-C. Keep authored app authentication enabled.

Native integrations additionally need their selected Fused configuration, `FUSED_API_KEY` for contract preparation, and separate `FUSED_ENGINE_GRPC_URL`, `FUSED_APP_ID`, and `FUSED_APP_TOKEN` for execution. Use protected environment values, never config or UI literals. The child does not receive the build management key. Minion’s existing generator still owns the internal typed service bindings.

With `hostlier init --package minion --ui`, the server runs `load.ts` on each page request. The loader calls typed actions in-process and returns JSON data for the default React component in `ui.tsx`. The component receives `{ data, actions }`; after hydration, event handlers call the same methods over same-origin HTTP. Mark actions safe for page loads with `readOnly: true`. Authentication and authorization still apply. Only return data the current visitor may see: loader results are embedded in the HTML. Configure this with `"ui": { "entry": "ui.tsx", "load": "load.ts" }`. Omitting `ui.load` keeps the existing browser-only entry.

## Verify the requested behavior

Run the app's typecheck and relevant tests. Exercise actions through `createMinionServer` with an injected `Transport` for isolated tests; an integration call outside an active action intentionally fails. Verify meaningful behavior such as schema rejection, authentication, ownership, user isolation or the requested data transformation.

For a UI, build the browser bundle and exercise its real action flow when a browser is available. Do not treat a screenshot of static placeholders as integration verification. Label fixture data and distinguish fixture success from a successful live Fused call. Use the existing example tests in the installed framework or checkout for HTTP/gRPC fixture patterns rather than creating real provider effects to test a button.

The generated build refuses an existing output directory; choose a new build identifier instead of deleting another build blindly. A local build does not publish a Fused app, connect accounts or deploy anything.

Report the working entry points, checks performed, and any missing runtime identity or credentials that prevent live verification. Do not claim Engine database APIs, worker replay, durable webhooks or reverse Bridge hosting: these are not provided by this Minion runtime.
