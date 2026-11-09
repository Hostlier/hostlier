# Orbit — Minion chat for the Harnest agent

A public React playground using typed `@fused/minion` actions. Open the app and
start chatting; no sign-in or playground access token is required. Anyone with
the URL can use it. The backend authenticates to the Harnest agent at
`https://agent.example.com/responses`; its agent credential stays on
the server and never enters the browser bundle.

## Deploy from your laptop

Requires the updated Hostlier CLI, Docker running locally, and an authorized
connection to your controller. Build the app, copy the example manifest, and
set your own chat and agent URLs before deploying.

```sh
cd examples/ollama-chat
cp .hostlier.example .hostlier
# Set endpoint.route, MINION_ORIGIN and AGENT_URL in .hostlier.
hostlier validate
hostlier deploy
```

The example expects an agent credential stored on your controller as
`ollama-agent-access`. Its reference contains no secret value:

```yaml
secrets:
  AGENT_ACCESS_TOKEN: secret://ollama-agent-access
```

No `PLAYGROUND_ACCESS_TOKEN` is needed. Hostlier builds the declared Docker
context, uploads the image, and reviews and applies the deployment plan.
`hostlier validate` is offline. No staging script, registry, SSH command or
socket flag is needed for deployment.

After deployment, open [Orbit](https://chat.example.com) and try
“Use calculate to multiply 17 by 23.”

```sh
hostlier status ollama-chat
hostlier logs ollama-chat --tail 30
```

The app requests 0.25 CPU and 192 MiB RAM, runs as a non-root Node user, and has
no volumes. Hostlier manages routing, HTTPS, readiness, restart and cleanup.
The agent remains a separate workload.

## Build and test

Requires Node 24+. This example uses the Minion framework in the same Hostlier repository.

```sh
npm ci --prefix ../../packages/minion
npm --prefix ../../packages/minion run build
npm ci
npm run typecheck
npm test
npm run build -- dist/example
```

The example manifest uses `dist/example`. For another build directory, update
`source.container.context` in your ignored `.hostlier` file. Keep private URLs
and credentials out of `.hostlier.example` and source files.
Each build contains a server bundle, browser files and a Dockerfile pinned to
Node 24.21.0's image digest. Runtime credentials are not part of the build context.

Tests exercise anonymous actions through a real HTTP server, origin and input
validation, upstream token separation, error redaction, tool results, bounded
responses, cancellation and conversation context. The upstream provider is mocked.

## Behavior

Replies appear when the full response is available. Only one inference runs at
a time. Stop cancels the HTTP request but cannot guarantee immediate cancellation
of remote inference. Failures do not trigger automatic retries or expose private
upstream diagnostics.

Conversation history stays in the browser tab. The last six messages, each up
to 2,000 characters, are supplied as bounded context. Reloading clears history.
