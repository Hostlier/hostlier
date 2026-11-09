import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const execute = promisify(execFile);
const cli = fileURLToPath(new URL('../src/cli.js', import.meta.url));

// CLI defaults must agree with Hostlier init without changing existing config values or build output.
for (const [label, selection, expected] of [
  ['omitted', {}, 'build'],
  ['empty', { outdir: '' }, 'build'],
  ['explicit', { outdir: 'releases/v1' }, 'releases/v1'],
] as const) {
  test(`build CLI resolves ${label} outdir beside the config and preserves existing artifacts`, async t => {
    const directory = await mkdtemp(resolve(tmpdir(), 'minion-cli-outdir-'));
    t.after(() => rm(directory, { recursive: true, force: true }));
    const root = resolve(directory, 'app');
    await mkdir(root);
    const config = resolve(root, 'minion.json');
    const source = JSON.stringify({
      name: 'default-output', entry: 'app.ts', image: 'default-output:0.1.0',
      route: 'default-output.example.com', baseImage: 'node:24.21.0-bookworm-slim', architecture: 'amd64', ...selection,
    });
    await writeFile(config, source);
    await writeFile(resolve(root, 'app.ts'), `import { buildMinion } from '@fused/minion'; export default buildMinion({ actions: {} });`);
    const { stdout } = await execute(process.execPath, [cli, 'build', config], { cwd: directory });
    const artifact = resolve(root, expected, 'artifact');
    assert.equal(JSON.parse(stdout).directory, artifact);
    assert.deepEqual(JSON.parse(await readFile(resolve(artifact, '.hostlier'), 'utf8')).source, { container: { context: '.', architecture: 'amd64' } });
    assert.match(await readFile(resolve(artifact, 'Dockerfile'), 'utf8'), /FROM node:24\.21\.0-bookworm-slim/);
    assert.equal(await readFile(config, 'utf8'), source);
    assert.deepEqual(await readdir(directory), ['app']);
    const server = await readFile(resolve(artifact, 'server.mjs'));
    await assert.rejects(execute(process.execPath, [cli, 'build', config], { cwd: directory }), /EEXIST/);
    assert.deepEqual(await readFile(resolve(artifact, 'server.mjs')), server);
  });
}
