import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import ts from 'typescript';
import { compileSelectedManifest, generateServicesDeclaration, resolveSelectedContracts, type ServiceSelections } from '../src/contracts.js';

const serviceId = '11111111-1111-4111-8111-111111111111';
const versionId = '22222222-2222-4222-8222-222222222222';
const endpointId = '33333333-3333-4333-8333-333333333333';
const selection: ServiceSelections = { billing: { service: '@example/billing', version: 'v1', operations: ['GetCustomer'] } };

/** Model the exact two Registry reads without allowing mutations or leaked credential bodies. */
function fixtureFetch(overrides: Record<string, unknown> = {}): typeof globalThis.fetch {
  return async (_input, init) => {
    const body = JSON.parse(String(init?.body));
    assert.match(body.query, /^query /);
    assert.equal(init?.redirect, 'error');
    assert.equal(JSON.stringify(body).includes('private-build-key'), false);
    assert.equal((init?.headers as Record<string, string>)['X-API-Key'], 'private-build-key');
    // The first query resolves an exact provider namespace and pinned revision.
    if (body.query.includes('MinionVersions')) {
      assert.deepEqual(body.variables, { serviceId: 'billing', provider: 'example' });
      return Response.json({ data: { serviceVersions: [{ id: versionId, service_id: serviceId, name: 'v1', contract_version: 2, required_capabilities: [] }] } });
    }
    assert.match(body.query, /retain_generation_contract:false/);
    assert.deepEqual(body.variables.refs, [{ service_id: serviceId, version: versionId }]);
    return Response.json({ data: { serviceRuntimeContracts: [{ service_id: serviceId, service_version_id: versionId, version: 'v1', schema_definitions: {
      Customer: { raw: { type: 'object', properties: { id: { type: 'string' }, parent: { $ref: '#/components/schemas/Customer' } }, required: ['id'] } },
    }, operations: [{ id: endpointId, name: 'GetCustomer', parameters: [{ name: 'limit', required: true, schema: { raw: { type: 'integer' } } }], responses: { '200': { representations: [{ media_type: 'application/json', schema: { raw: { $ref: '#/components/schemas/Customer' } } }] } } }, { id: '44444444-4444-4444-8444-444444444444', name: 'DeleteCustomer' }], ...overrides }] } });
  };
}

/** Compile generated native declarations with real caller code so expected errors cannot silently disappear. */
async function checkUsage(declaration: string, usage: string): Promise<void> {
  const directory = await mkdtemp(join(tmpdir(), 'minion-contract-usage-'));
  try {
    await writeFile(join(directory, 'base.d.ts'), `declare module '@fused/minion' { export interface ServiceContracts {} export const services: { [S in keyof ServiceContracts]: { [O in keyof ServiceContracts[S]]: ServiceContracts[S][O] extends {input:infer I;output:infer R} ? (input:I)=>Promise<R> : never } }; }`);
    await writeFile(join(directory, 'generated.d.ts'), declaration);
    await writeFile(join(directory, 'app.ts'), `import {services} from '@fused/minion';\n${usage}`);
    const program = ts.createProgram(['base.d.ts', 'generated.d.ts', 'app.ts'].map(file => join(directory, file)), { strict: true, noEmit: true, target: ts.ScriptTarget.ES2023, module: ts.ModuleKind.NodeNext, moduleResolution: ts.ModuleResolutionKind.NodeNext, types: [], skipLibCheck: false });
    assert.deepEqual(ts.getPreEmitDiagnostics(program).map(diagnostic => ts.flattenDiagnosticMessageText(diagnostic.messageText, '\n')), []);
  } finally { await rm(directory, { recursive: true, force: true }); }
}

