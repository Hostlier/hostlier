# Action and JSX pattern

This small public greeting is a local framework example, not a default product or a live Fused integration. Replace its behavior with the user's task. Authenticated actions omit `access: 'public'` and require a verified app-session adapter.

`app.ts`:

```ts
import * as z from 'zod/mini';
import { buildMinion, defineAction } from '@fused/minion';

export default buildMinion({
  actions: {
    greet: defineAction({
      access: 'public',
      input: z.object({ name: z.string().check(z.minLength(1), z.maxLength(80)) }),
      output: z.object({ message: z.string() }),
      execute({ input }) {
        return { message: `Hello, ${input.name}` };
      },
    }),
  },
});
```

`ui.tsx`:

```tsx
import { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { createActionClient } from '@fused/minion/client';
import type app from './app.js';

const actions = createActionClient<typeof app>();

function App() {
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  async function submit() {
    setBusy(true);
    setMessage('');
    try {
      setMessage((await actions.greet({ name: name.trim() })).message);
    } catch {
      setMessage('Unable to load a greeting. Please try again.');
    } finally {
      setBusy(false);
    }
  }
  return <main>
    <h1>Greeting</h1>
    <label>Name <input value={name} maxLength={80}
      onChange={event => setName(event.currentTarget.value)} /></label>
    <button disabled={busy || !name.trim()} onClick={submit}>
      {busy ? 'Loading…' : 'Say hello'}
    </button>
    <p role="status">{message}</p>
  </main>;
}

const root = document.getElementById('app');
if (!root) throw new Error('Missing Minion mount point');
createRoot(root).render(<App />);
```

Use an ESM application (`"type": "module"`). A typical TypeScript setup uses `module` and `moduleResolution` set to `NodeNext`, `target: ES2022`, `jsx: react-jsx`, `strict: true`, and `noEmit: true`. Install the app's own TypeScript, Node, React and React DOM type dependencies. Typecheck separately: bundling with esbuild does not typecheck the app.

## Run the local app

Install Node 24+ and the complete Hostlier CLI release, including its adjacent `minion/` package directory. Save this minion.json beside the example:

```json
{ "entry": "app.ts", "ui": { "entry": "ui.tsx", "title": "Greeting" } }
```

```sh
hostlier minion install
hostlier minion dev
```

This greeting requires no Engine or controller credentials. The dev runner serves loopback port 4317, rebuilds on source changes, and closes on Ctrl-C. Omit `ui` for an API-only app; use `--port 0` for an available port. Retain the installed `.minion/framework/` archive with package.json and the npm lockfile. Integration apps also need selected Fused configuration and authorized runtime credentials; see the minion-fused skill.

## Test without Engine credentials

`@fused/minion/server` exports `createMinionServer(app, options)`. Supply an exact origin and a fake transport implementing `execute(request): Promise<unknown>`. Listening is explicit; import alone opens no port. Bind a disposable listener on `127.0.0.1` and close it after each test.

The action endpoint is `POST /api/actions/greet`, with JSON `{ "name": "Ada" }`; success wraps its value in `{ "output": { "message": "Hello, Ada" } }`. The typed browser client unwraps `output`. Invalid inputs return an action error; dependent provider failures are redacted. Tests should not rely on private provider exception text reaching the browser.

When testing connected operations, assert the fake transport received the expected endpoint, validated input and authenticated user selector. Tests that require local listeners need permission to bind sockets in a restricted environment; a denied bind is not a failing application assertion.

## Optional UI and framework limits

A UI build uses `ui: { entry: "ui.tsx", title: "Your app" }` in `minion.json`; omit `ui` for headless use. Config entry/output paths are relative to that file. The generated shell already loads the browser JavaScript and CSS. Use a root hostname: the current artifact generator does not implement arbitrary SPA deep-link routing, path-prefix hosting or custom route ports.

Only the browser-safe client belongs in the UI dependency graph. A normal import of the backend, `@fused/minion/grpc`, Node APIs or the Minion server fails the browser build. A type-only app import keeps input and output inference without shipping server code.

For a real data app, preserve provider timestamps, distinguish fresh/cache/demo data, and implement the requested domain. The packaged stocks example uses IBM daily closes, not live tick prices; its existing Engine app ID and VM addresses are deployment-specific examples, not defaults for other users.
