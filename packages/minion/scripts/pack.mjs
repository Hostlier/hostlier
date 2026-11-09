import { cp, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { tmpdir } from 'node:os';
import { dirname, resolve, isAbsolute } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
const execute = promisify(execFile);
const root = fileURLToPath(new URL('..', import.meta.url));
const args = process.argv.slice(2);
// A narrow destination option keeps the stage operation separate from arbitrary npm flags.
if (args.length && (args.length !== 2 || args[0] !== '--pack-destination')) throw new Error('Usage: npm run pack -- [--pack-destination DIRECTORY]');
const destination = resolve(args[1] ?? process.cwd());
const runtimeRoot = dirname(dirname(dirname(createRequire(import.meta.url).resolve('@fused/runtime/protocol'))));

/** Copy declared package payloads rather than following repository-local node_modules links. */
async function stagePackage(source, target, manifest) {
  await mkdir(target, { recursive: true });
  for (const entry of manifest.files ?? []) {
    // Package file declarations must stay inside their own package root.
    if (typeof entry !== 'string' || isAbsolute(entry) || entry.split(/[\\/]/).includes('..')) throw new Error('Invalid package file declaration');
    await cp(resolve(source, entry), resolve(target, entry), { recursive: true, dereference: true });
  }
  await writeFile(resolve(target, 'package.json'), JSON.stringify(manifest, null, 2) + '\n');
}

// Runtime is installed from Engine's pinned compiled archive; only Minion builds here.
await execute('npm', ['run', 'build'], { cwd: root });
const packageManifest = JSON.parse(await readFile(resolve(root, 'package.json'), 'utf8'));
const runtimeManifest = JSON.parse(await readFile(resolve(runtimeRoot, 'package.json'), 'utf8'));
// Consumers receive a normal versioned dependency fulfilled by the bundled runtime payload.
packageManifest.dependencies['@fused/runtime'] = runtimeManifest.version;
const stage = await mkdtemp(resolve(tmpdir(), 'minion-package-'));
try {
  await stagePackage(root, stage, packageManifest);
  await stagePackage(runtimeRoot, resolve(stage, 'node_modules/@fused/runtime'), runtimeManifest);
  await mkdir(destination, { recursive: true });
  const { stdout } = await execute('npm', ['pack', '--ignore-scripts', '--json', '--pack-destination', destination], { cwd: stage });
  const [receipt] = JSON.parse(stdout);
  // An archive must never recreate the repository's parent-relative dependency paths.
  if (receipt.files.some(entry => isAbsolute(entry.path) || entry.path.split(/[\\/]/).includes('..'))) throw new Error('Package contains non-canonical archive paths');
  // Missing bundled code would otherwise cause installation to fall back to an unpublished package.
  if (!receipt.files.some(entry => entry.path === 'node_modules/@fused/runtime/dist/src/index.js')) throw new Error('Package is missing its shared runtime');
  process.stdout.write(JSON.stringify({ file: resolve(destination, receipt.filename), integrity: receipt.integrity, entryCount: receipt.entryCount }, null, 2) + '\n');
} finally { await rm(stage, { recursive: true, force: true }); }
