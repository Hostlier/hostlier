import { currentScope, type OperationOptions } from './context.js';

/** Generated declarations specialize only operations selected for this Minion release. */
export type NativeServices<C> = { readonly [S in keyof C]: { readonly [K in keyof C[S]]:
  C[S][K] extends { input: infer I; output: infer O }
    ? (input: I, options?: OperationOptions) => Promise<O> : never } };
export type ServiceBindings = Readonly<Record<string, Readonly<Record<string, { readonly endpoint: string }>>>>;
type Method = (input: unknown, options?: OperationOptions) => Promise<unknown>;

/** Exclude promise/prototype hooks so selected names stay ordinary callable properties. */
function validName(value: string): boolean {
  return !!value && value.length <= 512 && !/[\x00-\x20]/.test(value) && !['then', '__proto__', 'constructor', 'prototype'].includes(value);
}

/** Bind a selected endpoint to the authenticated action without embedding deployment credentials. */
function method(alias: string, endpoint: string): Method {
  return async (input, options = {}) => {
    const current = currentScope();
    const ref = current.user?.serviceUserRefs?.[alias] ?? current.user?.userRef;
    // A service-specific authenticated binding takes precedence over caller-supplied routing.
    if (ref && options.selector?.endUserRef && options.selector.endUserRef !== ref) throw new Error('Connected-user reference conflicts with authenticated user');
    return current.transport.execute({ ...options, endpoint, input,
      selector: { ...options.selector, ...(ref ? { endUserRef: ref } : {}) }, signal: current.signal });
  };
}

/** Create one immutable selected-service surface per generated application runtime. */
export function createServicesRegistry<C>(): { services: NativeServices<C>; registerServices(bindings: ServiceBindings): void } {
  const target: Record<string, Readonly<Record<string, Method>>> = Object.create(null);
  let registered = false;
  const services = new Proxy(target, {
    /** Make missing generated initialization explicit without manufacturing arbitrary methods. */
    get(object, key, receiver) {
      // Symbols are used by inspectors and do not name provider operations.
      if (typeof key === 'symbol') return Reflect.get(object, key, receiver);
      // An app may import this value freely, but selected members need generated initialization.
      if (!registered) throw new Error('Minion services are not initialized; build the app with its Fused service selection');
      return Reflect.get(object, key, receiver);
    },
    /** Application code cannot replace generated bindings before or after initialization. */
    set() { throw new Error('Minion services are immutable'); },
    /** Reject property injection even while the underlying initialization target is still empty. */
    defineProperty() { throw new Error('Minion services are immutable'); },
    /** Retain the exact generated selection for the lifetime of this runtime. */
    deleteProperty() { throw new Error('Minion services are immutable'); },
    /** Keep prototype hooks out of the selected operation namespace. */
    setPrototypeOf() { throw new Error('Minion services are immutable'); },
  }) as NativeServices<C>;

  /** Validate the entire generated map before publishing any methods to application code. */
  function registerServices(bindings: ServiceBindings): void {
    // Reinitialization could mix different Minion contracts in one process.
    if (registered) throw new Error('Minion services are already initialized');
    // Bounded plain maps are generated artifacts, not dynamic provider lookup APIs.
    if (!bindings || typeof bindings !== 'object' || Array.isArray(bindings)) throw new Error('Invalid generated Minion service bindings');
    const entries = Object.entries(bindings);
    // Mirror compiler bounds before allocating executable wrappers.
    if (!entries.length || entries.length > 64) throw new Error('Declare 1 to 64 generated Minion services');
    const next: typeof target = Object.create(null);
    let count = 0;
    for (const [alias, operations] of entries) {
      // Reject names that affect promise resolution or prototype behavior.
      if (!validName(alias) || !operations || typeof operations !== 'object' || Array.isArray(operations)) throw new Error('Invalid generated Minion service');
      const methods: Record<string, Method> = Object.create(null);
      const selected = Object.entries(operations);
      // Empty services are not meaningful consent selections.
      if (!selected.length) throw new Error('Generated Minion service requires an operation');
      for (const [name, binding] of selected) {
        // Generated endpoint identity must be explicit; never derive it from an alias.
        if (!validName(name) || !binding || typeof binding.endpoint !== 'string' || !validName(binding.endpoint) || binding.endpoint === '*') throw new Error('Invalid generated Minion operation');
        count++;
        // Bound total wrappers independently of the number of service groups.
        if (count > 1000) throw new Error('Generated Minion service bindings exceed 1000 operations');
        methods[name] = method(alias, binding.endpoint);
      }
      next[alias] = Object.freeze(methods);
    }
    Object.assign(target, next);
    Object.freeze(target);
    registered = true;
  }
  return { services, registerServices };
}
