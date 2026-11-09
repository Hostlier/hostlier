import { AsyncLocalStorage } from 'node:async_hooks';

import type { Transport } from '@fused/runtime';
export type { Selector, OperationOptions, ExecutionRequest, Transport } from '@fused/runtime';
export interface Principal { id: string; userRef?: string; serviceUserRefs?: Readonly<Record<string, string>> }
export interface ActionScope { transport: Transport; user: Principal | null; signal: AbortSignal }
export const scope = new AsyncLocalStorage<ActionScope>();

/** Require an admitted action so imports and browser code cannot execute integrations. */
export function currentScope(): ActionScope {
  const current = scope.getStore();
  if (!current) throw new Error('Fused calls require an active Minion action');
  return current;
}
