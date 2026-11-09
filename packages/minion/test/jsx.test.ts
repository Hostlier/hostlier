import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import { setTimeout as delay } from 'node:timers/promises';
import { JSDOM } from 'jsdom';
import { buildMinionArtifact } from '../src/build.js';

const example = fileURLToPath(new URL('../../examples/customer-app/', import.meta.url));
const config = { name: 'jsx-app', image: 'example/jsx-app:1.0.0', baseImage: 'node:24.21.0-bookworm-slim', route: 'jsx.example.com', fused: { grpcUrl: 'https://engine.example.com', appId: '11111111-2222-3333-4444-555555555555', tokenSecret: 'jsx-engine-token' } };

/** Isolate authored fixtures and let their imports resolve through the installed package dependencies. */
async function temporary(t: test.TestContext): Promise<string> {
  const directory = await mkdtemp(resolve(tmpdir(), 'minion-jsx-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await symlink(dirname(dirname(createRequire(import.meta.url).resolve('react/package.json'))), resolve(directory, 'node_modules'));
  return directory;
}

/** React commits asynchronously; observe actual DOM state without relying on scheduler timings. */
async function until(condition: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (condition()) return;
    await delay(10);
  }
  assert.ok(condition(), 'React did not commit the expected UI state');
}

test('JSX compiles with the automatic runtime and hooks update rendered UI', async t => {
  const directory = await temporary(t);
  const entry = resolve(directory, 'ui.jsx');
  await writeFile(entry, `import { useState } from 'react'; import { createRoot } from 'react-dom/client';
function Counter() { const [count, setCount] = useState(0); return <button onClick={() => setCount(count + 1)}>Count {count}</button>; }
createRoot(document.getElementById('app')).render(<Counter />);`);
  const result = await buildMinionArtifact({ ...config, entry: resolve(example, 'app.ts'), outdir: resolve(directory, 'build'), ui: { entry } });
  const dom = new JSDOM(await readFile(resolve(result.directory, 'public/index.html'), 'utf8'), { url: 'https://jsx.example.com', runScripts: 'outside-only' });
  t.after(() => dom.window.close());
  dom.window.eval(await readFile(resolve(result.directory, 'public/ui.js'), 'utf8'));
  await until(() => dom.window.document.querySelector('button')?.textContent === 'Count 0');
  dom.window.document.querySelector('button')!.click();
  await until(() => dom.window.document.querySelector('button')?.textContent === 'Count 1');
  assert.equal((dom.window as unknown as Record<string, unknown>).React, undefined);
});

test('TSX customer UI submits typed actions and handles success and failure without server imports', async t => {
  const directory = await temporary(t);
  const result = await buildMinionArtifact({ ...config, entry: resolve(example, 'app.ts'), outdir: resolve(directory, 'build'), ui: { entry: resolve(example, 'ui.tsx') } });
  const html = await readFile(resolve(result.directory, 'public/index.html'), 'utf8');
  const bundle = await readFile(resolve(result.directory, 'public/ui.js'), 'utf8');
  assert.match(html, /ui.css/);
  assert.ok((await readFile(resolve(result.directory, 'public/ui.css'), 'utf8')).length > 0);
  assert.ok(!bundle.includes('EngineTransport') && !bundle.includes('FUSED_APP_TOKEN'));
  const dom = new JSDOM(html, { url: 'https://jsx.example.com', runScripts: 'outside-only' });
  t.after(() => dom.window.close());
  const calls: Array<{ url: string; body: string; authorization: string | null }> = [];
  let fail = false;
  dom.window.Headers = Headers as typeof dom.window.Headers;
  dom.window.fetch = (async (url, options) => {
    calls.push({ url: String(url), body: String(options?.body), authorization: new Headers(options?.headers).get('Authorization') });
    return Response.json(fail ? { error: { code: 'ACTION_FAILED' } } : { output: { customerId: 'customer-123' } }, { status: fail ? 500 : 200 });
  }) as typeof fetch;
  dom.window.eval(bundle);
  const document = dom.window.document;
  await until(() => !!document.querySelector('form'));
  // Use native setters to exercise React's normal input-event handling rather than mutating component state.
  const setValue = Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, 'value')!.set!;
  for (const [name, value] of [['token', 'app-session-token'], ['name', 'Ada']]) {
    const input = document.querySelector<HTMLInputElement>(`input[name="${name}"]`)!;
    setValue.call(input, value);
    input.dispatchEvent(new dom.window.Event('input', { bubbles: true }));
  }
  document.querySelector('form')!.dispatchEvent(new dom.window.Event('submit', { bubbles: true, cancelable: true }));
  await until(() => document.querySelector('[role="status"]')?.textContent === 'Created customer customer-123');
  assert.deepEqual(calls[0], { url: '/api/actions/createCustomer', body: '{"name":"Ada"}', authorization: 'Bearer app-session-token' });
  fail = true;
  document.querySelector('form')!.dispatchEvent(new dom.window.Event('submit', { bubbles: true, cancelable: true }));
  await until(() => document.querySelector('[role="status"]')?.textContent?.startsWith('Could not create customer.') === true);
  assert.equal(document.querySelector('button')?.disabled, false);
});
