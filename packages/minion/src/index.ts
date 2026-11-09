import { isServiceIdentity } from './service-identity.js';
import { createServicesRegistry } from './services.js';
import type { IncomingMessage } from 'node:http';
import * as z from 'zod/mini';
import { currentScope, type OperationOptions, type Principal } from './context.js';
export type { Principal, OperationOptions, Selector, Transport } from './context.js';

/** Minion's generated declaration file augments this interface with the pinned provider contracts. */
export interface ServiceContracts {}
const nativeServices = createServicesRegistry<ServiceContracts>();
/** Native provider methods are generated from this Minion's selected Fused operations. */
export const services = nativeServices.services;
/** @internal Generated startup installs the selected bindings before importing application code. */
export const registerServices = nativeServices.registerServices;

export interface ActionContext<I> { input: I; user: Principal | null; signal: AbortSignal }
export interface Action<I extends z.ZodMiniType = z.ZodMiniType, O extends z.ZodMiniType = z.ZodMiniType> {
  input: I; output: O; access?: 'public' | 'authenticated';
  /** Safe to call when rendering a GET page (including browser prefetches). */
  readOnly?: boolean;
  authorize?: (user: Principal | null, input: z.output<I>) => boolean | Promise<boolean>;
  execute(context: ActionContext<z.output<I>>): z.input<O> | Promise<z.input<O>>;
}
export type Actions = Record<string, Action<any, any>>;
export interface Minion<A extends Actions = Actions> {
  actions: A;
  authenticate?: (request: IncomingMessage) => Principal | null | Promise<Principal | null>;
}

/** Preserve schema inference for each action independently of the surrounding action map. */
export function defineAction<I extends z.ZodMiniType, O extends z.ZodMiniType>(action: Action<I, O>): Action<I, O> {
  if (action.access !== undefined && !['public', 'authenticated'].includes(action.access)) throw new Error('Invalid action access');
  return Object.freeze({ ...action });
}

/** Capture declarations without opening listeners, connecting to Engine, or running actions. */
export function buildMinion<A extends Actions>(app: Minion<A>): Minion<A> {
  const names = Object.keys(app.actions);
  if (!names.length || names.length > 100) throw new Error('Declare 1 to 100 actions');
  for (const name of names) {
    if (!/^[A-Za-z][A-Za-z0-9_]{0,63}$/.test(name) || ['constructor', 'then', 'prototype', '__proto__'].includes(name)) throw new Error('Invalid action name');
  }
  return Object.freeze({ ...app, actions: Object.freeze({ ...app.actions }) });
}

export interface Operation<I extends z.ZodMiniType = z.ZodMiniType, O extends z.ZodMiniType = z.ZodMiniType> {
  /** Exact endpoint_name from the published Engine SDK contract, not an inferred service prefix. */
  endpoint: string; input: I; output: O;
}
export type OperationMap = Record<string, Record<string, Operation<any, any>>>;
export interface ServiceDefinition<O extends Record<string, Operation<any, any>>> {
  service: string;
  version: string;
  operations: O;
}
type ServiceMap = Record<string, Record<string, Operation<any, any>> | ServiceDefinition<any>>;
type ServiceOperations<S> = S extends ServiceDefinition<infer O> ? O : S;
type DefinedServices<D extends ServiceMap> = Services<{ [S in keyof D]: ServiceOperations<D[S]> extends Record<string, Operation<any, any>> ? ServiceOperations<D[S]> : never }>;

/** Declare provider identity once; the compiler derives permission selections from its operations. */
export function defineService<O extends Record<string, Operation<any, any>>>(definition: ServiceDefinition<O>): ServiceDefinition<O> {
  if (!isServiceIdentity(definition.service) || !definition.version || /[\x00-\x20]/.test(definition.version)) throw new Error('An exact Fused service and version are required');
  return Object.freeze({ ...definition, operations: Object.freeze({ ...definition.operations }) });
}
export type Services<D extends OperationMap> = { readonly [S in keyof D]: { readonly [K in keyof D[S]]:
  (input: z.input<D[S][K]['input']>, options?: OperationOptions) => Promise<z.output<D[S][K]['output']>> } };

/** Retain input/output inference while binding an exact published operation identity. */
export function defineOperation<I extends z.ZodMiniType, O extends z.ZodMiniType>(operation: Operation<I, O>): Operation<I, O> {
  if (!operation.endpoint || operation.endpoint.length > 512 || /[\x00-\x20]/.test(operation.endpoint)) throw new Error('An exact Engine operation is required');
  return Object.freeze({ ...operation });
}

/** Build Unified App-style methods over the request-local authenticated transport. */
export function defineServices<D extends ServiceMap>(definitions: D): DefinedServices<D> {
  const services = Object.create(null);
  for (const [service, definition] of Object.entries(definitions)) {
    const operations: Record<string, Operation<any, any>> = typeof definition.service === 'string' ? (definition as ServiceDefinition<any>).operations : definition;
    const methods = Object.create(null);
    for (const [name, value] of Object.entries(operations)) {
      const operation = defineOperation(value);
      methods[name] = async (input: unknown, options: OperationOptions = {}) => {
        const current = currentScope();
        const ref = current.user?.serviceUserRefs?.[service] ?? current.user?.userRef;
        // An authenticated user's binding cannot be overridden by action arguments or another request.
        if (ref && options.selector?.endUserRef && options.selector.endUserRef !== ref) throw new Error('Connected-user reference conflicts with authenticated user');
        const selector = { ...options.selector, ...(ref ? { endUserRef: ref } : {}) };
        const result = await current.transport.execute({ ...options, selector, endpoint: operation.endpoint, input: await z.parseAsync(operation.input, input), signal: current.signal });
        return z.parseAsync(operation.output, result);
      };
    }
    services[service] = Object.freeze(methods);
  }
  return Object.freeze(services);
}
