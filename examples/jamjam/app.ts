import { buildMinion, defineAction } from '@fused/minion';
import * as z from 'zod/mini';

export default buildMinion({
  actions: {
    greet: defineAction({
      access: 'public',
      readOnly: true,
      input: z.object({ name: z.string() }),
      output: z.string(),
      execute: ({ input }) => 'Hello, ' + input.name + '!',
    }),
  },
});
