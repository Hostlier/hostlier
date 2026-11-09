import { build } from 'esbuild';
import { mkdir, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';

const out = resolve(process.argv[2] ?? `dist/build-${Date.now()}`);
await mkdir(resolve(out, '..'), { recursive: true });
await mkdir(out); // A previous build is never silently overwritten.
await mkdir(resolve(out, 'public'));
await build({ entryPoints: ['server.ts'], outfile: resolve(out, 'server.mjs'), bundle: true, platform: 'node', format: 'esm', target: 'node24', minify: true });
await build({ entryPoints: ['ui.tsx'], outfile: resolve(out, 'public/ui.js'), bundle: true, platform: 'browser', format: 'esm', target: 'es2022', jsx: 'automatic', minify: true,
  define: { 'process.env.NODE_ENV': '"production"' },
  plugins: [{ name: 'browser-boundary', setup(plugin) {
    plugin.onResolve({ filter: /^(node:|@fused\/minion$|@fused\/minion\/(server|grpc)|@grpc\/)/ }, () => ({ errors: [{ text: 'Server code cannot enter the browser bundle' }] }));
  } }],
});
await writeFile(resolve(out, 'public/index.html'), '<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="theme-color" content="#f6f7f9"><title>Orbit · Agent playground</title><link rel="stylesheet" href="/ui.css"></head><body><div id="app"></div><script type="module" src="/ui.js"></script></body></html>');
await writeFile(resolve(out, 'Dockerfile'), 'FROM node:24.21.0-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20\nWORKDIR /app\nCOPY --chown=node:node server.mjs ./\nCOPY --chown=node:node public ./public\nUSER node\nENV PORT=8080 NODE_ENV=production\nEXPOSE 8080\nCMD ["node", "server.mjs"]\n');
await writeFile(resolve(out, '.dockerignore'), '*\n!server.mjs\n!public\n!public/**\n');
console.log(out);
