import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile, symlink, readdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createRequire } from 'node:module';
import { dirname } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import * as grpc from '@grpc/grpc-js';
import { createServer } from 'node:net';
import { buildMinionArtifact, hostlierProject } from '../src/build.js';
import { engineDefinition } from '../src/grpc.js';

const packageRoot = fileURLToPath(new URL('../../', import.meta.url));
const sourceRoot = packageRoot;
const common = { name: 'mini', image: 'example/mini:1.0.0', baseImage: 'node:24.21.0-bookworm-slim', route: 'mini.example.com', fused: { grpcUrl: 'https://engine.example:443', appId: '11111111-2222-3333-4444-555555555555', tokenSecret: 'mini-token' } };

/** Reserve an ephemeral listener address for the built artifact smoke test. */
async function freePort(): Promise<number> {
  const server = createServer(); server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const port = (server.address() as { port: number }).port;
  await new Promise<void>(resolve => server.close(() => resolve())); return port;
}

/** All fixture files and processes belong to one disposable test directory. */
async function temporary(t: test.TestContext) {
  const directory = await mkdtemp(resolve(tmpdir(), 'fused-mini-test-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await symlink(dirname(dirname(createRequire(import.meta.url).resolve('zod/package.json'))), resolve(directory, 'node_modules'));
  return directory;
}

test('build creates a runnable server/UI artifact without evaluating app code or exposing server secrets', async t => {
  const directory = await temporary(t);
  const entry = resolve(directory, 'app.ts');
  const marker = resolve(directory, 'evaluated');
  await writeFile(entry, `import {writeFileSync} from 'node:fs'; import * as z from 'zod/mini'; import {buildMinion,defineAction,defineServices,defineOperation} from '@fused/minion'; writeFileSync(${JSON.stringify(marker)},'runtime'); const services=defineServices({crm:{create:defineOperation({endpoint:'createCustomer',input:z.object({name:z.string()}),output:z.object({id:z.string()})})}}); export default buildMinion({actions:{create:defineAction({access:'public',input:z.object({name:z.string()}),output:z.object({id:z.string()}),execute:({input})=>services.crm.create(input)})}});`);
  await writeFile(resolve(directory, 'ui.ts'), `import {createActionClient} from '@fused/minion/client'; const actions=createActionClient(); document.querySelector('#app').textContent='Mini app';`);
  const result = await buildMinionArtifact({ ...common, entry, outdir: resolve(directory, 'build'), ui: { entry: resolve(directory, 'ui.ts'), title: '<Customers>' } });
  assert.ok(!(await readdir(directory)).includes('evaluated'));
  const browser = await readFile(resolve(result.directory, 'public/ui.js'), 'utf8');
  assert.ok(!browser.includes('FUSED_APP_TOKEN') && !browser.includes('EngineTransport') && !browser.includes('createCustomer'));
  assert.match(await readFile(resolve(result.directory, 'public/index.html'), 'utf8'), /&lt;Customers&gt;/);
  assert.match(await readFile(resolve(result.directory, 'Dockerfile'), 'utf8'), /npm ci --omit=dev --ignore-scripts/);
  assert.equal(JSON.parse(await readFile(resolve(result.directory, '.hostlier'), 'utf8')).fused.tokenSecret, 'mini-token');
  await assert.rejects(buildMinionArtifact({ ...common, entry, outdir: resolve(directory, 'build') }));

  const server = new grpc.Server();
  server.addService((grpc.loadPackageDefinition(engineDefinition).engine as any).v1.EngineService.service, {
    Connect(_call: any, done: any) { done(null, {}); }, Disconnect(_call: any, done: any) { done(null, {}); },
    Execute(call: any) { assert.equal(call.request.endpoint_name, 'createCustomer'); assert.equal(call.metadata.get('x-api-key')[0], 'fixture-runtime-token'); call.write({ result: Buffer.from('{"id":"created-through-grpc"}'), status_code: 200 }); call.end(); },
  });
  const grpcPort = await new Promise<number>((resolve, reject) => server.bindAsync('127.0.0.1:0', grpc.ServerCredentials.createInsecure(), (error, port) => error ? reject(error) : resolve(port)));
  t.after(() => server.forceShutdown());
  const port = await freePort();
  const child = spawn(process.execPath, [resolve(result.directory, 'server.mjs')], { env: { ...process.env, PORT: String(port), MINION_ORIGIN: `http://127.0.0.1:${port}`, FUSED_ENGINE_GRPC_URL: `http://127.0.0.1:${grpcPort}`, FUSED_APP_ID: common.fused.appId, FUSED_APP_TOKEN: 'fixture-runtime-token' }, stdio: ['ignore', 'pipe', 'pipe'] });
  let stderr = ''; child.stderr.on('data', data => { stderr += data; });
  t.after(async () => { if (child.exitCode === null) { child.kill('SIGKILL'); await once(child, 'exit').catch(() => {}); } });
  const base = `http://127.0.0.1:${port}`;
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) { try { ready = (await fetch(base + '/healthz')).ok; } catch {} if (ready || child.exitCode !== null) break; await delay(30); }
  assert.ok(ready, stderr);
  const response = await fetch(base + '/api/actions/create', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"name":"Ada"}' });
  assert.deepEqual(await response.json(), { output: { id: 'created-through-grpc' } });
  assert.match(await (await fetch(base)).text(), /&lt;Customers&gt;/);
  child.kill('SIGTERM'); const [code] = await once(child, 'exit'); assert.equal(code, 0, stderr);
});

test('headless build omits UI and browser bundle rejects server imports', async t => {
  const directory = await temporary(t);
  const entry = resolve(sourceRoot, 'examples/customer-app/app.ts');
  const result = await buildMinionArtifact({ ...common, architecture: 'amd64', entry, outdir: resolve(directory, 'headless'), secrets: { APP_TOKEN: 'stored-app-token' } });
  assert.deepEqual(JSON.parse(await readFile(resolve(result.directory, '.hostlier'), 'utf8')).secrets, { APP_TOKEN: 'secret://stored-app-token' });
  assert.deepEqual(result.project?.secrets, { APP_TOKEN: 'stored-app-token' });
  assert.deepEqual(JSON.parse(await readFile(resolve(result.directory, '.hostlier'), 'utf8')).source, { container: { context: '.', architecture: 'amd64' } });
  assert.ok(!(await readdir(result.directory)).includes('public'));
  await writeFile(resolve(directory, 'bad-ui.ts'), `import {buildMinion} from '@fused/minion'; console.log(buildMinion);`);
  await assert.rejects(buildMinionArtifact({ ...common, entry, outdir: resolve(directory, 'bad'), ui: { entry: resolve(directory, 'bad-ui.ts') } }), /Server imports cannot enter the UI/);
});

test('Hostlier exporter requires explicit identities and never serializes extra credential fields', () => {
  assert.throws(() => hostlierProject({ ...common, image: 'example/mini:latest' }));
  assert.throws(() => hostlierProject({ ...common, architecture: 'sparc' as 'amd64' }), /amd64 or arm64/);
  assert.deepEqual(hostlierProject(common).source, { image: common.image });
  assert.throws(() => hostlierProject({ ...common, fused: { ...common.fused, grpcUrl: 'http://remote.example:50051' } }));
  assert.throws(() => hostlierProject({ ...common, secrets: { FUSED_APP_TOKEN: 'override' } }));
  const value = hostlierProject({ ...common, fused: { ...common.fused, token: 'private-token' } } as typeof common);
  assert.ok(!JSON.stringify(value).includes('private-token'));
});

test('consent build collects declarations and emits no deployable placeholder identity', async t => {
  const directory = await temporary(t);
  const entry = resolve(directory, 'consent.ts');
  await writeFile(entry, `import * as z from 'zod/mini'; import {buildMinion,defineAction,defineService,defineServices,defineOperation} from '@fused/minion'; const services=defineServices({crm:defineService({service:'crm',version:'v1',operations:{create:defineOperation({endpoint:'createCustomer',input:z.object({name:z.string()}),output:z.object({id:z.string()})})}})}); export default buildMinion({actions:{create:defineAction({input:z.object({name:z.string()}),output:z.object({id:z.string()}),execute:({input})=>services.crm.create(input)})}});`);
  const result = await buildMinionArtifact({ ...common, architecture: 'arm64', entry, outdir: resolve(directory, 'consent'), secrets: { APP_TOKEN: 'stored-app-token' }, fused: { engineUrl: 'https://engine.example', grpcUrl: 'https://grpc.example', tokenSecret: 'consent-token', version: '1.0.0', bucket: 'demo' } });
  assert.equal(result.project, null);
  assert.equal(result.manifest?.bucket, 'demo');
  assert.equal(JSON.parse(await readFile(resolve(result.directory, 'fused-access.json'), 'utf8')).bucket, 'demo');
  assert.deepEqual(result.manifest?.services, { crm: { version: 'v1', operations: ['createCustomer'] } });
  assert.ok(!(await readdir(result.directory)).includes('.hostlier'));
  const pending = JSON.parse(await readFile(resolve(result.directory, 'hostlier-pending.json'), 'utf8'));
  assert.equal(pending.fused.appId, undefined);
  assert.deepEqual(pending.secrets, { APP_TOKEN: 'stored-app-token' });
  assert.deepEqual(pending.source, { container: { context: '.', architecture: 'arm64' } });
  assert.equal(pending.consent.digest, result.digest);
  assert.equal(pending.consent.engineUrl, 'https://engine.example');
  const pkg = JSON.parse(await readFile(resolve(result.directory, 'package.json'), 'utf8'));
  assert.equal(pkg.dependencies['@fused/runtime'], undefined);
});

test('loader data cannot break out of the HTML hydration payload', async () => {
  const { pageRenderer } = await import('../src/page.js');
  const payload = '</script><script>alert(1)</script>$&';
  const render = pageRenderer('<div id="app"></div>', ({ data }) => data, async () => payload);
  const html = await render({});
  assert.ok(!html.includes('<script>alert'));
  const state = html.match(/<script type="application\/json" id="minion-data">(.*?)<\/script>/)![1]!;
  assert.equal(JSON.parse(state), payload);
  assert.match(html, /&lt;script&gt;alert/);
});

test('the browser bundle rejects accidental imports of a server loader', async t => {
  const directory = await temporary(t);
  const load = resolve(directory, 'load.ts'), entry = resolve(directory, 'ui.ts');
  await writeFile(load, "export default () => 'server-only';");
  await writeFile(entry, "import load from './load'; export default () => load();");
  await assert.rejects(buildMinionArtifact({ ...common, entry: resolve(sourceRoot, 'examples/customer-app/app.ts'), outdir: resolve(directory, 'out'), ui: { entry, load } }), /Server imports cannot enter the UI/);
});

test('building a portable artifact needs no hostname or registry, and honours explicit runtime images', async t => {
  const directory = await temporary(t);
  const entry = resolve(directory, 'actions.ts');
  await writeFile(entry, 'export default { actions: {} };');
  const offline = await buildMinionArtifact({ entry, name: 'portable', outdir: resolve(directory, 'offline') });
  assert.equal(offline.project, null);
  assert.ok(!(await readdir(offline.directory)).includes('.hostlier'));
  assert.match(await readFile(resolve(offline.directory, 'Dockerfile'), 'utf8'), /^FROM node:24\.\d+\.\d+/);
  const local = await buildMinionArtifact({ entry, name: 'portable', outdir: resolve(directory, 'arm'), route: 'portable.example.com', architecture: 'arm64', image: 'unused:latest', baseImage: 'node:24.21.0-alpine' });
  assert.deepEqual(local.project?.source, { container: { context: '.', architecture: 'arm64' } });
  assert.match(await readFile(resolve(local.directory, 'Dockerfile'), 'utf8'), /^FROM node:24\.21\.0-alpine/);
  assert.deepEqual(hostlierProject({ name: 'portable', route: 'mini.example.com', image: 'registry.example.com/team/app:v2' }).source, { image: 'registry.example.com/team/app:v2' });
});
