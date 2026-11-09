import { test } from 'node:test';
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { createMinionServer } from '@fused/minion/server';
import { createActionClient } from '@fused/minion/client';
import { createChatApp } from './app.js';

const agentToken = 'synthetic-upstream-secret';
async function fixture(send: typeof fetch) {
  const server = createMinionServer(createChatApp({ agentToken, agentUrl: 'https://agent.example', fetch: send }), {
    origin: 'http://127.0.0.1', transport: { async execute() { throw new Error('unused'); } }, actionTimeoutMs: 5000,
  });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const address = server.address(); assert.ok(address && typeof address !== 'string');
  const baseUrl = `http://127.0.0.1:${address.port}`;
  return { server, baseUrl, client: createActionClient<ReturnType<typeof createChatApp>>({ baseUrl }),
    async close() { server.closeAllConnections(); await new Promise<void>(resolve => server.close(() => resolve())); } };
}
const complete = () => Response.json({ status: 'completed', outputText: '17 × 23 = 391', sessionId: 'session_1', usage: { totalTokens: 90 }, output: [
  { type: 'tool_call', id: 'c1', name: 'calculate', arguments: { left: 17, operator: 'multiply', right: 23 } },
  { type: 'tool_result', callId: 'c1', output: { result: 391 } },
] });

test('schema and origin checks still protect public actions', async () => {
  let calls = 0;
  const f = await fixture(async () => { calls++; return complete(); });
  try {
    const headers = { 'Content-Type': 'application/json' };
    const invalid = await fetch(`${f.baseUrl}/api/actions/chat`, { method: 'POST', headers, body: JSON.stringify({ prompt: 'x'.repeat(6001) }) });
    assert.equal(invalid.status, 400);
    const foreign = await fetch(`${f.baseUrl}/api/actions/chat`, { method: 'POST', headers: { ...headers, Origin: 'https://untrusted.example' }, body: JSON.stringify({ prompt: 'hello' }) });
    assert.equal(foreign.status, 403); assert.equal(calls, 0);
  } finally { await f.close(); }
});

test('anonymous typed action forwards only the server agent token and returns tool metadata', async () => {
  const f = await fixture(async (url, options) => {
    assert.equal(String(url), 'https://agent.example/responses');
    assert.equal(new Headers(options?.headers).get('authorization'), `Bearer ${agentToken}`);
    assert.deepEqual(JSON.parse(options?.body as string), { input: 'multiply', stream: false, sessionId: 'session_1' });
    assert.equal(options?.redirect, 'error'); return complete();
  });
  try {
    assert.deepEqual(await f.client.connect({}), { name: 'Ollama assistant' });
    const result = await f.client.chat({ prompt: 'multiply', sessionId: 'session_1' });
    assert.equal(result.ok, true); assert.equal(result.text, '17 × 23 = 391');
    assert.equal(result.tools[0]?.result, '{"result":391}'); assert.equal(result.totalTokens, 90);
    assert.equal(JSON.stringify(result).includes(agentToken), false);
  } finally { await f.close(); }
});

test('upstream errors are redacted and requests are never automatically replayed', async () => {
  let calls = 0;
  const f = await fixture(async () => { calls++; return new Response(`private diagnostic ${agentToken}`, { status: 500 }); });
  try { const result = await f.client.chat({ prompt: 'hello' }); assert.equal(result.error, 'AGENT_UNAVAILABLE'); assert.equal(JSON.stringify(result).includes(agentToken), false); assert.equal(calls, 1); }
  finally { await f.close(); }
});

test('overlapping inference is bounded and cancelling reaches the upstream signal', async () => {
  let started!: () => void;
  let cancelled!: () => void;
  const entered = new Promise<void>(resolve => { started = resolve; });
  const stopped = new Promise<void>(resolve => { cancelled = resolve; });
  const f = await fixture(async (_url, options) => {
    started();
    await new Promise<void>((_resolve, reject) => options?.signal?.addEventListener('abort', () => { cancelled(); reject(new Error('aborted')); }, { once: true }));
    return complete();
  });
  try {
    const abort = new AbortController();
    const request = f.client.chat({ prompt: 'first' }, { signal: abort.signal }).catch(() => undefined);
    await entered;
    assert.equal((await f.client.chat({ prompt: 'second' })).error, 'BUSY');
    abort.abort(); await request;
    await Promise.race([stopped, new Promise((_, reject) => { const timer = setTimeout(() => reject(new Error('upstream not cancelled')), 2000); timer.unref(); })]);
  } finally { await f.close(); }
});

test('oversized upstream output is bounded', async () => {
  const f = await fixture(async () => new Response('x'.repeat(524289)));
  try { assert.equal((await f.client.chat({ prompt: 'hello' })).error, 'INVALID_RESPONSE'); }
  finally { await f.close(); }
});

test('turn-only agents receive bounded caller-provided history for follow-ups', async () => {
  const f = await fixture(async (_url, options) => {
    const body = JSON.parse(options?.body as string);
    assert.ok(body.input.includes('391'));
    assert.ok(body.input.endsWith('Add 9 to that result.'));
    assert.ok(Buffer.byteLength(options?.body as string) <= 15000);
    return complete();
  });
  try { assert.equal((await f.client.chat({ prompt: 'Add 9 to that result.', history: [{ role: 'assistant', text: 'The result is 391.' }] })).ok, true); }
  finally { await f.close(); }
});
