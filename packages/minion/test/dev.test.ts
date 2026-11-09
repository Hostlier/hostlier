import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { createRequire } from 'node:module';
import { setTimeout as delay } from 'node:timers/promises';
import { startMinionDev, type DevEvent } from '../src/dev.js';

/** Build a disposable ordinary app with installed framework dependencies, never a hosted identity. */
async function fixture(t: test.TestContext) {
  const root = await mkdtemp(resolve(tmpdir(), 'minion-dev-test-'));
  await symlink(dirname(dirname(createRequire(import.meta.url).resolve('zod/package.json'))), resolve(root, 'node_modules'));
  await writeFile(resolve(root, 'minion.json'), JSON.stringify({ entry: 'app.ts', ui: { entry: 'ui.ts', title: 'Development test' } }));
  await writeFile(resolve(root, 'ui.ts'), "document.querySelector('#app')!.textContent = 'Local Minion';");
  await writeFile(resolve(root, 'app.ts'), app('first'));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

/** Include one observable value and a credential-boundary assertion in the actual app response. */
function app(value: string): string {
  return `import * as z from 'zod/mini'; import {buildMinion,defineAction} from '@fused/minion'; export default buildMinion({actions:{read:defineAction({access:'public',input:z.object({}),output:z.object({value:z.string(),controlCredentialPresent:z.boolean()}),execute:()=>({value:${JSON.stringify(value)},controlCredentialPresent:!!process.env.FUSED_API_KEY||!!process.env.FUSED_LICENSE_KEY})})}});`;
}

/** Use the same HTTP action endpoint as a browser client without exposing any fixture transport hooks. */
async function read(url: string): Promise<{ output: { value: string; controlCredentialPresent: boolean } }> {
  const response = await fetch(url + '/api/actions/read', { method: 'POST', headers: { 'Content-Type': 'application/json', Origin: url }, body: '{}' });
  assert.equal(response.status, 200);
  return response.json() as Promise<{ output: { value: string; controlCredentialPresent: boolean } }>;
}

/** Wait for semantic dev events rather than relying on file-watcher timing. */
async function event(events: DevEvent[], type: DevEvent['type'], after = 0): Promise<void> {
  const until = Date.now() + 15000;
  while (!events.slice(after).some(item => item.type === type)) {
    if (Date.now() >= until) throw new Error(`Timed out waiting for ${type}`);
    await delay(30);
  }
}

test('dev serves on loopback, reloads source and config, retains a healthy app after build errors, and closes cleanly', async t => {
  const root = await fixture(t), events: DevEvent[] = [];
  const originalKey = process.env.FUSED_API_KEY;
  process.env.FUSED_API_KEY = 'synthetic-build-only-key';
  t.after(() => { if (originalKey === undefined) delete process.env.FUSED_API_KEY; else process.env.FUSED_API_KEY = originalKey; });
  const server = await startMinionDev(resolve(root, 'minion.json'), { port: 0, onEvent: item => events.push(item) });
  t.after(() => server.close());
  assert.match(server.url, /^http:\/\/127\.0\.0\.1:\d+$/);
  assert.deepEqual(await read(server.url), { output: { value: 'first', controlCredentialPresent: false } });
  assert.match(await (await fetch(server.url)).text(), /Development test/);
  const foreign = await fetch(server.url + '/api/actions/read', { method: 'POST', headers: { 'Content-Type': 'application/json', Origin: 'https://other.example.test' }, body: '{}' });
  assert.equal(foreign.status, 403);

  let before = events.length;
  await writeFile(resolve(root, 'app.ts'), app('second'));
  await event(events, 'rebuilt', before);
  assert.equal((await read(server.url)).output.value, 'second');

  before = events.length;
  await writeFile(resolve(root, 'app.ts'), 'export default { this is broken syntax');
  await event(events, 'build-error', before);
  assert.equal((await read(server.url)).output.value, 'second');

  before = events.length;
  await writeFile(resolve(root, 'app.ts'), "throw new Error('Startup failure');\n" + app('unhealthy'));
  await event(events, 'build-error', before);
  assert.equal((await read(server.url)).output.value, 'second');

  before = events.length;
  await writeFile(resolve(root, 'app.ts'), app('recovered'));
  await event(events, 'rebuilt', before);
  assert.equal((await read(server.url)).output.value, 'recovered');

  before = events.length;
  const config = JSON.parse(await readFile(resolve(root, 'minion.json'), 'utf8'));
  config.ui.title = 'Config changed';
  await writeFile(resolve(root, 'minion.json'), JSON.stringify(config));
  await event(events, 'rebuilt', before);
  assert.match(await (await fetch(server.url)).text(), /Config changed/);

  before = events.length;
  await writeFile(resolve(root, 'ui.ts'), "document.querySelector('#app')!.textContent = 'Changed browser bundle';");
  await event(events, 'rebuilt', before);
  assert.match(await (await fetch(server.url + '/ui.js')).text(), /Changed browser bundle/);

  await server.close();
  await assert.rejects(fetch(server.url + '/healthz'));
  assert.deepEqual((await readdir(resolve(root, '.minion'))).filter(name => name.startsWith('dev-')), []);
  assert.equal(events.at(-1)?.type, 'closed');
});

test('dev cancellation stops the listener and invalid initial builds leave no dev artifact', async t => {
  const root = await fixture(t), abort = new AbortController();
  const server = await startMinionDev(resolve(root, 'minion.json'), { port: 0, signal: abort.signal });
  t.after(() => server.close());
  // Cancel while a replacement is still starting; neither generation may outlive the dev session.
  await writeFile(resolve(root, 'app.ts'), 'await new Promise(accept => setTimeout(accept, 5000));\n' + app('slow startup'));
  await delay(300);
  abort.abort();
  await server.close();
  await assert.rejects(fetch(server.url + '/healthz'));
  await writeFile(resolve(root, 'app.ts'), 'this cannot compile');
  await assert.rejects(startMinionDev(resolve(root, 'minion.json'), { port: 0 }));
  assert.deepEqual((await readdir(resolve(root, '.minion'))).filter(name => name.startsWith('dev-')), []);
  await assert.rejects(startMinionDev(resolve(root, 'minion.json'), { port: -1 }), /port/);
});


test('dev CLI starts a minimal app and SIGTERM shuts down its listener and child', { timeout: 30000 }, async t => {
  const root = await fixture(t);
  const child = spawn(process.execPath, [fileURLToPath(new URL('../src/cli.js', import.meta.url)), 'dev', resolve(root, 'minion.json'), '--port', '0'], { stdio: ['ignore', 'pipe', 'pipe'] });
  t.after(() => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL'); });
  const exited = once(child, 'exit');
  let output = '', diagnostics = '';
  child.stdout.on('data', chunk => { output += chunk.toString(); });
  child.stderr.on('data', chunk => { diagnostics += chunk.toString(); });
  const deadline = Date.now() + 15000;
  while (!/Minion dev ready: (http:\/\/127\.0\.0\.1:\d+)/.test(output)) {
    assert.equal(child.exitCode, null, diagnostics);
    assert.ok(Date.now() < deadline, 'CLI did not become ready: ' + diagnostics);
    await delay(30);
  }
  const url = output.match(/Minion dev ready: (http:\/\/127\.0\.0\.1:\d+)/)![1]!;
  assert.equal((await read(url)).output.value, 'first');
  child.kill('SIGTERM');
  const [code, signal] = await exited;
  assert.equal(code, 0, diagnostics);
  assert.equal(signal, null);
  await assert.rejects(fetch(url + '/healthz'));
  assert.deepEqual((await readdir(resolve(root, '.minion'))).filter(name => name.startsWith('dev-')), []);
});
