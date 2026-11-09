import { build } from 'esbuild';
import { copyFile, mkdir } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const root = path.dirname(fileURLToPath(import.meta.url));
const output = path.resolve(root, '../internal/localui/assets');
await mkdir(output, { recursive: true });
const css = spawnSync(process.execPath, [path.join(root, 'node_modules/@tailwindcss/cli/dist/index.mjs'), '-i', 'src/style.css', '-o', path.join(output, 'style.css'), '--minify'], { cwd: root, stdio: 'inherit' });
if (css.status !== 0) process.exit(css.status || 1);
await build({ entryPoints: [path.join(root, 'src/app.js')], bundle: true, outfile: path.join(output, 'app.js'), format: 'iife', target: ['es2022'], minify: true, legalComments: 'none' });
await copyFile(path.join(root, 'src/index.html'), path.join(output, 'index.html'));
console.log('Hostlier UI built into internal/localui/assets');
