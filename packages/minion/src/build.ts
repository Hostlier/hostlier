import { build, type Plugin } from 'esbuild';
import { copyFile, mkdir, writeFile, rename, rm, mkdtemp, access, realpath } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { hostlierProject, type HostlierOptions } from './hostlier.js';
import { createRequire } from 'node:module';
import { compileAccessManifest, manifestDigest, withSelectedBucket, type AccessManifest } from './manifest.js';
import { engineAddress } from './grpc.js';
import { prepareServices, writePreparedServices, checkNativeApplication, type NativeFusedOptions } from './prepare.js';
export { prepareServices, writePreparedServices } from './prepare.js';
export type { ServiceSelections } from './contracts.js';
export { hostlierProject } from './hostlier.js';
export type { HostlierOptions } from './hostlier.js';
export interface BuildOptions extends Omit<HostlierOptions, 'fused' | 'route'> {
  /** Optional public text returned by the app metadata endpoint. */
  description?: string;
  entry: string; outdir: string; ui?: { entry: string; title?: string; load?: string }; baseImage?: string; route?: string;
  fused?: HostlierOptions['fused'] | { grpcUrl: string; tokenSecret: string; engineUrl: string; version: string; bucket?: string; services?: NativeFusedOptions['services'] };
  /** Generated editor declarations live beside the config; no generated import appears in application code. */
  typesDirectory?: string;
}

/** Escape user-authored titles before generating the static HTML shell. */
function html(value: string): string { return value.replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!); }

/** HTTP consent shares TLS/loopback policy with gRPC but retains its own canonical origin. */
function consentOrigin(value: string): string {
  engineAddress(value);
  const url = new URL(value);
  if (!['http:', 'https:'].includes(url.protocol)) throw new Error('Consent requires an HTTP(S) Engine origin');
  return url.origin;
}

