import { timingSafeEqual } from 'node:crypto';
import * as z from 'zod/mini';
import { buildMinion, defineAction, defineServices, defineOperation } from '@fused/minion';

// Match these schemas and the exact endpoint to the selected, published Fused SDK operation.
const services = defineServices({ crm: { createCustomer: defineOperation({
  endpoint: 'createCustomer',
  input: z.object({ name: z.string().check(z.minLength(1), z.maxLength(100)) }),
  output: z.object({ id: z.string() }),
}) } });

export default buildMinion({
  /** This single-operator example uses a separate app access token, never the Engine execution token. */
  authenticate(request) {
    const expected = process.env.CUSTOMER_APP_ACCESS_TOKEN;
    const supplied = request.headers.authorization?.replace(/^Bearer /, '');
    if (!expected || !supplied || Buffer.byteLength(expected) !== Buffer.byteLength(supplied)) return null;
    if (!timingSafeEqual(Buffer.from(expected), Buffer.from(supplied))) return null;
    return { id: 'operator', userRef: 'operator' };
  },
  actions: {
    createCustomer: defineAction({
      input: z.object({ name: z.string().check(z.minLength(1), z.maxLength(100)) }),
      output: z.object({ customerId: z.string() }),
      /** Run application logic locally while Fused executes the selected provider operation over gRPC. */
      async execute({ input }) {
        const customer = await services.crm.createCustomer(input);
        return { customerId: customer.id };
      },
    }),
  },
});
