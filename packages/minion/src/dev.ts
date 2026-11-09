import { createServer, request } from 'node:http';
import { createServer as createPortProbe } from 'node:net';
import { watch, type FSWatcher } from 'node:fs';
import { readFile, mkdir, mkdtemp, rm, symlink } from 'node:fs/promises';
import { dirname, resolve, relative, sep } from 'node:path';
import { spawn, type ChildProcess } from 'node:child_process';
import { createRequire } from 'node:module';
import { setTimeout as delay } from 'node:timers/promises';
import type { Writable } from 'node:stream';
import { buildMinionArtifact, type BuildOptions } from './build.js';

import { resolveBuildConfig } from './build-config.js';

export interface DevEvent { type: 'ready' | 'rebuilt' | 'build-error' | 'closed'; url: string; message?: string }
export interface DevOptions {
  port?: number;
  signal?: AbortSignal;
  stdout?: Writable;
  stderr?: Writable;
  onEvent?: (event: DevEvent) => void;
}
export interface DevServer { url: string; close(): Promise<void> }

/** Use a private loopback port for each replacement process while the public dev URL remains stable. */
async function freePort(): Promise<number> {
  const probe = createPortProbe();
  await new Promise<void>((accept, reject) => { probe.once('error', reject); probe.listen(0, '127.0.0.1', accept); });
  const port = (probe.address() as { port: number }).port;
  await new Promise<void>((accept, reject) => probe.close(error => error ? reject(error) : accept()));
  return port;
}

/** Stop one owned child gracefully, with a bounded fallback for application code that ignores termination. */
async function stopChild(child: ChildProcess): Promise<void> {
  if (!child.pid || child.exitCode !== null || child.signalCode !== null) return;
  await new Promise<void>(accept => {
    const timer = setTimeout(() => child.kill('SIGKILL'), 3000);
    child.once('exit', () => { clearTimeout(timer); accept(); });
    child.once('error', () => { clearTimeout(timer); accept(); });
    child.kill('SIGTERM');
  });
}

/** Resolve authored paths beside minion.json; development never needs a deployment destination. */
export async function readDevConfig(file: string, outdir: string): Promise<BuildOptions> {
  const root = dirname(file);
  const config = JSON.parse(await readFile(file, 'utf8')) as Partial<BuildOptions>;
  return resolveBuildConfig(root, { name: 'minion-dev', ...config, outdir, route: undefined });
}

