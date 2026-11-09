// Native gRPC only. Keep the API-specific signing transcript in controller.js;
// gRPC owns framing, TLS validation, flow control and trailer handling.
import grpc from '@grpc/grpc-js';
import loader from '@grpc/proto-loader';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';

const definition = loader.loadSync(fileURLToPath(new URL('./controller.proto', import.meta.url)));
export const Controller = grpc.loadPackageDefinition(definition).hostlier.control.v1.Controller;
const maxBytes = 4 * 1024 * 1024;

// grpc-js maps both a stream closed without status and Caddy's HTTP/2
// INTERNAL_ERROR reset to INTERNAL. Keep these distinct from controller
// errors, including when the reset interrupts signing a new watch.
function interruptedStream(status) {
  if (status.code !== grpc.status.INTERNAL) return false;
  return status.details === 'Received RST_STREAM with code 0 (Call ended without gRPC status)' ||
    status.details === 'Received RST_STREAM with code 2 (Internal server error)';
}

export function controllerRPC(origin) {
  const url = new URL(origin);
  const channel = new Controller('dns:///' + url.hostname + ':' + (url.port || (url.protocol === 'https:' ? '443' : '80')),
    url.protocol === 'https:' ? grpc.credentials.createSsl() : grpc.credentials.createInsecure(), {
      'grpc.max_receive_message_length': maxBytes + 1024, 'grpc.enable_retries': 0,
      'grpc.enable_http_proxy': 0, 'grpc.keepalive_time_ms': 30000, 'grpc.keepalive_timeout_ms': 10000,
    });
  function metadata(headers) {
    const result = new grpc.Metadata();
    for (const [name, value] of Object.entries(headers)) result.set(name, value);
    return result;
  }
  function failure(cause) {
    const code = cause.metadata?.get('hostlier-error-code')[0];
    const err = new Error(code ? cause.details : 'Hostlier controller gRPC request failed', { cause });
    // grpc-js reports TLS verification failures as UNAVAILABLE. Retrying cannot
    // repair an untrusted, expired or mismatched certificate.
    const tlsError = cause.code === grpc.status.UNAVAILABLE && /certificate|self.signed|hostname mismatch|unable to verify|wrong version number/i.test(cause.details);
    const disconnected = cause.code === grpc.status.UNAVAILABLE || interruptedStream(cause);
    err.code = code || (tlsError ? 'TLS' : disconnected ? 'CONNECTION' : 'CONTROLLER');
    return err;
  }
  async function command(method, route, jsonBody, headers, signal) {
    signal?.throwIfAborted();
    const reply = await new Promise((resolve,reject) => {
      const call = channel.command({method, route, jsonBody}, metadata(headers), { deadline: Date.now()+30_000 }, (error, result) => {
        signal?.removeEventListener('abort', abort);
        if (signal?.aborted) reject(signal.reason);
        else if (error) reject(failure(error));
        else resolve(result);
      });
      const abort = () => call.cancel();
      signal?.addEventListener('abort', abort, {once:true});
    });
    const raw = reply.jsonBody || Buffer.alloc(0);
    if (raw.length > maxBytes) throw new Error('Hostlier response exceeded 4 MiB');
    return raw.length ? JSON.parse(raw.toString('utf8')) : {};
  }
  async function watch(kind, id, route, sign, signal) {
    let cursor = '', backoff = 100;
    for (;;) {
      signal?.throwIfAborted();
      const operation = kind === 'operation';
      let call, abort;
      try {
        const headers = metadata(await sign());
        signal?.throwIfAborted();
        call = operation ? channel.watchOperation({operationId:id, cursor},headers) : channel.watchJob({route},headers);
        abort = () => call.cancel();
        signal?.addEventListener('abort', abort, {once:true});
        for await (const frame of call) {
          if (frame.kind === 'heartbeat') continue;
          const data = JSON.parse(frame.jsonData.toString('utf8'));
          if (operation && frame.kind === 'log') {
            if (data.operationId !== id || frame.cursor !== id + ':' + data.sequence) throw new Error('Invalid Hostlier log cursor');
            cursor = frame.cursor; backoff = 100; continue;
          }
          if (operation && frame.kind === 'reset') continue;
          if (frame.kind !== (operation ? 'operation' : 'receipt') || data.id !== id || typeof data.status !== 'string' || !data.status) throw new Error('Invalid Hostlier watch response');
          if (!['queued','running','pending','accepted'].includes(data.status)) return data;
        }
      } catch (cause) {
        if (signal?.aborted) throw signal.reason;
        const error = typeof cause.code === 'number' ? failure(cause) : cause;
        if (error.code !== 'CONNECTION') throw error;
      } finally {
        if (abort) signal?.removeEventListener('abort', abort);
        call?.cancel();
      }
      await delay(backoff,undefined,{signal});
      backoff = Math.min(2000, backoff*2);
    }
  }
  return {command,watch};
}
