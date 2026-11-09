import assert from 'node:assert/strict';
import { test, type TestContext } from 'node:test';
import { mkdtemp, mkdir, readFile, writeFile, rm, stat, utimes } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { contractAPIKey, checkNativeApplication, prepareServices, writePreparedServices, type PreparedServices } from '../src/prepare.js';
import { compileSelectedManifest, generateServicesDeclaration, type ResolvedOperation, type ServiceSelections } from '../src/contracts.js';

const serviceId = '11111111-1111-4111-8111-111111111111';
const versionId = '22222222-2222-4222-8222-222222222222';
const endpointId = '33333333-3333-4333-8333-333333333333';

/** Keep all lock/type artifacts within an individually owned disposable test directory. */
async function directory(t: TestContext): Promise<string> {
  const path = await mkdtemp(join(tmpdir(), 'minion-prepare-'));
  t.after(() => rm(path, { recursive: true, force: true }));
  return path;
}

/** Build a complete public preparation result without requiring provider calls or mutable remote state. */
function prepared(alias = 'billing', change: Partial<ResolvedOperation> = {}): PreparedServices {
  const operations: ResolvedOperation[] = [{ alias, service: '@example/billing', version: 'v1', serviceId, serviceVersionId: versionId, endpointId, endpoint: 'GetCustomer', inputSchema: { type: 'object', properties: { id: { type: 'string' } }, required: ['id'], additionalProperties: false }, outputSchema: { type: 'object', properties: { name: { type: 'string' } }, required: ['name'] }, ...change }];
  const selections: ServiceSelections = { [alias]: { service: '@example/billing', version: 'v1', operations: ['GetCustomer'] } };
  return { manifest: compileSelectedManifest(selections, 'billing-app', '1.0.0'), operations, declaration: generateServicesDeclaration(operations), lock: { schema_version: 1, engine: 'https://engine.example.test', selections, digest: 'sha256:' + createHash('sha256').update(JSON.stringify(operations)).digest('hex'), operations } };
}

/** Alias changes are authoring-only updates and must retain the same immutable provider contract. */
test('prepare lock permits a local alias rename without changing its provider contract', async t => {
  const path = await directory(t);
  await writePreparedServices(path, prepared());
  await writePreparedServices(path, prepared('payments'));
  const lock = JSON.parse(await readFile(join(path, 'fused-contract.json'), 'utf8'));
  assert.equal(lock.operations[0].alias, 'payments');
  assert.equal(lock.operations[0].serviceVersionId, versionId);
  assert.deepEqual(Object.keys(lock.selections), ['payments']);
  assert.match(await readFile(join(path, 'services.d.ts'), 'utf8'), /"payments"/);
});

/** Stable outputs must not feed custom generated-directory changes back into the development watcher. */
test('identical preparation preserves generated file contents and modification times', async t => {
  const path = join(await directory(t), 'generated');
  const contract = prepared();
  await writePreparedServices(path, contract);
  const files = ['fused-contract.json', 'services.d.ts'].map(name => join(path, name));
  const timestamp = new Date('2001-01-01T00:00:00Z');
  const previous = [];
  for (const file of files) {
    await utimes(file, timestamp, timestamp);
    previous.push({ content: await readFile(file, 'utf8'), mtime: (await stat(file)).mtimeMs });
  }
  await writePreparedServices(path, contract);
  await writePreparedServices(path, contract);
  for (const [index, file] of files.entries()) {
    assert.equal(await readFile(file, 'utf8'), previous[index]!.content);
    assert.equal((await stat(file)).mtimeMs, previous[index]!.mtime);
  }
});

/** An alias rename must never hide drift of a pinned version, operation identity or public schema. */
test('prepare lock rejects contract drift behind an alias rename and preserves previous artifacts', async t => {
  for (const change of [
    { serviceVersionId: '44444444-4444-4444-8444-444444444444' },
    { endpointId: '55555555-5555-4555-8555-555555555555' },
    { inputSchema: { type: 'object', properties: { id: { type: 'number' } }, required: ['id'] } },
    { outputSchema: { type: 'object', properties: { name: { type: 'number' } }, required: ['name'] } },
  ] satisfies Partial<ResolvedOperation>[]) {
    const path = await directory(t);
    await writePreparedServices(path, prepared());
    const beforeLock = await readFile(join(path, 'fused-contract.json'), 'utf8');
    const beforeTypes = await readFile(join(path, 'services.d.ts'), 'utf8');
    await assert.rejects(writePreparedServices(path, prepared('payments', change)), /pinned Fused operation contract changed/);
    assert.equal(await readFile(join(path, 'fused-contract.json'), 'utf8'), beforeLock);
    assert.equal(await readFile(join(path, 'services.d.ts'), 'utf8'), beforeTypes);
  }
});

