#!/usr/bin/env node
import { parseArgs } from 'node:util';
import { readFile } from 'node:fs/promises';
import { resolve, dirname } from 'node:path';
import { buildMinionArtifact, prepareServices, writePreparedServices, type BuildOptions } from './build.js';
import { resolveBuildConfig } from './build-config.js';
import { startMinionDev } from './dev.js';

/** Run the app locally until the terminal or parent Hostlier process requests shutdown. */
async function dev(file: string, extra: string[]): Promise<void> {
  let port: number | undefined;
  // Only a literal port is forwarded; local development never opens a public listener.
  if (extra.length) {
    if (extra.length !== 2 || extra[0] !== '--port' || !/^\d+$/.test(extra[1] ?? '')) throw new Error('Usage: minion dev minion.json [--port PORT]');
    port = Number(extra[1]);
  }
  const abort = new AbortController();
  const stop = () => abort.abort();
  process.once('SIGINT', stop); process.once('SIGTERM', stop);
  try {
    const server = await startMinionDev(file, {
      port, signal: abort.signal, stdout: process.stdout, stderr: process.stderr,
      onEvent: event => {
        // Readiness output gives one exact loopback origin without exposing runtime credentials.
        if (event.type === 'ready') process.stdout.write(`Minion dev ready: ${event.url}\n`);
        else if (event.type === 'rebuilt') process.stdout.write(`Minion rebuilt: ${event.url}\n`);
        else if (event.type === 'build-error') process.stderr.write(`Minion rebuild failed; previous app retained. ${event.message}\n`);
      },
    });
    if (!abort.signal.aborted) await new Promise<void>(accept => abort.signal.addEventListener('abort', () => accept(), { once: true }));
    await server.close();
  } finally { process.removeListener('SIGINT', stop); process.removeListener('SIGTERM', stop); }
}

/** Keep building local and deterministic; deployment remains Hostlier's reviewed operation. */
async function main(): Promise<void> {
  const [command, file, ...extra] = process.argv.slice(2);
  // Preparation and local development share the same selected service configuration.
  if (!['build', 'prepare', 'dev'].includes(command ?? '') || !file) throw new Error('Usage: minion <prepare|build|dev> minion.json');
  const path = resolve(file);
  if (command === 'dev') return dev(path, extra);
  if (command !== 'build' && extra.length) throw new Error('Usage: minion <prepare|build> minion.json');
  const config = JSON.parse(await readFile(path, 'utf8')) as Omit<BuildOptions, 'outdir'> & { outdir?: string };
  const root = dirname(path);
  const typesDirectory = resolve(root, config.typesDirectory ?? '.minion');
  if (command === 'prepare') {
    // Legacy hand-authored bindings have no generated contract to prepare.
    if (!config.fused || !('services' in config.fused) || !config.fused.services) throw new Error('Configure fused.services before preparing native bindings');
    const prepared = await prepareServices(config.name, { ...config.fused, services: config.fused.services });
    await writePreparedServices(typesDirectory, prepared);
    process.stdout.write(JSON.stringify({ typesDirectory, contractDigest: prepared.lock.digest }) + '\n');
    return;
  }
  // Build protocol v1: callers supply deployment inputs without rewriting the application's config.
  const { values } = parseArgs({ args: extra, options: { outdir: { type: 'string' }, architecture: { type: 'string' }, route: { type: 'string' } } });
  if (values.architecture !== undefined && values.architecture !== 'arm64' && values.architecture !== 'amd64') throw new Error('Use amd64 or arm64 for the target architecture');
  const buildConfig = await resolveBuildConfig(root, {
    ...config,
    architecture: values.architecture ?? config.architecture,
    route: values.route ?? config.route,
    outdir: values.outdir ?? config.outdir,
  });
  const result = await buildMinionArtifact(buildConfig);
  process.stdout.write(JSON.stringify(result, null, 2) + '\n');
}
main().catch(error => { process.stderr.write((error instanceof Error ? error.message : 'Build failed') + '\n'); process.exitCode = 1; });