/** Serve a stable loopback URL while complete replacement builds run in isolated child processes. */
export async function startMinionDev(configFile: string, options: DevOptions = {}): Promise<DevServer> {
  const file = resolve(configFile), root = dirname(file), port = options.port ?? 4317;
  if (!Number.isSafeInteger(port) || port < 0 || port > 65535) throw new Error('Dev port must be an integer from 0 to 65535');
  if (options.signal?.aborted) throw new Error('Minion dev was cancelled');
  await mkdir(resolve(root, '.minion'), { recursive: true });
  const directory = await mkdtemp(resolve(root, '.minion/dev-'));
  let upstream = 0, sequence = 0, closed = false, current: ChildProcess | undefined;
  let currentDirectory: string | undefined, watcher: FSWatcher | undefined, debounce: NodeJS.Timeout | undefined;
  let activeBuild: Promise<void> | undefined, queued = false, closing: Promise<void> | undefined;
  const proxy = createServer((incoming, outgoing) => {
    // An incomplete initial build cannot receive application traffic.
    if (!upstream) { outgoing.writeHead(503); outgoing.end('Minion is starting'); return; }
    const forward = request({ hostname: '127.0.0.1', port: upstream, path: incoming.url, method: incoming.method, headers: incoming.headers }, response => {
      outgoing.writeHead(response.statusCode ?? 502, response.headers);
      response.once('error', () => outgoing.destroy());
      response.pipe(outgoing);
    });
    // Local proxy failures must not disclose child process diagnostics or retry an action.
    forward.on('error', () => { if (!outgoing.headersSent) outgoing.writeHead(502); outgoing.end('Minion app unavailable'); });
    incoming.once('aborted', () => forward.destroy());
    outgoing.once('close', () => { if (!outgoing.writableFinished) forward.destroy(); });
    incoming.pipe(forward);
  });
  let url = '';
  const emit = (type: DevEvent['type'], message?: string) => {
    // Progress observers cannot corrupt a successful process swap or interrupt cleanup.
    try { options.onEvent?.({ type, url, ...(message ? { message } : {}) }); } catch { /* Lifecycle ownership stays with the runner. */ }
  };

  /** Discard a failed replacement while retaining the last healthy application. */
  async function replace(initial: boolean): Promise<void> {
    const generation = resolve(directory, String(++sequence));
    let candidate: ChildProcess | undefined;
    try {
      const config = await readDevConfig(file, generation);
      const built = await buildMinionArtifact(config);
      if (closed) return;
      // Development reuses installed dependencies; it never installs packages on each file edit.
      const modules = dirname(dirname(createRequire(import.meta.url).resolve('zod/package.json')));
      await symlink(modules, resolve(built.directory, 'node_modules'), 'dir');
      const childPort = await freePort();
      const env: NodeJS.ProcessEnv = { ...process.env, HOST: '127.0.0.1', PORT: String(childPort), MINION_ORIGIN: url };
      // Catalog credentials belong only to preparation, never to the running application.
      delete env.FUSED_API_KEY;
      delete env.FUSED_LICENSE_KEY;
      if (config.fused) {
        env.FUSED_ENGINE_GRPC_URL ??= config.fused.grpcUrl;
        if ('appId' in config.fused) env.FUSED_APP_ID ??= config.fused.appId;
      }
      candidate = spawn(process.execPath, [resolve(built.directory, 'server.mjs')], { cwd: built.directory, env, stdio: ['ignore', 'pipe', 'pipe'] });
      candidate.stdout?.on('data', chunk => options.stdout?.write(chunk));
      candidate.stderr?.on('data', chunk => options.stderr?.write(chunk));
      let spawnFailed = false;
      candidate.on('error', () => { spawnFailed = true; });
      let healthy = false;
      const deadline = Date.now() + 15000;
      // The old generation continues serving until the new child's actual readiness endpoint succeeds.
      while (!closed && !spawnFailed && candidate.exitCode === null && candidate.signalCode === null && Date.now() < deadline) {
        try { healthy = (await fetch(`http://127.0.0.1:${childPort}/healthz`, { signal: AbortSignal.timeout(250) })).ok; } catch { /* Startup has not bound the loopback port yet. */ }
        if (healthy) break;
        await delay(40);
      }
      if (!healthy || closed) throw new Error(closed ? 'Minion dev was cancelled' : 'New Minion process did not become ready');
      const previous = current, previousDirectory = currentDirectory;
      current = candidate; candidate = undefined; currentDirectory = generation; upstream = childPort;
      emit(initial ? 'ready' : 'rebuilt');
      if (previous) await stopChild(previous);
      if (previousDirectory) await rm(previousDirectory, { recursive: true, force: true });
    } catch (error) {
      if (candidate) await stopChild(candidate);
      await rm(generation, { recursive: true, force: true });
      if (initial) throw error;
      if (!closed) emit('build-error', error instanceof Error ? error.message : 'Minion rebuild failed');
    }
  }

  /** Serialize changes so a slow build cannot race a newer config or replace a newer generation. */
  function rebuild(initial = false): Promise<void> {
    if (activeBuild) { queued = true; return activeBuild; }
    activeBuild = (async () => {
      do { queued = false; await replace(initial); initial = false; } while (queued && !closed);
    })().finally(() => { activeBuild = undefined; });
    return activeBuild;
  }

  /** Release watchers, listeners, child processes and only this dev session's generated files. */
  function close(): Promise<void> {
    if (closing) return closing;
    closed = true; watcher?.close(); clearTimeout(debounce); options.signal?.removeEventListener('abort', abort);
    closing = (async () => {
      proxy.closeAllConnections();
      await new Promise<void>(accept => proxy.close(() => accept()));
      await activeBuild?.catch(() => {});
      if (current) await stopChild(current);
      await rm(directory, { recursive: true, force: true });
      emit('closed');
    })();
    return closing;
  }
  const abort = () => { void close(); };
  options.signal?.addEventListener('abort', abort, { once: true });
  try {
    await new Promise<void>((accept, reject) => { proxy.once('error', reject); proxy.listen(port, '127.0.0.1', accept); });
    url = `http://127.0.0.1:${(proxy.address() as { port: number }).port}`;
    await rebuild(true);
    if (closed) throw new Error('Minion dev was cancelled');
    watcher = watch(root, { recursive: true }, (_event, filename) => {
      if (!filename || closed) return;
      const name = filename.toString(), parts = name.split(/[\\/]/);
      // Ignore generated files and dependency churn, but retain config, source, styles and asset edits.
      if (parts.some(part => ['.minion', '.git', 'node_modules', 'dist', '.test'].includes(part))) return;
      if (relative(root, resolve(root, name)).startsWith('..' + sep)) return;
      clearTimeout(debounce);
      debounce = setTimeout(() => { void rebuild(); }, 100);
    });
    watcher.on('error', () => { emit('build-error', 'Source watcher failed; restart minion dev'); });
    return { url, close };
  } catch (error) { await close(); throw error; }
}
