import { readFile, writeFile } from 'node:fs/promises';
// Include build-time runtime manifests in the published tarball; npm omits top-level package-lock.json.
const pkg = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'));
const lock = JSON.parse(await readFile(new URL('../package-lock.json', import.meta.url), 'utf8'));
// The server bundle includes the runtime client; Docker never resolves a repository-local package link.
delete pkg.dependencies['@fused/runtime'];
delete pkg.bundleDependencies;
delete lock.packages[''].dependencies['@fused/runtime'];
delete lock.packages[''].bundleDependencies;
delete lock.packages['node_modules/@fused/runtime'];
delete lock.packages['../runtime'];
await writeFile(new URL('../dist/src/runtime-package.json', import.meta.url), JSON.stringify(pkg, null, 2));
await writeFile(new URL('../dist/src/runtime-package-lock.json', import.meta.url), JSON.stringify(lock, null, 2));
