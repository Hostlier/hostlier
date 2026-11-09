import { spawnSync } from 'node:child_process';
import { copyFile, readFile, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const source = join(root, 'packages/cloud-sdk');
const target = join(root, 'internal/cli/cloud_sdk_assets');
const esbuild = join(root, 'ui/node_modules/.bin/esbuild');
const result = spawnSync(esbuild, [join(source, 'index.js'), '--bundle', '--platform=node', '--format=esm', '--target=node24', '--legal-comments=inline', `--banner:js=import { createRequire } from 'node:module'; const require = createRequire(import.meta.url);`, `--outfile=${join(target, 'index.js')}`], { stdio: 'inherit' });
if (result.error) throw result.error;
if (result.status !== 0) throw new Error(`esbuild exited with status ${result.status}`);
await copyFile(join(source, 'index.d.ts'), join(target, 'index.d.ts'));
await copyFile(join(source, 'node_modules/yaml/LICENSE'), join(target, 'YAML-LICENSE'));
await copyFile(join(root, 'packages/cloud-sdk-go/cloudsdk.go'), join(target, 'cloudsdk.go.txt'));
await copyFile(join(root, 'packages/cloud-sdk-go/api.go'), join(target, 'cloudsdk.api.go.txt'));
await copyFile(join(root, 'packages/cloud-sdk-go/README.md'), join(target, 'cloudsdk.README.md'));
const sourcePackage = JSON.parse(await readFile(join(source, 'package.json'), 'utf8'));
await writeFile(join(target, 'package.json'), JSON.stringify({ name: sourcePackage.name, version: sourcePackage.version, type: 'module', hostlierBundled: true, engines: sourcePackage.engines, exports: { '.': { types: './index.d.ts', import: './index.js' } } }, null, 2) + '\n');
