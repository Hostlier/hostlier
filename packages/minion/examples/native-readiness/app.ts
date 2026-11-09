import * as z from 'zod/mini';
import { buildMinion, defineAction, services } from '@fused/minion';

export default buildMinion({ actions: {
  status: defineAction({
    // Anonymous access is intentional only for this explicitly synthetic readiness fixture.
    access: 'public',
    input: z.object({}),
    output: z.object({ status: z.string(), synthetic: z.boolean() }),
    /** Use the native generated method inside the request-local Minion action scope. */
    execute: () => services.probe.ReadStatus({}),
  }),
} });
