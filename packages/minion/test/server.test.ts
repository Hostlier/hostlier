import test from 'node:test';
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { setTimeout as delay } from 'node:timers/promises';
import * as z from 'zod/mini';
import { buildMinion, defineAction, defineServices, defineOperation } from '../src/index.js';
import { createMinionServer, type ServerOptions } from '../src/server.js';
import { createActionClient, ActionError } from '../src/client.js';
import type { ExecutionRequest } from '../src/context.js';
const services = defineServices({ crm: { customer: defineOperation({ endpoint: 'customer', input: z.object({ name: z.string() }), output: z.object({ user: z.string() }) }) } });

/** Start a disposable real HTTP listener with no production services. */
async function listen(t: test.TestContext, app: Parameters<typeof createMinionServer>[0], options: Partial<ServerOptions> = {}) {
  const server = createMinionServer(app, { origin: 'https://app.example', transport: { execute: async () => ({}) }, ...options });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => new Promise<void>(resolve => { server.closeAllConnections(); server.close(() => resolve()); }));
  return `http://127.0.0.1:${(server.address() as { port: number }).port}`;
}
/** Send JSON action requests with explicitly supplied fixture authentication. */
function post(base: string, name: string, input: unknown, headers: Record<string, string> = {}) {
  return fetch(`${base}/api/actions/${name}`, { method: 'POST', headers: { 'Content-Type': 'application/json', ...headers }, body: JSON.stringify(input) });
}

test('concurrent actions retain independent connected-user identities', async t => {
  const requests: ExecutionRequest[] = [];
  const app = buildMinion({ authenticate(request) { const id = request.headers.authorization; return id ? { id, userRef: id } : null; }, actions: {
    customer: defineAction({ input: z.object({ name: z.string() }), output: z.object({ user: z.string() }), async execute({ input }) { await delay(input.name === 'slow' ? 25 : 1); return services.crm.customer(input); } }),
  } });
  const base = await listen(t, app, { transport: { async execute(request) { requests.push(request); return { user: request.selector!.endUserRef }; } } });
  const clients = ['alice', 'bob'].map(id => createActionClient<typeof app>({ baseUrl: base, headers: () => ({ Authorization: id }) }));
  assert.deepEqual(await Promise.all([clients[0]!.customer({ name: 'slow' }), clients[1]!.customer({ name: 'fast' })]), [{ user: 'alice' }, { user: 'bob' }]);
  assert.equal(requests.length, 2);
  await assert.rejects(services.crm.customer({ name: 'outside' }), /active Minion action/);
});

test('authentication, authorization, validation and origin checks precede effects', async t => {
  let executions = 0;
  const app = buildMinion({ authenticate(request) { return request.headers.authorization ? { id: 'alice' } : null; }, actions: {
    save: defineAction({ input: z.object({ name: z.string() }), output: z.string(), authorize: (_user, input) => input.name !== 'denied', execute: ({ input }) => { executions++; return input.name; } }),
  } });
  const base = await listen(t, app);
  assert.equal((await post(base, 'save', { name: 'a' })).status, 401);
  assert.equal((await post(base, 'save', { name: 1 }, { Authorization: 'fixture' })).status, 400);
  assert.equal((await post(base, 'save', { name: 'denied' }, { Authorization: 'fixture' })).status, 403);
  assert.equal((await post(base, 'save', { name: 'a' }, { Authorization: 'fixture', Origin: 'https://evil.example' })).status, 403);
  assert.equal((await post(base, '__proto__', {})).status, 404);
  assert.equal(executions, 0);
  assert.equal((await post(base, 'save', { name: 'yes' }, { Authorization: 'fixture', Origin: 'https://app.example' })).status, 200);
});

