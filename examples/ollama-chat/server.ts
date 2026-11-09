import { createMinionServer } from '@fused/minion/server';
import { fileURLToPath } from 'node:url';
import app from './app.js';

if (!process.env.AGENT_ACCESS_TOKEN) throw new Error('Agent credential is required');
const server = createMinionServer(app, {
  origin: process.env.MINION_ORIGIN ?? 'http://127.0.0.1:4317',
  uiDirectory: fileURLToPath(new URL('./public/', import.meta.url)),
  actionTimeoutMs: 125000, maxConcurrentActions: 4, maxBodyBytes: 196608,
  // This app uses direct HTTPS, so no Fused integration transport is configured.
  transport: { async execute() { throw new Error('No Engine operations configured'); } },
});
server.listen(Number(process.env.PORT ?? 4317), '0.0.0.0');
let closing = false;
function shutdown() {
  if (closing) return;
  closing = true;
  const timeout = setTimeout(() => process.exit(1), 10000);
  timeout.unref();
  server.close(() => clearTimeout(timeout));
}
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);
