import type { ActionClient } from '@fused/minion/client';
import type app from './app.js';

export default async function load(actions: ActionClient<typeof app.actions>) {
  return { greeting: await actions.greet({ name: 'Minion' }) };
}
