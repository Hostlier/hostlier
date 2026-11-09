import type * as z from 'zod/mini';
import type { Actions, Minion } from './index.js';

export type ActionClient<A extends Actions> = { [K in keyof A]: (input: z.input<A[K]['input']>, options?: { signal?: AbortSignal }) => Promise<z.output<A[K]['output']>> };
/** Infer both page props from a server loader imported as a type only. */
export type PageProps<L extends (actions: any) => unknown> = { data: Awaited<ReturnType<L>>; actions: Parameters<L>[0] };

export class ActionError extends Error {
  /** Expose a bounded action code while keeping backend and provider diagnostics private. */
  constructor(public readonly code: string, public readonly status: number) { super(code); this.name = 'ActionError'; }
}

/** Browser-only typed action calls; the app import must be type-only so no server code is bundled. */
export function createActionClient<A extends Minion<any>>(options: { baseUrl?: string; fetch?: typeof fetch; headers?: () => HeadersInit | Promise<HeadersInit> } = {}): ActionClient<A['actions']> {
  const send = options.fetch ?? globalThis.fetch;
  const base = (options.baseUrl ?? '').replace(/\/$/, '');
  return bindActions(async (name, input, call) => {
    const headers = new Headers(await options.headers?.());
    headers.set('Content-Type', 'application/json');
    const response = await send(`${base}/api/actions/${encodeURIComponent(name)}`, { method: 'POST', credentials: 'same-origin', headers, body: JSON.stringify(input), signal: call.signal });
    const result = await response.json() as { output?: unknown; error?: { code?: string } };
    if (!response.ok) throw new ActionError(result.error?.code ?? 'ACTION_FAILED', response.status);
    return result.output;
  });
}

/** Bind the same typed method names to either the browser or request-local transport. */
export function bindActions<A extends Actions>(invoke: (name: string, input: unknown, call: { signal?: AbortSignal }) => Promise<unknown>): ActionClient<A> {
  return new Proxy(Object.create(null), {
    get(_target, name) {
      if (typeof name !== 'string' || name === 'then') return undefined;
      return (input: unknown, call: { signal?: AbortSignal } = {}) => invoke(name, input, call);
    },
  }) as ActionClient<A>;
}