test('public actions are explicit and backend/output errors are redacted', async t => {
  const app = buildMinion({ actions: {
    broken: defineAction({ access: 'public', input: z.object({}), output: z.string(), execute: () => { throw new Error('private-token-123'); } }),
    invalid: defineAction({ access: 'public', input: z.object({}), output: z.string(), execute: () => 42 as unknown as string }),
  } });
  const base = await listen(t, app);
  await assert.rejects(createActionClient<typeof app>({ baseUrl: base }).broken({}), error => error instanceof ActionError && error.code === 'ACTION_FAILED');
  const response = await post(base, 'invalid', {});
  assert.equal(response.status, 500);
  assert.equal(await response.text(), '{"error":{"code":"ACTION_FAILED"}}');
});

test('deadlines cancel work and oversized bodies are rejected', async t => {
  let signal: AbortSignal | undefined;
  const app = buildMinion({ actions: { slow: defineAction({ access: 'public', input: z.object({}), output: z.string(), async execute(context) { signal = context.signal; await delay(40); return 'late'; } }) } });
  const base = await listen(t, app, { actionTimeoutMs: 15, maxBodyBytes: 100 });
  assert.equal((await post(base, 'slow', {})).status, 504);
  assert.equal(signal?.aborted, true);
  assert.equal((await post(base, 'slow', { huge: 'x'.repeat(500) })).status, 413);
});

test('authenticated connected-user overrides never reach the transport', async t => {
  let called = false;
  const app = buildMinion({ authenticate: () => ({ id: 'alice', userRef: 'alice' }), actions: { wrong: defineAction({ input: z.object({}), output: z.object({ user: z.string() }), execute: () => services.crm.customer({ name: 'x' }, { selector: { endUserRef: 'bob' } }) }) } });
  const base = await listen(t, app, { transport: { async execute() { called = true; return { user: 'bob' }; } } });
  assert.equal((await post(base, 'wrong', {})).status, 500); assert.equal(called, false);
});

test('anonymous metadata lists only explicitly public actions without authorization hooks', async t => {
  const baseAction = { input: z.object({}), output: z.string(), execute: () => 'ok' };
  const app = buildMinion({ actions: {
    greet: defineAction({ ...baseAction, access: 'public' }),
    secret: defineAction(baseAction),
    restricted: defineAction({ ...baseAction, access: 'public', authorize: () => false }),
  } });
  const base = await listen(t, app, { name: 'hello', description: 'A greeting app' });
  const response = await fetch(base + '/');
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { name: 'hello', description: 'A greeting app', actions: [{ name: 'greet', method: 'POST', path: '/api/actions/greet' }] });
});

test('page reads share dispatch permissions and isolate each visitor’s connected-user context', async t => {
  const app = buildMinion({ authenticate(req) { const id = req.headers.authorization; return id ? { id, userRef: id } : null; }, actions: {
    customer: defineAction({ readOnly: true, input: z.object({ name: z.string() }), output: z.object({ user: z.string() }),
      authorize: user => user?.id !== 'denied', execute: ({ input }) => services.crm.customer(input) }),
    write: defineAction({ access: 'public', input: z.object({}), output: z.string(), execute() { throw Error('must not execute'); } }),
  } });
  let reads = 0;
  const base = await listen(t, app, { uiDirectory: '.', renderPage: async actions => JSON.stringify(await actions.customer!({ name: 'read' })),
    transport: { async execute(call) { reads++; await delay(2); return { user: call.selector!.endUserRef }; } } });
  for (const [authorization, status] of [['', 401], ['denied', 403]] as const) {
    assert.equal((await fetch(base, { headers: { authorization } })).status, status);
    assert.equal((await post(base, 'customer', { name: 'read' }, { authorization })).status, status);
  }
  await fetch(base, { method: 'HEAD', headers: { authorization: 'alice' } });
  assert.equal(reads, 0);
  await Promise.all(['alice', 'bob'].map(async id => {
    const res = await fetch(base, { headers: { authorization: id } });
    assert.equal(res.headers.get('cache-control'), 'private, no-store');
    assert.deepEqual(await res.json(), { user: id });
  }));
  const write = await listen(t, app, { uiDirectory: '.', renderPage: async actions => actions.write!({}) });
  const denied = await fetch(write + '/index.html');
  assert.equal(denied.status, 405);
  assert.deepEqual(await denied.json(), { error: { code: 'READ_ONLY_REQUIRED' } });
});