/** Consent permissions are canonical and refuse ambiguous endpoint-only execution scope. */
test('selected manifests preserve pinned provider identity and reject floating or ambiguous scope', () => {
  assert.deepEqual(compileSelectedManifest(selection, 'billing-app', '1.0.0'), { schema_version: 1, name: 'billing-app', version: '1.0.0', services: { '@example/billing': { version: 'v1', operations: ['GetCustomer'] } } });
  assert.throws(() => compileSelectedManifest({ billing: { ...selection.billing!, version: 'latest' } }, 'billing-app', '1.0.0'), /exact/);
  assert.throws(() => compileSelectedManifest({ billing: { ...selection.billing!, operations: ['*'] } }, 'billing-app', '1.0.0'), /Invalid/);
  assert.throws(() => compileSelectedManifest({ billing: { ...selection.billing!, operations: ['GetCustomer', 'GetCustomer'] } }, 'billing-app', '1.0.0'), /unique/);
  assert.throws(() => compileSelectedManifest({ ...selection, other: { service: 'other', version: 'v1', operations: ['GetCustomer'] } }, 'billing-app', '1.0.0'), /Ambiguous/);
});

/** Exact schema reads cannot silently substitute revisions or add unselected operations. */
test('resolver retains immutable IDs and rejects changed contracts', async () => {
  const options = { engineUrl: 'https://engine.example.test', apiKey: 'private-build-key', fetch: fixtureFetch() };
  const operations = await resolveSelectedContracts(selection, options);
  assert.equal(operations.length, 1);
  assert.equal(operations[0]!.endpointId, endpointId);
  assert.equal(operations[0]!.serviceVersionId, versionId);
  assert.equal(JSON.stringify(operations).includes('DeleteCustomer'), false);
  assert.equal(JSON.stringify(operations).includes('private-build-key'), false);
  await assert.rejects(resolveSelectedContracts(selection, { ...options, fetch: fixtureFetch({ service_version_id: serviceId }) }), /different/);
  await assert.rejects(resolveSelectedContracts(selection, { ...options, engineUrl: 'http://public.example.test' }), /Unable to read/);
});

/** Generated declarations provide strict input/output typing with recursive shared schemas. */
test('native service declarations reject wrong arguments, missing methods and wrong output types', async () => {
  const operations = await resolveSelectedContracts(selection, { engineUrl: 'https://engine.example.test', apiKey: 'private-build-key', fetch: fixtureFetch() });
  const directory = await mkdtemp(join(tmpdir(), 'minion-contract-types-'));
  try {
    await writeFile(join(directory, 'base.d.ts'), `declare module '@fused/minion' { export interface ServiceContracts {} export const services: { [S in keyof ServiceContracts]: { [O in keyof ServiceContracts[S]]: ServiceContracts[S][O] extends {input:infer I;output:infer R} ? (input:I)=>Promise<R> : never } }; }`);
    await writeFile(join(directory, 'generated.d.ts'), generateServicesDeclaration(operations));
    await writeFile(join(directory, 'app.ts'), `import {services} from '@fused/minion';\nasync function read() { const customer = await services.billing.GetCustomer({limit:1}); const id:string=customer.id; const parent:string|undefined=customer.parent?.id;\n// @ts-expect-error Input number is contract-checked.\nawait services.billing.GetCustomer({limit:'bad'});\n// @ts-expect-error Required input is enforced.\nawait services.billing.GetCustomer({});\n// @ts-expect-error Unselected operation is absent.\nawait services.billing.DeleteCustomer({});\n// @ts-expect-error Response schema supplies a string.\nconst wrong:number=customer.id; return [id,parent,wrong]; }`);
    const program = ts.createProgram(['base.d.ts', 'generated.d.ts', 'app.ts'].map(file => join(directory, file)), { strict: true, noEmit: true, target: ts.ScriptTarget.ES2023, module: ts.ModuleKind.NodeNext, moduleResolution: ts.ModuleResolutionKind.NodeNext, types: [], skipLibCheck: false });
    assert.deepEqual(ts.getPreEmitDiagnostics(program).map(diagnostic => ts.flattenDiagnosticMessageText(diagnostic.messageText, '\n')), []);
  } finally { await rm(directory, { recursive: true, force: true }); }
});

