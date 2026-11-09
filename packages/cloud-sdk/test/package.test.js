import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const packageDirectory = fileURLToPath(new URL('..', import.meta.url));

test('npm package includes the direct controller transport', async t => {
  const scratch = await mkdtemp(join(tmpdir(), 'hostlier-cloud-pack-'));
  t.after(() => rm(scratch, { recursive: true, force: true }));
  const result = spawnSync('npm', ['pack', '--dry-run', '--json', '--ignore-scripts'], {
    cwd: packageDirectory,
    encoding: 'utf8',
    env: { ...process.env, npm_config_cache: join(scratch, 'npm-cache') },
  });
  assert.equal(result.status, 0, result.stderr);
  const files = new Set(JSON.parse(result.stdout)[0].files.map(file => file.path));
  assert(files.has('index.js'));
  assert(files.has('controller.js'));
  assert(files.has('index.d.ts'));
});
