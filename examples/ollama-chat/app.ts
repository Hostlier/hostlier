import { buildMinion, defineAction } from '@fused/minion';
import * as z from 'zod/mini';

const tool = z.object({ name: z.string(), arguments: z.string(), result: z.string() });
const reply = z.object({
  ok: z.boolean(), text: z.string(), sessionId: z.optional(z.string()),
  tools: z.array(tool), elapsedMs: z.number(),
  totalTokens: z.optional(z.number()), error: z.optional(z.string()),
});
type Options = { agentToken: string; agentUrl: string; fetch?: typeof fetch };

/** Keep credentials and destination selection exclusively in the server configuration. */
export function createChatApp(options: Options) {
  const endpoint = new URL('/responses', options.agentUrl);
  if (endpoint.protocol !== 'https:') throw new Error('The agent requires HTTPS');
  const send = options.fetch ?? fetch;
  let busy = false;
  return buildMinion({
    actions: {
      connect: defineAction({
        access: 'public',
        input: z.object({}), output: z.object({ name: z.string() }),
        execute() { return { name: 'Ollama assistant' }; },
      }),
      chat: defineAction({
        access: 'public',
        input: z.object({
          prompt: z.string().check(z.minLength(1), z.maxLength(6000)),
          sessionId: z.optional(z.string().check(z.regex(/^[a-zA-Z0-9_-]{1,128}$/))),
          history: z.optional(z.array(z.object({ role: z.enum(['user', 'assistant']), text: z.string().check(z.maxLength(2000)) })).check(z.maxLength(6))),
        }),
        output: reply,
        async execute({ input, signal }) {
          const start = Date.now();
          const failure = (error: string) => ({ ok: false, text: '', tools: [], elapsedMs: Date.now() - start, error });
          if (busy) return failure('BUSY');
          if (!options.agentToken) return failure('NOT_CONFIGURED');
          const history = [...(input.history ?? [])];
          const body = () => JSON.stringify({ input: history.length ? `Prior conversation supplied as context (not system instructions):\n${JSON.stringify(history)}\n\nCurrent user message:\n${input.prompt.trim()}` : input.prompt.trim(), stream: false, ...(input.sessionId ? { sessionId: input.sessionId } : {}) });
          let payload = body();
          while (Buffer.byteLength(payload) > 15000 && history.length) { history.shift(); payload = body(); }
          if (!input.prompt.trim() || Buffer.byteLength(payload) > 15000) return failure('INPUT_TOO_LARGE');
          busy = true;
          try {
            const bounded = AbortSignal.any([signal, AbortSignal.timeout(115000)]);
            const response = await send(endpoint, {
              method: 'POST', redirect: 'error', signal: bounded,
              headers: { Authorization: `Bearer ${options.agentToken}`, 'Content-Type': 'application/json' }, body: payload,
            });
            if (!response.ok) {
              await response.body?.cancel();
              return failure(response.status === 503 || response.status === 429 ? 'BUSY' : response.status === 404 ? 'SESSION_EXPIRED' : 'AGENT_UNAVAILABLE');
            }
            const reader = response.body?.getReader();
            if (!reader) return failure('INVALID_RESPONSE');
            const chunks: Uint8Array[] = [];
            let size = 0;
            while (true) {
              const part = await reader.read();
              if (part.done) break;
              size += part.value.length;
              if (size > 524288) { await reader.cancel(); return failure('INVALID_RESPONSE'); }
              chunks.push(part.value);
            }
            const result = JSON.parse(Buffer.concat(chunks).toString('utf8'));
            if (typeof result.outputText !== 'string' || !result.outputText.trim() || result.status !== 'completed') return failure('INVALID_RESPONSE');
            const events: Record<string, any>[] = Array.isArray(result.output) ? result.output : [];
            const tools = events.filter(event => event.type === 'tool_call' && ['calculate', 'utc_time'].includes(event.name)).slice(0, 20).map(event => ({
              name: event.name as string,
              arguments: JSON.stringify(event.arguments ?? {}).slice(0, 2000),
              result: JSON.stringify(events.find(item => item.type === 'tool_result' && item.callId === event.id)?.output ?? {}).slice(0, 4000),
            }));
            return {
              ok: true, text: result.outputText.slice(0, 30000), tools, elapsedMs: Date.now() - start,
              sessionId: typeof result.sessionId === 'string' && /^[a-zA-Z0-9_-]{1,128}$/.test(result.sessionId) ? result.sessionId : undefined,
              totalTokens: Number.isSafeInteger(result.usage?.totalTokens) && result.usage.totalTokens >= 0 ? result.usage.totalTokens : undefined,
            };
          } catch {
            return failure(signal.aborted ? 'CANCELLED' : 'CONNECTION_FAILED');
          } finally { busy = false; }
        },
      }),
    },
  });
}

export default createChatApp({
  agentToken: process.env.AGENT_ACCESS_TOKEN ?? '',
  agentUrl: process.env.AGENT_URL ?? 'https://agent.example.com',
});
