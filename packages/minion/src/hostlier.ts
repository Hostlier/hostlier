import { engineAddress } from './grpc.js';

export interface HostlierOptions {
  name: string; image?: string; route: string;
  /** Build and transfer the local artifact for this server CPU instead of pulling the named image. */
  architecture?: 'amd64' | 'arm64';
  fused?: { grpcUrl: string; appId: string; tokenSecret: string };
  /** Environment variable to existing Hostlier secret name; values never belong here. */
  secrets?: Record<string, string>;
}

/** Export a normal Hostlier v2 deployment with explicit Engine identity and stored secret references. */
export function hostlierProject(options: HostlierOptions) {
  if (!/^[a-z][a-z0-9-]{0,62}$/.test(options.name)) throw new Error('Invalid Hostlier name');
  const source = buildSource(options);
  const url = new URL(options.route.includes('://') ? options.route : 'https://' + options.route);
  if (url.protocol !== 'https:' || url.port || url.username || url.password || url.search || url.hash || url.pathname !== '/' || !/^[a-z0-9.-]+\.[a-z]{2,}$/i.test(url.hostname)) throw new Error('Use an HTTPS hostname at the root path');
  // Deterministic apps need no Engine identity or integration credential.
  if (options.fused) {
    engineAddress(options.fused.grpcUrl);
    if (!/^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/i.test(options.fused.appId)) throw new Error('An immutable Engine app UUID is required');
    if (!/^[A-Za-z_][A-Za-z0-9_.-]{0,127}$/.test(options.fused.tokenSecret)) throw new Error('Use a Hostlier secret name');
  }
  for (const [key, value] of Object.entries(options.secrets ?? {})) {
    if (!/^[A-Z_][A-Z0-9_]*$/.test(key) || !/^[A-Za-z_][A-Za-z0-9_.-]{0,127}$/.test(value) || ['FUSED_APP_TOKEN', 'FUSED_APP_ID', 'FUSED_ENGINE_GRPC_URL', 'PORT', 'MINION_ORIGIN'].includes(key)) throw new Error('Invalid or reserved secret binding');
  }
  return { version: 2, name: options.name, source, endpoint: { exposure: 'public', port: 8080, route: url.hostname }, health: { path: '/healthz', timeoutSeconds: 30 }, shutdown: { graceSeconds: 35 }, fused: options.fused ? { grpcUrl: options.fused.grpcUrl, appId: options.fused.appId, tokenSecret: options.fused.tokenSecret } : undefined, env: { PORT: '8080', MINION_ORIGIN: url.origin }, secrets: { ...options.secrets } };
}

/** Local artifacts carry their own image bytes. A registry reference is needed only for a pull. */
function buildSource({ architecture, image }: HostlierOptions) {
  if (architecture !== undefined || image === undefined) {
    const target = architecture ?? 'amd64';
    if (target !== 'amd64' && target !== 'arm64') throw new Error('Use amd64 or arm64 for the target architecture');
    return { container: { context: '.', architecture: target } };
  }
  if (typeof image !== 'string' || !/^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,511}$/.test(image) || !/(:[\w.-]+|@sha256:[a-f0-9]{64})$/.test(image) || image.endsWith(':latest')) throw new Error('Use an explicitly versioned image for a registry deployment');
  return { image };
}