/** Build server and browser separately without evaluating application modules or reading runtime credentials. */
export async function buildMinionArtifact(options: BuildOptions): Promise<{ directory: string; project: ReturnType<typeof hostlierProject> | null; manifest?: AccessManifest; digest?: string }> {
  const fused = options.fused;
  const consent = !!fused && !('appId' in fused);
  const engineUrl = fused && 'engineUrl' in fused ? consentOrigin(fused.engineUrl) : undefined;
  // Pending builds validate deployment fields but never publish the placeholder as a deployable app identity.
  const validated = options.route === undefined ? undefined : hostlierProject({ ...options, route: options.route, fused: fused ? { ...fused, appId: 'appId' in fused ? fused.appId : '00000000-0000-4000-8000-000000000001' } : undefined });
  const project = consent ? null : validated ?? null;
  const baseImage = options.baseImage === undefined ? 'node:24.21.0-bookworm-slim' : options.baseImage;
  if (!/^node:(?:24|2[5-9]|[3-9][0-9])\.\d+\.\d+(?:-[a-z0-9.-]+)?(?:@sha256:[a-f0-9]{64})?$/.test(baseImage)) throw new Error('Supply a concrete Node 24+ image version');
  const destination = resolve(options.outdir);
  await mkdir(dirname(destination), { recursive: true });
  const temporary = await mkdtemp(resolve(dirname(destination), '.minion-build-'));
  const root = fileURLToPath(new URL('.', import.meta.url));
  // Resolve this package from source too, allowing local builds before publishing it to npm.
  const ownPackage: Plugin = { name: 'minion', setup(plugin) {
    plugin.onResolve({ filter: /^@fused\/minion(?:\/(client|server|grpc))?$/ }, args => ({ path: resolve(root, (args.path.split('/')[2] ?? 'index') + '.js') }));
  } };
  try {
    const entry = resolve(options.entry);
    // Native declarations resolve once and feed both the callable SDK and its consent scope.
    const native = fused && 'services' in fused && fused.services ? await prepareServices(options.name, fused as NativeFusedOptions) : undefined;
    if (native) {
      await writePreparedServices(resolve(options.typesDirectory ?? resolve(dirname(entry), '.minion')), native);
      await writeFile(resolve(temporary, 'services.d.ts'), native.declaration);
      await writeFile(resolve(temporary, 'fused-contract.json'), JSON.stringify(native.lock, null, 2) + '\n');
      checkNativeApplication(entry, resolve(temporary, 'services.d.ts'));
    }
    const selected = Object.fromEntries(Object.keys(fused && 'services' in fused ? fused.services ?? {} : {}).map(alias => [alias, Object.fromEntries((native?.operations ?? []).filter(operation => operation.alias === alias).map(operation => [operation.endpoint, { endpoint: operation.endpoint }]))]));
    const appImport = native ? `import { registerServices } from '@fused/minion';\nregisterServices(${JSON.stringify(selected)});\nconst { default: app } = await import(${JSON.stringify(entry)});` : `import app from ${JSON.stringify(entry)};`;
    // A deterministic app starts locally without an Engine; accidental integration calls fail closed.
    const transportSource = fused ? `import { EngineTransport } from '@fused/minion/grpc';\nconst transport = new EngineTransport({ grpcUrl: process.env.FUSED_ENGINE_GRPC_URL, appId: process.env.FUSED_APP_ID, token: process.env.FUSED_APP_TOKEN });\nawait transport.connect();` : `const transport = { async execute() { throw new Error('Configure fused in minion.json before calling integrations'); }, async close() {} };`;
    if (options.description !== undefined && typeof options.description !== 'string') throw new Error('Minion description must be public text');
    const publicInfo = JSON.stringify({ name: options.name, description: options.description });
    const pageSetup = options.ui?.load ? `import Page from ${JSON.stringify(resolve(options.ui.entry))};
import load from ${JSON.stringify(resolve(options.ui.load))};
import { pageRenderer } from ${JSON.stringify(resolve(root, 'page.js'))};
import { readFile } from 'node:fs/promises';
const renderPage = pageRenderer(await readFile(new URL('./public/index.html', import.meta.url), 'utf8'), Page, load);` : '';
    const launcher = `${appImport}\nimport { createMinionServer } from '@fused/minion/server';\nimport { fileURLToPath } from 'node:url';\n${transportSource}\n${pageSetup}\nconst server = createMinionServer(app, { ...${publicInfo}, ${pageSetup ? "renderPage," : ""} transport, origin: process.env.MINION_ORIGIN, ${options.ui ? "uiDirectory: fileURLToPath(new URL('./public/', import.meta.url))," : ''} });\nserver.listen(Number(process.env.PORT || 8080), process.env.HOST || '0.0.0.0');\nlet closing = false;\nasync function shutdown() { if (closing) return; closing = true; const timer = setTimeout(() => process.exit(1), 30000); timer.unref(); server.close(async () => { try { await transport.close(); } finally { clearTimeout(timer); } }); }\nprocess.on('SIGTERM', shutdown); process.on('SIGINT', shutdown);\n`;
    const serverBuild = await build({ metafile: true, stdin: { contents: launcher, resolveDir: dirname(entry), sourcefile: 'minion-launcher.mjs' }, outfile: resolve(temporary, 'server.mjs'), bundle: true, platform: 'node', ...(pageSetup && { jsx: 'automatic' as const, loader: { '.css': 'empty' as const } }), format: 'esm', target: 'node24', external: ['@grpc/grpc-js', '@grpc/proto-loader'], banner: { js: "import { createRequire as __createRequire } from 'node:module'; const require = __createRequire(import.meta.url);" }, plugins: [ownPackage], logLevel: 'silent' });
    const manifest = native?.manifest ?? (fused && consent ? withSelectedBucket(compileAccessManifest(Object.keys(serverBuild.metafile!.inputs).filter(file => !file.startsWith('<')), options.name, 'version' in fused ? fused.version : ''), 'bucket' in fused ? fused.bucket : undefined) : undefined);
    const digest = manifest && manifestDigest(manifest);
    if (manifest) {
      await writeFile(resolve(temporary, 'fused-access.json'), JSON.stringify(manifest));
    }
    if (manifest && validated) {
      const { appId: _placeholder, ...pendingFused } = validated.fused!;
      await writeFile(resolve(temporary, 'hostlier-pending.json'), JSON.stringify({ ...validated, fused: pendingFused, consent: { engineUrl, manifest: 'fused-access.json', digest } }, null, 2) + '\n');
    }
    await copyFile(createRequire(import.meta.url).resolve('@fused/runtime/protocol'), resolve(temporary, 'engine.proto'));
    // Runtime dependencies are installed from the published package's exact lockfile.
    await copyFile(resolve(root, 'runtime-package-lock.json'), resolve(temporary, 'package-lock.json'));
    await copyFile(resolve(root, 'runtime-package.json'), resolve(temporary, 'package.json'));
    if (options.ui) {
      await mkdir(resolve(temporary, 'public'));
      const browserOnly: Plugin = { name: 'browser-boundary', setup(plugin) {
        plugin.onResolve({ filter: /^(@fused\/minion(?:\/(?:server|grpc|build))?|@fused\/(?:operations|runtime)|node:.*|@grpc\/.*)$/ }, () => ({ errors: [{ text: 'Server imports cannot enter the UI; import @fused/minion/client and use type-only app imports' }] }));
      } };
      const browserEntry = options.ui.load ? { stdin: { contents: `
import { createElement } from 'react';
import { hydrateRoot } from 'react-dom/client';
import { createActionClient } from '@fused/minion/client';
import Page from ${JSON.stringify(resolve(options.ui.entry))};
hydrateRoot(document.getElementById('app'), createElement(Page, {
  actions: createActionClient(), data: JSON.parse(document.getElementById('minion-data').textContent)
}));`, resolveDir: dirname(resolve(options.ui.entry)), sourcefile: 'minion-hydrate.js' } } : { entryPoints: [resolve(options.ui.entry)] };
      const browser = await build({ ...browserEntry, metafile: true, outfile: resolve(temporary, 'public/ui.js'), bundle: true, platform: 'browser', format: 'esm', target: 'es2022', jsx: 'automatic', jsxImportSource: 'react', define: { 'process.env.NODE_ENV': '"production"' }, plugins: [browserOnly, ownPackage], logLevel: 'silent' });
      // Catch transitive value imports as well as direct imports; type-only imports are erased.
      const serverFiles = await Promise.all([entry, ...(options.ui.load ? [options.ui.load] : [])].map(file => realpath(file)));
      if (Object.keys(browser.metafile.inputs).some(file => serverFiles.includes(resolve(file)))) throw new Error('Server imports cannot enter the UI; use type-only app and loader imports');
      const css = await access(resolve(temporary, 'public/ui.css')).then(() => '<link rel="stylesheet" href="/ui.css">', () => '');
      await writeFile(resolve(temporary, 'public/index.html'), `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${html(options.ui.title ?? options.name)}</title>${css}</head><body><div id="app"></div><script type="module" src="/ui.js"></script></body></html>`);
    }
    // Source config readers distinguish stored references from literal secret values by their prefix.
    if (project) await writeFile(resolve(temporary, '.hostlier'), JSON.stringify({ ...project, secrets: Object.fromEntries(Object.entries(project.secrets).map(([key, value]) => [key, 'secret://' + value])) }, null, 2) + '\n');
    await writeFile(resolve(temporary, 'Dockerfile'), `FROM ${baseImage}\nWORKDIR /app\nCOPY package.json package-lock.json ./\nRUN npm ci --omit=dev --ignore-scripts\nCOPY --chown=node:node server.mjs engine.proto ./\n${options.ui ? 'COPY --chown=node:node public ./public\n' : ''}USER node\nEXPOSE 8080\nCMD ["node", "server.mjs"]\n`);
    await writeFile(resolve(temporary, '.dockerignore'), '*\n!package.json\n!package-lock.json\n!server.mjs\n!engine.proto\n!public\n!public/**\n');
    // Refuse replacing any existing build so a failed rebuild cannot destroy reviewable output.
    await mkdir(destination);
    try { await rename(temporary, resolve(destination, 'artifact')); }
    catch (error) { await rm(destination, { recursive: true, force: true }); throw error; }
    return { directory: resolve(destination, 'artifact'), project, ...(manifest ? { manifest, digest } : {}) };
  } finally { await rm(temporary, { recursive: true, force: true }); }
}
