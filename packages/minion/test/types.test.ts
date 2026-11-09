import * as z from 'zod/mini';
import { buildMinion, defineAction, defineOperation, defineServices } from '../src/index.js';
import { createActionClient } from '../src/client.js';
/** Compile-only checks verify schema types survive both client and server boundaries. */
function typeContract() {
  const services = defineServices({ crm: { create: defineOperation({ endpoint: 'createCustomer', input: z.object({ name: z.string() }), output: z.object({ id: z.string() }) }) } });
  const app = buildMinion({ actions: { create: defineAction({ input: z.object({ name: z.string() }), output: z.object({ id: z.string() }), execute: ({ input }) => services.crm.create(input) }) } });
  const client = createActionClient<typeof app>();
  const result: Promise<{ id: string }> = client.create({ name: 'Ada' });
  // @ts-expect-error Browser inputs retain the authored schema.
  client.create({ name: 3 });
  // @ts-expect-error Undeclared server actions are not callable.
  client.remove({});
  // @ts-expect-error Service input schemas remain typed.
  services.crm.create({ title: 'wrong' });
  return result;
}
void typeContract;