/** Request representation defaults and composed body fields match Engine flattening. */
test('resolver compiles selected default media and composed request bodies', async () => {
  const operations = [{ id: endpointId, name: 'GetCustomer', request_content: { required: true, default_media_type: 'application/json', representations: [ { media_type: 'text/plain', schema: { raw: { type: 'string' } } }, { media_type: 'application/json', schema: { raw: { allOf: [{ type: 'object', properties: { name: { type: 'string' } }, required: ['name'] }, { type: 'object', properties: { enabled: { type: 'boolean' } } }] } } } ] } }];
  const result = await resolveSelectedContracts(selection, { engineUrl: 'https://engine.example.test', apiKey: 'private-build-key', fetch: fixtureFetch({ operations }) });
  const declaration = generateServicesDeclaration(result);
  assert.match(declaration, /"name": string/);
  assert.match(declaration, /"enabled"\?: boolean/);
});

/** Parameter-local definitions and annotation payloads cannot alter another parameter's schema scope. */
test('local references, nullable unions and auth fields keep their own exact contract', async () => {
  const operations = [{ id: endpointId, name: 'GetCustomer', security_requirements: [{ schemes: [{ scheme: 'providerKey' }] }], parameters: [
    { name: 'X-API-Key', in: 'header', required: true, type: 'string' },
    { name: 'Authorization', in: 'header', required: true, type: 'string' },
    { name: 'fused_internal', in: 'query', required: true, type: 'string' },
    { name: 'first', in: 'query', required: true, schema: { raw: { $ref: '#/$defs/value', $defs: { value: { type: 'string', nullable: true } }, examples: [{ $ref: 'https://example.test/not-a-schema' }] } } },
    { name: 'second', in: 'query', required: true, schema: { raw: { $ref: '#/$defs/value', $defs: { value: { type: 'integer' } } } } },
  ], responses: { '200': { representations: [{ schema: { raw: { type: 'object', properties: { common: { type: 'string' } }, required: ['common'], anyOf: [{ type: 'object', properties: { count: { type: 'integer' } } }, { type: 'object', properties: { enabled: { type: 'boolean' } } }] } } }] } } }];
  const result = await resolveSelectedContracts(selection, { engineUrl: 'https://engine.example.test', apiKey: 'private-build-key', fetch: fixtureFetch({ operations, service: { auth_configs: [{ name: 'providerKey', type: 'apiKey', location: 'header', key_name: 'x-api-key' }] } }) });
  const declaration = generateServicesDeclaration(result);
  assert.doesNotMatch(declaration, /X-API-Key|Authorization|fused_internal/);
  assert.match(declaration, /string \| null/);
  assert.match(declaration, /Definition\d = number/);
  assert.match(declaration, /"common": string/);
  assert.match(declaration, /"count"\?: number/);
  assert.match(declaration, /"enabled"\?: boolean/);
  assert.doesNotMatch(declaration, /\bany\b/);
  await checkUsage(declaration, `async function read() { const result = await services.billing.GetCustomer({first:null,second:1}); const common:string = result.common;
// @ts-expect-error Nullable string never admits a boolean.
await services.billing.GetCustomer({first:false,second:1});
// @ts-expect-error The second document's local definition remains a number.
await services.billing.GetCustomer({first:'ok',second:'wrong'}); return common; }`);
});

/** Required object bodies retain the at-least-one-field constraint without inventing a fixed required field. */
test('required bodies with optional fields produce an input presence union', async () => {
  const operations = [{ id: endpointId, name: 'GetCustomer', request_content: { required: true, representations: [{ media_type: 'application/json', serialization: 'json', schema: { raw: { type: 'object', properties: { first: { type: 'string' }, second: { type: 'number' } } } } }] } }];
  const result = await resolveSelectedContracts(selection, { engineUrl: 'https://engine.example.test', apiKey: 'private-build-key', fetch: fixtureFetch({ operations }) });
  const declaration = generateServicesDeclaration(result);
  assert.match(declaration, /"first"\?: string/);
  assert.match(declaration, /"first": unknown/);
  assert.match(declaration, /"second": unknown/);
  await checkUsage(declaration, `async function read() { await services.billing.GetCustomer({first:'ok'}); await services.billing.GetCustomer({second:1});
// @ts-expect-error A required closed body needs at least one body property.
await services.billing.GetCustomer({});
// @ts-expect-error Header overrides do not count as a body.
await services.billing.GetCustomer({_headers:{'X-Trace':'123'}}); }`);
});
