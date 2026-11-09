import { createServer, type IncomingMessage, type ServerResponse, type Server } from 'node:http';
import { readFile, realpath } from 'node:fs/promises';
import { resolve, sep, extname } from 'node:path';
import * as z from 'zod/mini';
import { bindActions, ActionError, type ActionClient } from './client.js';
import type { Actions, Minion } from './index.js';
import { scope, type Transport, type Principal } from './context.js';

const pageCsp = "default-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'";
class DispatchError extends ActionError {}

export interface ServerOptions {
  transport: Transport;
  /** Public browser origin is explicit; forwarded Host headers are never trusted. */
  origin: string;
  name?: string;
  description?: string;
  uiDirectory?: string;
  renderPage?: (actions: ActionClient<Actions>) => Promise<string>;
  maxBodyBytes?: number;
  maxConcurrentActions?: number;
  actionTimeoutMs?: number;
  onError?: (error: unknown, action: string) => void;
}

/** Return only intentionally public JSON; provider errors never cross the action boundary. */
function json(response: ServerResponse, status: number, body: unknown): void {
  if (response.destroyed || response.writableEnded) return;
  response.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' });
  response.end(JSON.stringify(body));
}

/** Bound memory independently of Content-Length and stop reading an oversized body. */
async function readBody(request: IncomingMessage, maximum: number): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of request.iterator({ destroyOnReturn: false })) {
    size += chunk.length;
    if (size > maximum) throw new Error('BODY_TOO_LARGE');
    chunks.push(Buffer.from(chunk));
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

/** Copy authentication results so application-owned mutable objects cannot change an active request. */
function principal(user: Principal | null): Principal | null {
  if (!user) return null;
  if (typeof user.id !== 'string' || !user.id || user.id.length > 512) throw new Error('Invalid authenticated principal');
  const refs = [user.userRef, ...Object.values(user.serviceUserRefs ?? {})];
  if (refs.some(ref => ref !== undefined && (typeof ref !== 'string' || !ref || ref.length > 255))) throw new Error('Invalid connected-user reference');
  return Object.freeze({ id: user.id, userRef: user.userRef, serviceUserRefs: user.serviceUserRefs ? Object.freeze({ ...user.serviceUserRefs }) : undefined });
}

/** Restrict browser submissions to the configured origin while permitting authenticated non-browser clients. */
function sameOrigin(request: IncomingMessage, origin: string): boolean {
  return (!request.headers.origin || request.headers.origin === origin) && request.headers['sec-fetch-site'] !== 'cross-site';
}

/** Serve only files inside the explicit public directory; symlinks cannot escape it. */
async function serveUI(path: string, directory: string, request: IncomingMessage, response: ServerResponse): Promise<void> {
  try {
    const decoded = decodeURIComponent(path);
    if (decoded.includes('\0') || decoded.includes('\\')) throw new Error('Invalid path');
    const root = await realpath(directory);
    const file = await realpath(resolve(root, '.' + (decoded === '/' ? '/index.html' : decoded)));
    if (!file.startsWith(root + sep)) throw new Error('Outside UI');
    const mime: Record<string, string> = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon', '.woff2': 'font/woff2' };
    if (!mime[extname(file)]) throw new Error('Unsupported public asset');
    const data = await readFile(file);
    response.writeHead(200, { 'Content-Type': mime[extname(file)]!, 'X-Content-Type-Options': 'nosniff', 'Content-Security-Policy': pageCsp, 'Cache-Control': 'no-cache' });
    response.end(request.method === 'HEAD' ? undefined : data);
  } catch { json(response, 404, { error: { code: 'NOT_FOUND' } }); }
}

/** Create a listener without starting it; integration calls always execute under AsyncLocalStorage. */
export function createMinionServer(app: Minion, options: ServerOptions): Server {
  const origin = new URL(options.origin);
  if (!['http:', 'https:'].includes(origin.protocol) || origin.origin !== options.origin) throw new Error('Supply an exact public origin');
  const maximum = options.maxBodyBytes ?? 1024 * 1024;
  const concurrency = options.maxConcurrentActions ?? 64;
  const timeout = options.actionTimeoutMs ?? 30000;
  if (!Number.isSafeInteger(maximum) || maximum < 1 || maximum > 16 * 1024 * 1024 || !Number.isSafeInteger(concurrency) || concurrency < 1 || concurrency > 10000 || !Number.isSafeInteger(timeout) || timeout < 1 || timeout > 600000) throw new Error('Invalid server limits');
  const metadata = {
    name: options.name ?? 'minion',
    ...(typeof options.description === 'string' && { description: options.description }),
    actions: Object.entries(app.actions)
      .filter(([, action]) => action.access === 'public' && !action.authorize)
      .map(([name]) => ({ name, method: 'POST', path: '/api/actions/' + name })),
  };
  let active = 0, executing = 0;

  async function invoke(name: string, input: () => Promise<unknown>, user: Principal | null, signal: AbortSignal, page: boolean) {
    if (!Object.hasOwn(app.actions, name)) throw new DispatchError('NOT_FOUND', 404);
    const action = app.actions[name]!;
    if (action.access !== 'public' && !user) throw new DispatchError('AUTHENTICATION_REQUIRED', 401);
    if (page && action.readOnly !== true) throw new DispatchError('READ_ONLY_REQUIRED', 405);
    if (executing >= concurrency) throw new DispatchError('BUSY', 503);
    executing++;
    try {
      signal.throwIfAborted();
      const parsed = await z.safeParseAsync(action.input, await input());
      if (!parsed.success) throw new DispatchError('INVALID_INPUT', 400);
      if (action.authorize && !await action.authorize(user, parsed.data)) throw new DispatchError('ACTION_DENIED', 403);
      signal.throwIfAborted();
      const result = await scope.run({ transport: options.transport, user, signal }, () => action.execute({ input: parsed.data, user, signal }));
      const body = JSON.stringify({ output: await z.parseAsync(action.output, result) });
      if (Buffer.byteLength(body) > maximum) throw new Error('Action output exceeds limit');
      return JSON.parse(body).output;
    } catch (error) {
      if (error instanceof DispatchError) throw error;
      try { options.onError?.(error, name); } catch { /* Keep diagnostics private. */ }
      throw new DispatchError('ACTION_FAILED', 500);
    } finally { executing--; }
  }
  const server = createServer({ requestTimeout: timeout, headersTimeout: Math.min(timeout, 10000), maxHeaderSize: 16384 }, (request, response) => {
    void handle(request, response).catch(() => json(response, 500, { error: { code: 'INTERNAL_ERROR' } }));
  });

  /** Route health, static assets, and actions without allowing arbitrary operation dispatch. */
  async function handle(request: IncomingMessage, response: ServerResponse): Promise<void> {
    const path = (request.url ?? '/').split('?')[0]!;
    if (path === '/healthz' && request.method === 'GET') { json(response, 200, { status: 'ready' }); return; }
    if (path === '/' && !options.uiDirectory && !options.renderPage) {
      if (request.method !== 'GET' && request.method !== 'HEAD') {
        response.setHeader('Allow', 'GET, HEAD');
        json(response, 405, { error: { code: 'METHOD_NOT_ALLOWED' } });
      } else {
        json(response, 200, metadata);
      }
      return;
    }
    const page = options.renderPage && (path === '/' || path === '/index.html') && ['GET', 'HEAD'].includes(request.method ?? '');
    if (!page && !path.startsWith('/api/actions/')) {
      if (options.uiDirectory && ['GET', 'HEAD'].includes(request.method ?? '')) await serveUI(path, options.uiDirectory, request, response);
      else json(response, 404, { error: { code: 'NOT_FOUND' } });
      return;
    }
    const name = page ? 'page' : path.slice('/api/actions/'.length);
    if (!page) {
      if (!Object.hasOwn(app.actions, name)) { json(response, 404, { error: { code: 'NOT_FOUND' } }); return; }
      if (request.method !== 'POST') { response.setHeader('Allow', 'POST'); json(response, 405, { error: { code: 'METHOD_NOT_ALLOWED' } }); return; }
      if (!sameOrigin(request, origin.origin)) { json(response, 403, { error: { code: 'ORIGIN_DENIED' } }); return; }
      if (request.headers['content-type']?.split(';')[0]?.trim() !== 'application/json') { json(response, 415, { error: { code: 'JSON_REQUIRED' } }); return; }
    }
    if (active >= concurrency) { json(response, 503, { error: { code: 'BUSY' } }); return; }
    active++;
    const abort = new AbortController();
    const timer = setTimeout(() => { abort.abort(); json(response, 504, { error: { code: 'ACTION_TIMEOUT' } }); }, timeout);
    const disconnect = () => { if (!response.writableEnded) abort.abort(); };
    response.on('close', disconnect);
    try {
      const user = principal(await app.authenticate?.(request) ?? null);
      if (abort.signal.aborted) return;
      if (page) {
        const actions = bindActions((key, input, call) => invoke(key, async () => {
          const body = JSON.stringify(input);
          if (body === undefined) throw new DispatchError('INVALID_INPUT', 400);
          if (Buffer.byteLength(body) > maximum) throw new DispatchError('INVALID_INPUT', 413);
          return JSON.parse(body);
        }, user, call.signal ? AbortSignal.any([abort.signal, call.signal]) : abort.signal, true));
        const html = request.method === 'HEAD' ? undefined : await options.renderPage!(actions);
        if (abort.signal.aborted) return;
        response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'private, no-store', 'Content-Security-Policy': pageCsp, 'X-Content-Type-Options': 'nosniff' });
        response.end(html);
      } else {
        const output = await invoke(name, async () => {
          try { return await readBody(request, maximum); }
          catch (error) { throw new DispatchError('INVALID_INPUT', error instanceof Error && error.message === 'BODY_TOO_LARGE' ? 413 : 400); }
        }, user, abort.signal, false);
        if (!abort.signal.aborted) json(response, 200, { output });
      }
    } catch (error) {
      if (error instanceof DispatchError) { json(response, error.status, { error: { code: error.code } }); return; }
      // Diagnostics are opt-in server-side callbacks; callback failures cannot expose original errors.
      try { options.onError?.(error, name); } catch { /* Preserve the public error contract. */ }
      json(response, 500, { error: { code: 'ACTION_FAILED' } });
    } finally { abort.abort(); clearTimeout(timer); response.off('close', disconnect); active--; }
  }
  return server;
}
