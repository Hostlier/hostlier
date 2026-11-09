// Executed after the embedded compiler selection rules. Never import authored application code.
import { stat } from 'node:fs/promises';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const [root, compilerPath] = process.argv.slice(1);
const app = JSON.parse(readFileSync(0,'utf8'));
const fused = app.fused;
const engine = new URL(fused.engineUrl);
const loopback = engine.protocol === 'http:' && ['127.0.0.1', '[::1]'].includes(engine.hostname);
if (!(engine.protocol === 'https:' || loopback) || engine.pathname !== '/' || engine.username || engine.password || engine.hash || engine.search) throw new Error('Expected a bare Engine origin');
let manifest;
if (fused.services !== undefined) {
  manifest = compileSelectedManifest(fused.services,app.name,fused.version);
} else {
  let entry = app.entry;
  if (!entry) {
    for (const candidate of ['app.ts','src/app.ts','app.tsx','src/app.tsx']) {
      if (await stat(resolve(root,candidate)).then(info => info.isFile(),()=>false)) { entry=candidate; break; }
    }
  }
  const imports = [entry];
  if (app.ui?.load) imports.push(app.ui.entry,app.ui.load);
  const resolveCompiler = createRequire(compilerPath);
  const { build } = resolveCompiler('esbuild');
  const graph = await build({entryPoints:imports.map(file=>resolve(root,file)),bundle:true,metafile:true,write:false,
    outdir:resolve(root,'.minion/permission-analysis'),platform:'node',format:'esm',target:'node24',
    jsx:'automatic',external:['@fused/minion','@grpc/grpc-js','@grpc/proto-loader'],loader:{'.css':'empty'},logLevel:'silent'});
  const { compileAccessManifest } = await import(pathToFileURL(resolve(dirname(compilerPath),'manifest.js')).href);
  manifest = compileAccessManifest(Object.keys(graph.metafile.inputs),app.name,fused.version);
}
process.stdout.write(JSON.stringify({engineUrl:engine.origin,manifest:withSelectedBucket(manifest,fused.bucket)}));
