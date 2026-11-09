import test from 'node:test';
import assert from 'node:assert/strict';
import { setTimeout as delay } from 'node:timers/promises';
import { services, registerServices } from '../src/index.js';
import { createServicesRegistry } from '../src/services.js';
import { scope, type ExecutionRequest, type Principal } from '../src/context.js';

interface FixtureContracts {
  crm: { Lookup: { input: { id: string }; output: { user: string } } };
}
declare module '../src/index.js' {
  interface ServiceContracts extends FixtureContracts {}
}
const selected = { crm: { Lookup: { endpoint: 'GetCustomer' } } };

/** Compile-time failures prove that generated augmentation preserves operation and payload types. */
function checkGeneratedTypes(): void {
  // @ts-expect-error Unselected services are absent from the native facade.
  void services.payments;
  // @ts-expect-error Unselected operations are absent even for a selected service.
  void services.crm.Delete;
  // @ts-expect-error Provider input types are generated rather than widened to unknown.
  void services.crm.Lookup({ id: 4 });
  const result: Promise<{ user: string }> = services.crm.Lookup({ id: 'valid' });
  void result;
}
void checkGeneratedTypes;

/** Run a callback under the same request-local context created by the HTTP action server. */
function run<T>(user: Principal | null, execute: (request: ExecutionRequest) => Promise<unknown>, action: () => T, signal = new AbortController().signal): T {
  return scope.run({ user, transport: { execute }, signal }, action);
}

// The native export uses the exact same registry as generated startup rather than a permissive proxy.
test('native services require generated initialization and expose only selected frozen methods', async () => {
  assert.throws(() => services.crm, /not initialized/);
  assert.throws(() => Object.defineProperty(services, 'arbitrary', { value: {} }), /immutable/);
  registerServices(selected);
  assert.deepEqual(Object.keys(services), ['crm']);
  assert.deepEqual(Object.keys(services.crm), ['Lookup']);
  assert.equal((services.crm as unknown as Record<string, unknown>).Delete, undefined);
  assert.equal((services as unknown as Record<string, unknown>).arbitrary, undefined);
  assert.equal(Object.getPrototypeOf(services.crm), null);
  assert.throws(() => Object.assign(services, { arbitrary: {} }), /immutable/);
  assert.throws(() => Object.assign(services.crm, { Lookup: () => undefined }), TypeError);
  assert.throws(() => registerServices(selected), /already initialized/);
  await assert.rejects(services.crm.Lookup({ id: 'outside' }), /active Minion action/);
});

// AsyncLocalStorage routing must stay isolated even when one provider call yields to another user.
test('native service calls isolate concurrent users and preserve service-specific identity', async () => {
  const registry = createServicesRegistry<FixtureContracts>();
  registry.registerServices(selected);
  const requests: ExecutionRequest[] = [];
  /** Record only fixture requests and return the authenticated routing value. */
  async function execute(request: ExecutionRequest): Promise<unknown> {
    await delay((request.input as { id: string }).id === 'slow' ? 15 : 1);
    requests.push(request);
    return { user: request.selector?.endUserRef };
  }
  const results = await Promise.all([
    run({ id: 'alice', userRef: 'default-alice', serviceUserRefs: { crm: 'crm-alice' } }, execute, () => registry.services.crm.Lookup({ id: 'slow' })),
    run({ id: 'bob', userRef: 'bob' }, execute, () => registry.services.crm.Lookup({ id: 'fast' })),
  ]);
  assert.deepEqual(results, [{ user: 'crm-alice' }, { user: 'bob' }]);
  assert.equal(requests.every(request => request.endpoint === 'GetCustomer'), true);
});

// Caller options cannot replace the verified connected-user identity or action cancellation signal.
test('native calls enforce user bindings and forward selected execution options', async () => {
  const registry = createServicesRegistry<FixtureContracts>();
  registry.registerServices(selected);
  const requests: ExecutionRequest[] = [];
  const signal = new AbortController().signal;
  /** Capture fixture requests without executing a provider. */
  async function execute(request: ExecutionRequest): Promise<unknown> { requests.push(request); return { user: 'alice' }; }
  await run({ id: 'alice', userRef: 'alice' }, execute, async () => {
    await assert.rejects(registry.services.crm.Lookup({ id: 'x' }, { selector: { endUserRef: 'bob' } }), /conflicts/);
    assert.equal(requests.length, 0);
    await registry.services.crm.Lookup({ id: 'x' }, { bucketId: 'approved-bucket', pagination: { maxPages: 1 }, idempotencyKey: 'stable-key', selector: { resourceId: 'approved-resource' } });
  }, signal);
  assert.deepEqual(requests[0], { endpoint: 'GetCustomer', input: { id: 'x' }, bucketId: 'approved-bucket', pagination: { maxPages: 1 }, idempotencyKey: 'stable-key', selector: { resourceId: 'approved-resource', endUserRef: 'alice' }, signal });
});

// Validation is atomic so a bad generated selection cannot leave partially callable operations.
test('invalid generated maps do not publish partial bindings', () => {
  const registry = createServicesRegistry<FixtureContracts>();
  assert.throws(() => registry.registerServices({ crm: selected.crm, billing: { Other: { endpoint: '*' } } }), /Invalid/);
  assert.throws(() => registry.services.crm, /not initialized/);
  assert.throws(() => registry.registerServices({ crm: { All: { endpoint: '*' } } }), /Invalid/);
  assert.throws(() => registry.registerServices({ then: selected.crm }), /Invalid/);
  registry.registerServices(selected);
  assert.deepEqual(Object.keys(registry.services), ['crm']);
});

// Generated identity strings are copied into closures so later config mutation cannot redirect a call.
test('registered service identities cannot change with their source object', async () => {
  const registry = createServicesRegistry<FixtureContracts>();
  const bindings = { crm: { Lookup: { endpoint: 'GetCustomer' } } };
  registry.registerServices(bindings);
  bindings.crm.Lookup.endpoint = 'DeleteCustomer';
  let endpoint = '';
  await run(null, async request => { endpoint = request.endpoint; return { user: '' }; }, () => registry.services.crm.Lookup({ id: 'x' }));
  assert.equal(endpoint, 'GetCustomer');
});

// Two aliases may intentionally route one selected provider operation to distinct authenticated connections.
test('aliases of one selected endpoint retain their own authenticated user routing', async () => {
  const registry = createServicesRegistry<FixtureContracts & { other: FixtureContracts['crm'] }>();
  registry.registerServices({ crm: selected.crm, other: selected.crm });
  const results = await run({ id: 'operator', serviceUserRefs: { crm: 'primary', other: 'secondary' } },
    async request => ({ user: request.selector?.endUserRef }),
    () => Promise.all([registry.services.crm.Lookup({ id: 'x' }), registry.services.other.Lookup({ id: 'x' })]));
  assert.deepEqual(results, [{ user: 'primary' }, { user: 'secondary' }]);
});
