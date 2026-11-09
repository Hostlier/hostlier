import { defineService } from '../src/index.js';
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { compileAccessManifest, manifestDigest } from '../src/manifest.js';

/** Keep source inspection fixtures independent of application evaluation and package installation. */
async function fixture(t: test.TestContext, source: string) {
  const directory = await mkdtemp(join(tmpdir(), 'minion-manifest-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const file = join(directory, 'app.ts');
  await writeFile(file, source);
  return { directory, file };
}

test('compiler derives canonical permissions from services and imported operation declarations without execution', async t => {
  const { directory, file } = await fixture(t, `
    import {defineService as service, defineServices} from '@fused/minion';
    import {operation} from './operations.js';
    throw new Error('must never execute during build');
    const slack = service({service:'slack',version:'v1',operations:{post:operation,again:operation}});
    export const services = defineServices({messaging:slack});
  `);
  const operations = join(directory, 'operations.ts');
  await writeFile(operations, `import {defineOperation} from '@fused/minion'; export const operation = defineOperation({endpoint:'chat.postMessage',input:null,output:null});`);
  const manifest = compileAccessManifest([file, operations], 'support', '1.0.0');
  assert.deepEqual(manifest.services, { slack: { version: 'v1', operations: ['chat.postMessage'] } });
  assert.equal(manifestDigest(manifest), manifestDigest(compileAccessManifest([operations, file], 'support', '1.0.0')));
  assert.match(await readFile(file, 'utf8'), /must never execute/);
});

test('compiler refuses incomplete or dynamic permission declarations', async t => {
  for (const declaration of [
    `{service:'slack',version:process.env.VERSION,operations:{post:{endpoint:'send'}}}`,
    `{service:'slack',version:'v1',operations:getOperations()}`,
    `{service:'slack',version:'v1',operations:{...extra}}`,
    `{service:'slack',version:'v1',operations:{post:{endpoint:choose()}}}`,
  ]) {
    const { file } = await fixture(t, `import {defineService} from '@fused/minion'; defineService(${declaration});`);
    assert.throws(() => compileAccessManifest([file], 'support', '1.0.0'));
  }
  const { file } = await fixture(t, `import {defineServices} from '@fused/minion'; defineServices({slack:{post:{endpoint:'send'}}});`);
  assert.throws(() => compileAccessManifest([file], 'support', '1.0.0'), /defineService/);
});

test('compiler includes every defined service and rejects conflicting service versions', async t => {
  const { file } = await fixture(t, `import * as minion from '@fused/minion';
    minion.defineService({service:'slack',version:'v1',operations:{post:{endpoint:'send'}}});
    minion.defineService({service:'slack',version:'v2',operations:{post:{endpoint:'send'}}});`);
  assert.throws(() => compileAccessManifest([file], 'support', '1.0.0'), /Conflicting/);
});

test('compiler follows barrel re-exports and refuses ambiguous cross-service endpoints', async t => {
  const { directory, file } = await fixture(t, `import {service} from './barrel.js'; const declare=service; declare({service:'slack',version:'v1',operations:{send:{endpoint:'send'}}});`);
  const barrel = join(directory, 'barrel.ts');
  await writeFile(barrel, `export {defineService as service} from '@fused/minion';`);
  assert.deepEqual(compileAccessManifest([file, barrel], 'support', '1.0.0').services, { slack: { version: 'v1', operations: ['send'] } });
  await writeFile(file, `import {defineService} from '@fused/minion'; defineService({service:'slack',version:'v1',operations:{send:{endpoint:'send'}}}); defineService({service:'teams',version:'v1',operations:{send:{endpoint:'send'}}});`);
  assert.throws(() => compileAccessManifest([file], 'support', '1.0.0'), /Ambiguous/);
});

/** Scoped Registry identities must survive compilation without widening permission scope. */
test('compiler preserves scoped service identity and rejects malformed namespace paths', async t => {
  const { file } = await fixture(t, `import {defineService} from '@fused/minion';
    defineService({service:'@example-owner/stripe',version:'2026-07-29.dahlia',operations:{latest:{endpoint:'GetCharges'}}});`);
  assert.doesNotThrow(() => defineService({ service: '@example-owner/stripe', version: '2026-07-29.dahlia', operations: {} }));
  assert.deepEqual(compileAccessManifest([file], 'payments', '1.0.0').services, {
    '@example-owner/stripe': { version: '2026-07-29.dahlia', operations: ['GetCharges'] },
  });
  for (const slug of ['@/stripe', '@owner/', 'owner/stripe', '@owner/stripe/extra', '@owner/*']) {
    await writeFile(file, `import {defineService} from '@fused/minion'; defineService({service:'${slug}',version:'v1',operations:{latest:{endpoint:'GetCharges'}}});`);
    assert.throws(() => compileAccessManifest([file], 'payments', '1.0.0'), /Invalid Fused service identity/);
    assert.throws(() => defineService({ service: slug, version: 'v1', operations: {} }), /exact Fused service/);
  }
});
