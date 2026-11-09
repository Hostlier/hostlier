import type { ServiceSelections } from '../contracts.js';
import type { AccessManifest } from '../manifest.js';

const unsafeNames = new Set(['__proto__', 'prototype', 'constructor', 'then']);

/** Accept Registry service identities while retaining an optional owner namespace. */
export function isServiceIdentity(value: string): boolean {
  return /^(?:@[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}\/)?[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$/.test(value);
}

/** Selection validation has one authority shared by permission manifests and generated bindings. */
export function compileSelectedManifest(selections: ServiceSelections, name: string, version: string): AccessManifest {
  // Stable app identity is required before any network access or publication artifact is produced.
  if (typeof name !== 'string' || typeof version !== 'string' || !/^[a-z][a-z0-9-]{0,62}$/.test(name) || version.length > 128 || !/^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.test(version)) throw new Error('Fused selection requires an app name and semantic version');
  const aliases = Object.keys(selections && typeof selections === 'object' && !Array.isArray(selections) ? selections : {}).sort();
  // An empty or oversized allowlist cannot represent a bounded Minion contract.
  if (!aliases.length || aliases.length > 64) throw new Error('Select between 1 and 64 Fused services');
  const services: AccessManifest['services'] = Object.create(null);
  const owners = new Map<string, string>();
  for (const alias of aliases) {
    const selected = selections[alias]!;
    // Safe aliases support ordinary native property access without thenable/prototype traps.
    if (!/^[A-Za-z][A-Za-z0-9_]{0,63}$/.test(alias) || unsafeNames.has(alias) || !selected || typeof selected !== 'object' || typeof selected.service !== 'string' || !isServiceIdentity(selected.service) || typeof selected.version !== 'string' || !selected.version || selected.version.length > 128 || /[\x00-\x20]/.test(selected.version)) throw new Error('Invalid Fused service selection');
    // Provider revisions must be exact; floating labels would make redeployment change its contract.
    if (['latest', '*'].includes(selected.version) || /^[~^<>]/.test(selected.version)) throw new Error('Pin an exact Fused service version');
    // Repeated methods are mistakes and must not silently change the generated contract.
    if (!Array.isArray(selected.operations) || !selected.operations.length || new Set(selected.operations).size !== selected.operations.length) throw new Error('Select unique Fused operations');
    const previous = services[selected.service];
    // One provider cannot resolve to two revisions within one consent grant.
    if (previous && previous.version !== selected.version) throw new Error('Conflicting Fused service versions');
    for (const operation of selected.operations) {
      // Endpoint-only gRPC routing cannot disambiguate identical names across providers.
      if (typeof operation !== 'string' || !operation || operation.length > 512 || /[\x00-\x20]/.test(operation) || operation === '*' || unsafeNames.has(operation)) throw new Error('Invalid selected Fused operation');
      if (owners.has(operation) && owners.get(operation) !== selected.service) throw new Error('Ambiguous Fused operation across services: ' + operation);
      owners.set(operation, selected.service);
    }
    services[selected.service] = { version: selected.version, operations: [...new Set([...(previous?.operations ?? []), ...selected.operations])].sort() };
  }
  const manifest: AccessManifest = { schema_version: 1, name, version, services: Object.fromEntries(Object.keys(services).sort().map(key => [key, services[key]!])) };
  // Preserve the existing Engine consent limits before emitting files.
  if (owners.size > 1000 || Buffer.byteLength(JSON.stringify(manifest)) > 65536) throw new Error('Fused manifest exceeds consent limits');
  return manifest;
}


/** Bind an explicitly selected bucket into the hashed request; omission leaves selection to Fused consent. */
export function withSelectedBucket(manifest: AccessManifest, bucket?: string): AccessManifest {
  // Missing configuration preserves the generic picker; blank or malformed selections cannot silently fall back.
  if (bucket === undefined) return manifest;
  if (typeof bucket !== 'string' || !bucket || bucket.length > 255 || bucket.trim() !== bucket || /[\x00\r\n\t]/.test(bucket) || bucket === '*') throw new Error('Select an exact Fused bucket name or UUID');
  const selected = { ...manifest, bucket };
  // The final wire bytes, including routing, must respect the Engine registration limit.
  if (Buffer.byteLength(JSON.stringify(selected)) > 65536) throw new Error('Fused manifest exceeds consent limits');
  return selected;
}
