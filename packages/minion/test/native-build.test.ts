import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdtemp, writeFile, readFile, rm, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, dirname } from 'node:path';
import { createRequire } from 'node:module';
import { buildMinionArtifact } from '../src/build.js';

/** Exercise the real build pipeline with an isolated read-only Registry contract server. */
test('native build generates bindings and rejects type errors and changed pinned schemas', async t => {
  const directory = await mkdtemp(resolve(tmpdir(), 'minion-native-build-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await symlink(dirname(dirname(createRequire(import.meta.url).resolve('zod/package.json'))), resolve(directory, 'node_modules'));
  await writeFile(resolve(directory, 'package.json'), '{"type":"module"}');
  const serviceId = '11111111-1111-4111-8111-111111111111';
  const versionId = '22222222-2222-4222-8222-222222222222';
  const endpointId = '33333333-3333-4333-8333-333333333333';
  let outputType = 'string'; let reads = 0;
  // Fixture responses carry the same shape as the production Engine GraphQL projection.
  const registry = createServer(async (request, response) => {
    let raw = ''; for await (const chunk of request) raw += chunk;
    const input = JSON.parse(raw); reads++;
    assert.match(input.query, /^query /);
    const data = input.query.includes('MinionVersions')
      ? { serviceVersions: [{ id: versionId, service_id: serviceId, name: '1.0.0', contract_version: 2, required_capabilities: [] }] }
      : { serviceRuntimeContracts: [{ service_id: serviceId, service_version_id: versionId, version: '1.0.0', schema_definitions: {}, operations: [{ id: endpointId, name: 'ReadStatus', parameters: [{ name: 'limit', in: 'query', required: true, schema: { raw: { type: 'integer' } } }], responses: { '200': { representations: [{ media_type: 'application/json', schema: { raw: { type: 'object', properties: { status: { type: outputType } }, required: ['status'] } } }] } } }] }] };
    response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify({ data }));
  });
  await new Promise<void>(ready => registry.listen(0, '127.0.0.1', ready));
  t.after(() => new Promise<void>(done => registry.close(() => done())));
  const engineUrl = `http://127.0.0.1:${(registry.address() as { port: number }).port}`;
  const entry = resolve(directory, 'app.ts');
  const source = `import * as z from 'zod/mini'; import {buildMinion,defineAction,services} from '@fused/minion'; export default buildMinion({actions:{read:defineAction({access:'public',input:z.object({}),output:z.object({status:z.string()}),execute:()=>services.probe.ReadStatus({limit:1})})}});`;
  await writeFile(entry, source);
  const options = { name: 'native-probe', image: 'example/probe:1.0.0', route: 'probe.example.com', baseImage: 'node:24.21.0-bookworm-slim', entry, outdir: resolve(directory, 'good'), fused: { engineUrl, grpcUrl: 'http://127.0.0.1:15051', tokenSecret: 'probe-token', version: '1.0.0', bucket: 'demo', services: { probe: { service: 'local-readiness', version: '1.0.0', operations: ['ReadStatus'] } } } };
  const result = await buildMinionArtifact(options);
  assert.equal(result.project, null);
  assert.equal(result.manifest?.bucket, 'demo');
  assert.equal(JSON.parse(await readFile(resolve(result.directory, 'fused-access.json'), 'utf8')).bucket, 'demo');
  assert.deepEqual(result.manifest?.services, { 'local-readiness': { version: '1.0.0', operations: ['ReadStatus'] } });
  assert.match(await readFile(resolve(result.directory, 'server.mjs'), 'utf8'), /registerServices/);
  const lock = JSON.parse(await readFile(resolve(result.directory, 'fused-contract.json'), 'utf8'));
  assert.equal(lock.operations[0].endpointId, endpointId);
  assert.equal(lock.operations.length, 1);
  assert.match(await readFile(resolve(directory, '.minion/services.d.ts'), 'utf8'), /interface ServiceContracts/);
  // Invalid authored calls fail before a runnable artifact can be emitted.
  await writeFile(entry, source.replace('limit:1', "limit:'bad'"));
  await assert.rejects(buildMinionArtifact({ ...options, outdir: resolve(directory, 'bad-input') }), /not assignable/);
  await writeFile(entry, source.replace('ReadStatus', 'DeleteStatus'));
  await assert.rejects(buildMinionArtifact({ ...options, outdir: resolve(directory, 'bad-method') }), /DeleteStatus/);
  await writeFile(entry, source);
  // A Registry changing bytes under a pinned immutable revision cannot silently change the shipped SDK.
  outputType = 'number';
  await assert.rejects(buildMinionArtifact({ ...options, outdir: resolve(directory, 'changed') }), /pinned Fused operation contract changed/);
  assert.equal(reads, 8);
});
