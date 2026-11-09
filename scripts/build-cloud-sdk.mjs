import { spawnSync } from 'node:child_process';
import { copyFile, readFile, readdir, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const source = join(root, 'packages/cloud-sdk');
const target = join(root, 'internal/cli/cloud_sdk_assets');
await copyFile(join(root, 'internal/controlrpc/controller.proto'), join(source, 'controller.proto'));
await copyFile(join(source, 'controller.proto'), join(target, 'controller.proto'));
await copyFile(join(root, 'internal/controlrpc/controller.pb.go'), join(target, 'controller.pb.go.txt'));
await copyFile(join(root, 'internal/controlrpc/watch.go'), join(target, 'controlrpc.watch.go.txt'));
// Embed exactly the versions used by this checkout in standalone Go installations.
const goMod = await readFile(join(root, 'go.mod'), 'utf8');
const goSum = await readFile(join(root, 'go.sum'), 'utf8');
const sums = ['connectrpc.com/connect', 'google.golang.org/protobuf', 'gopkg.in/yaml.v3'].flatMap(name => {
  const version = goMod.split('\n').map(line => line.trim().split(/\s+/)).find(([module]) => module === name)?.[1];
  const lines = goSum.split('\n').filter(line => line.startsWith(name + ' ' + version + ' ') || line.startsWith(name + ' ' + version + '/go.mod '));
  if (lines.length !== 2) throw new Error('Missing Go checksums for ' + name);
  return lines;
});
await writeFile(join(target, 'cloudsdk.sums.txt'), sums.join('\n') + '\n');
const esbuild = join(root, 'ui/node_modules/.bin/esbuild');
const result = spawnSync(esbuild, [join(source, 'index.js'), '--bundle', '--platform=node', '--format=esm', '--target=node24', '--legal-comments=inline', `--banner:js=import { createRequire } from 'node:module'; const require = createRequire(import.meta.url);`, `--outfile=${join(target, 'index.js')}`], { stdio: 'inherit' });
if (result.error) throw result.error;
if (result.status !== 0) throw new Error(`esbuild exited with status ${result.status}`);
await copyFile(join(source, 'index.d.ts'), join(target, 'index.d.ts'));
// Distribute the dependencies' original license and notice texts with the bundled client.
const lock = JSON.parse(await readFile(join(source, 'package-lock.json'), 'utf8'));
const notices = [];
for (const [path, pkg] of Object.entries(lock.packages)) {
  if (!path || pkg.dev) continue;
  const files = (await readdir(join(source, path))).filter(name => /^(licen[cs]e|notice)(\..*)?$/i.test(name)).sort();
  for (const name of files) notices.push(path.replace(/^node_modules\//, '') + '@' + pkg.version + ' — ' + name + '\n\n' + (await readFile(join(source, path, name), 'utf8')).trimEnd());
}
await writeFile(join(target, 'THIRD-PARTY-NOTICES'), notices.join('\n\n') + '\n');
await copyFile(join(source, 'node_modules/yaml/LICENSE'), join(target, 'YAML-LICENSE'));
await copyFile(join(root, 'packages/cloud-sdk-go/cloudsdk.go'), join(target, 'cloudsdk.go.txt'));
await copyFile(join(root, 'packages/cloud-sdk-go/grpc.go'), join(target, 'cloudsdk.grpc.go.txt'));
await copyFile(join(root, 'packages/cloud-sdk-go/api.go'), join(target, 'cloudsdk.api.go.txt'));
await copyFile(join(root, 'packages/cloud-sdk-go/README.md'), join(target, 'cloudsdk.README.md'));
const sourcePackage = JSON.parse(await readFile(join(source, 'package.json'), 'utf8'));
await writeFile(join(target, 'package.json'), JSON.stringify({ name: sourcePackage.name, version: sourcePackage.version, type: 'module', hostlierBundled: true, engines: sourcePackage.engines, exports: { '.': { types: './index.d.ts', import: './index.js' } } }, null, 2) + '\n');