/** Build credentials may authorize metadata reads but can never become generated SDK metadata. */
test('prepare emits only public metadata and strips extra configuration credentials', async t => {
  const key = 'fixture-build-management-credential';
  const previous = process.env.FUSED_API_KEY;
  process.env.FUSED_API_KEY = key;
  t.after(() => {
    // Restore the caller's environment exactly, including absence of the variable.
    if (previous === undefined) delete process.env.FUSED_API_KEY;
    else process.env.FUSED_API_KEY = previous;
  });
  t.mock.method(globalThis, 'fetch', async (_input: unknown, init: RequestInit) => {
    const request = JSON.parse(String(init.body));
    assert.equal((init.headers as Record<string, string>)['X-API-Key'], key);
    assert.equal(JSON.stringify(request).includes(key), false);
    // Resolution reads metadata only; no deployment or token endpoint is used.
    assert.match(request.query, /^query /);
    if (request.query.includes('MinionVersions')) return Response.json({ data: { serviceVersions: [{ id: versionId, service_id: serviceId, name: 'v1', contract_version: 2, required_capabilities: [] }] } });
    return Response.json({ data: { serviceRuntimeContracts: [{ service_id: serviceId, service_version_id: versionId, version: 'v1', operations: [{ id: endpointId, name: 'GetCustomer', parameters: [], responses: {} }] }] } });
  });
  const selections = { billing: { service: '@example/billing', version: 'v1', operations: ['GetCustomer'], token: 'fixture-config-secret', apiKey: 'fixture-config-key' } };
  const result = await prepareServices('billing-app', { engineUrl: 'https://engine.example.test', version: '1.0.0', bucket: 'demo', services: selections });
  assert.equal(result.manifest.bucket, 'demo');
  const path = await directory(t);
  await writePreparedServices(path, result);
  const output = JSON.stringify(result) + await readFile(join(path, 'fused-contract.json'), 'utf8') + await readFile(join(path, 'services.d.ts'), 'utf8');
  for (const secret of [key, selections.billing.token, selections.billing.apiKey]) assert.equal(output.includes(secret), false);
  assert.deepEqual(result.lock.selections, { billing: { service: '@example/billing', version: 'v1', operations: ['GetCustomer'] } });
});

/** Application ambient declarations must survive native type checking while stale generated services do not. */
test('native application typecheck preserves ambient declarations and replaces stale service types', async t => {
  const path = await directory(t);
  await mkdir(join(path, '.minion'));
  await mkdir(join(path, 'current'));
  await writeFile(join(path, 'package.json'), '{"type":"module"}');
  await writeFile(join(path, 'tsconfig.json'), JSON.stringify({ compilerOptions: { target: 'ES2023', module: 'NodeNext', moduleResolution: 'NodeNext', strict: true, skipLibCheck: true, types: [] }, include: ['app.ts', 'ambient.d.ts', '.minion/services.d.ts'] }));
  await writeFile(join(path, 'ambient.d.ts'), `declare const APP_TITLE: string;\ndeclare module '*.css' { const css: string; export default css; }\n`);
  await writeFile(join(path, '.minion/services.d.ts'), `import '@fused/minion'; declare module '@fused/minion' { interface ServiceContracts { billing: { RemovedOperation: {input:{};output:number} } } }`);
  const declaration = join(path, 'current/services.d.ts');
  await writeFile(declaration, prepared().declaration);
  const entry = join(path, 'app.ts');
  await writeFile(entry, `import {services} from '@fused/minion'; import css from './styles.css'; export async function read() { const customer=await services.billing.GetCustomer({id:'one'}); const label:string=APP_TITLE+css+customer.name; return label; }`);
  assert.doesNotThrow(() => checkNativeApplication(entry, declaration));
  await writeFile(entry, `import {services} from '@fused/minion'; export async function read() { return services.billing.GetCustomer({id:42}); }`);
  assert.throws(() => checkNativeApplication(entry, declaration), /number.*not assignable.*string/s);
});

test('saved contract login stays on its original TLS origin and expires', async t => {
  const prior = process.env.FUSED_API_KEY;
  delete process.env.FUSED_API_KEY;
  t.after(() => { if (prior !== undefined) process.env.FUSED_API_KEY = prior; });
  const dir = await directory(t);
  await mkdir(join(dir, 'fused'));
  const path = join(dir, 'fused/config.json');
  const login = { engine_url: 'https://engine.example', api_key: 'fixture-only' };
  await writeFile(path, JSON.stringify(login));
  assert.equal(await contractAPIKey(login.engine_url, dir), login.api_key);
  for (const target of ['https://engine.example.other', 'https://engine.example:444', 'http://engine.example', 'https://engine.example/path']) {
    assert.equal(await contractAPIKey(target, dir), undefined);
  }
  await writeFile(path, JSON.stringify({ ...login, api_key_expires_at: '2000-01-01' }));
  assert.equal(await contractAPIKey(login.engine_url, dir), undefined);
});
