import { compileSelectedManifest } from './selection/rules.js';
export { compileSelectedManifest };

export type JsonSchema = boolean | Record<string, unknown>;
export interface ServiceSelection { service: string; version: string; operations: string[] }
export type ServiceSelections = Record<string, ServiceSelection>;
export interface ResolvedOperation {
  alias: string; service: string; version: string; serviceId: string; serviceVersionId: string;
  endpointId: string; endpoint: string; inputSchema: JsonSchema; outputSchema: JsonSchema;
}
export interface ContractResolverOptions { engineUrl: string; apiKey?: string; fetch?: typeof globalThis.fetch }
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

/** JSON records exclude arrays and primitives before reading remote contracts. */
function record(value: unknown): Record<string, unknown> {
  // Malformed optional projections remain unknown rather than acquiring invented fields.
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

/** Validate the credential destination before placing a management key on a request. */
function graphqlURL(origin: string): string {
  const url = new URL(origin);
  // Plaintext is exclusively a literal-loopback development convenience.
  if (url.username || url.password || url.search || url.hash || url.pathname !== '/' || !(url.protocol === 'https:' || url.protocol === 'http:' && ['127.0.0.1', '[::1]'].includes(url.hostname))) throw new Error('Fused contract resolution requires an HTTPS Engine origin or literal loopback');
  return new URL('/graphql', url).href;
}

/** Read bounded GraphQL documents without redirects, credential logging or mutation requests. */
async function query(options: ContractResolverOptions, source: string, variables: Record<string, unknown>): Promise<Record<string, unknown>> {
  let response: Response;
  try {
    response = await (options.fetch ?? globalThis.fetch)(graphqlURL(options.engineUrl), { method: 'POST', redirect: 'error', headers: { 'Content-Type': 'application/json', ...(options.apiKey ? { 'X-API-Key': options.apiKey } : {}) }, body: JSON.stringify({ query: source, variables }), signal: AbortSignal.timeout(30000) });
  } catch { throw new Error('Unable to read Fused operation contracts from Engine'); }
  // Error bodies are deliberately excluded because they may contain credentials or private provider content.
  if (!response.ok || !response.body) throw new Error(`Fused contract request failed (HTTP ${response.status})`);
  const chunks: Uint8Array[] = []; let length = 0;
  for await (const chunk of response.body) {
    length += chunk.length;
    // A catalogue cannot force unbounded allocation in a build process.
    if (length > 16 * 1024 * 1024) throw new Error('Fused contract response exceeds 16 MiB');
    chunks.push(chunk);
  }
  let result: Record<string, unknown>;
  try { result = record(JSON.parse(Buffer.concat(chunks).toString('utf8'))); }
  catch { throw new Error('Fused returned invalid contract JSON'); }
  // Partial GraphQL results must not silently omit selected operations.
  if ((result.errors !== undefined && (!Array.isArray(result.errors) || result.errors.length > 0)) || !result.data) throw new Error('Fused could not resolve the selected operation contracts');
  return record(result.data);
}

interface SchemaScope { definitions: Record<string, JsonSchema>; schema(value: unknown): JsonSchema }
const schemaMaps = new Set(['properties', 'patternProperties', 'dependentSchemas']);
const schemaValues = new Set(['items', 'additionalItems', 'contains', 'additionalProperties', 'additional_properties', 'unevaluatedProperties', 'unevaluatedItems', 'propertyNames', 'not', 'if', 'then', 'else']);
const schemaArrays = new Set(['allOf', 'anyOf', 'oneOf', 'prefixItems']);

/** Scope each source document before flattening so parameter-local references never capture another schema. */
function createSchemaScope(sharedContracts: Record<string, unknown>): SchemaScope {
  const definitions: Record<string, JsonSchema> = Object.create(null);
  const sharedNames = new Map<string, string>(); let nextName = 0;
  // Schema contracts retain raw source for declaration fidelity; execution still belongs to Engine.
  const source = (value: unknown): JsonSchema => {
    const contract = record(value); const raw = contract.raw ?? contract.projection;
    return typeof raw === 'boolean' ? raw : record(raw);
  };
  /** Normalize one independently rooted document and all references reachable from its public shape. */
  function document(raw: JsonSchema, reservedName?: string): JsonSchema {
    const refs = new Map<string, string>();
    // Shared root recursion uses the same generated definition instead of allocating another alias.
    if (reservedName) refs.set('#', reservedName);
    /** Resolve JSON pointers within this document, falling back only to admitted shared component names. */
    function reference(ref: string): string {
      const previous = refs.get(ref);
      if (previous) return previous;
      let target: unknown = raw; let local = ref === '#' || ref.startsWith('#/');
      if (ref.startsWith('#/')) {
        for (const token of ref.slice(2).split('/')) {
          const key = token.replace(/~1/g, '/').replace(/~0/g, '~');
          if (!target || typeof target !== 'object' || !Object.hasOwn(target, key)) { local = false; break; }
          target = (target as Record<string, unknown>)[key];
        }
      }
      if (local) {
        const name = `Schema${nextName++}`; refs.set(ref, name);
        definitions[name] = normalize(typeof target === 'boolean' ? target : record(target), 0);
        return name;
      }
      const shared = referenceName(ref);
      if (!shared || !Object.hasOwn(sharedContracts, shared)) throw new Error('Fused schema contains an unresolved reference');
      const found = sharedNames.get(shared);
      if (found) return found;
      const name = `Schema${nextName++}`; sharedNames.set(shared, name);
      definitions[name] = document(source(sharedContracts[shared]), name);
      return name;
    }
    /** Traverse schema keywords only, keeping example/const objects from being mistaken for references. */
    function normalize(value: JsonSchema, depth: number): JsonSchema {
      if (depth > 64) throw new Error('Fused schema exceeds type-generation depth');
      if (typeof value === 'boolean') return value;
      const result: Record<string, unknown> = { ...value };
      delete result.$defs; delete result.definitions;
      if (typeof value.$ref === 'string') result.$ref = '#/$defs/' + reference(value.$ref);
      for (const [key, item] of Object.entries(value)) {
        if (schemaMaps.has(key)) result[key] = Object.fromEntries(Object.entries(record(item)).map(([name, child]) => [name, normalize(typeof child === 'boolean' ? child : record(child), depth + 1)]));
        else if (schemaValues.has(key) && (typeof item === 'boolean' || item && typeof item === 'object' && !Array.isArray(item))) result[key] = normalize(item as JsonSchema, depth + 1);
        else if (schemaArrays.has(key) && Array.isArray(item)) result[key] = item.map(child => normalize(typeof child === 'boolean' ? child : record(child), depth + 1));
      }
      return result;
    }
    return normalize(raw, 0);
  }
  return { definitions, schema: value => document(source(value)) };
}

/** Collect flat body declarations using Engine's composed-property admission rules. */
function bodyRoot(value: JsonSchema, definitions: Record<string, JsonSchema>, seen = new Set<string>(), depth = 0): Record<string, unknown> {
  // Only root branches are expanded; nested properties remain recursive named types.
  if (depth > 64) throw new Error('Fused body schema exceeds type-generation depth');
  const object = record(value); const properties: Record<string, unknown> = Object.create(null);
  const required = new Set<string>(); let additional: unknown = false;
  const branches: Record<string, unknown>[] = [object];
  if (typeof object.$ref === 'string') {
    const key = referenceName(object.$ref);
    if (!key || !Object.hasOwn(definitions, key)) throw new Error('Fused request body schema reference cannot be resolved');
    if (!seen.has(key)) branches.push(bodyRoot(definitions[key]!, definitions, new Set([...seen, key]), depth + 1));
  }
  for (const keyword of ['allOf', 'anyOf', 'oneOf']) {
    if (Array.isArray(object[keyword])) for (const branch of object[keyword]) branches.push(bodyRoot(record(branch), definitions, seen, depth + 1));
  }
  for (const branch of branches) {
    for (const [key, item] of Object.entries(record(branch.properties))) {
      // Engine preserves every composed declaration for the same flat field.
      properties[key] = Object.hasOwn(properties, key) ? { anyOf: [properties[key], item] } : item;
    }
    for (const key of Array.isArray(branch.required) ? branch.required : []) if (typeof key === 'string') required.add(key);
    for (const keyword of ['additionalProperties', 'unevaluatedProperties', 'additional_properties']) {
      const item = branch[keyword];
      if (item === true) additional = true;
      else if (item && typeof item === 'object' && additional !== true) additional = additional ? { anyOf: [additional, item] } : item;
    }
  }
  return { properties, required: [...required], additionalProperties: additional };
}

/** Retain only shared definitions reachable from a selected operation's public schema. */
function withDefinitions(value: JsonSchema, shared: Record<string, JsonSchema>): JsonSchema {
  // Boolean schemas cannot reference a shared dictionary.
  if (typeof value === 'boolean') return value;
  const definitions = { ...shared, ...record(value.definitions), ...record(value.$defs) };
  const kept: Record<string, JsonSchema> = Object.create(null);
  const { $defs: _defs, definitions: _legacy, ...root } = value;
  const pending: unknown[] = [root];
  while (pending.length) {
    const item = pending.pop();
    // Traversal visits only schema objects and arrays, never evaluates provider data.
    if (!item || typeof item !== 'object') continue;
    if (Array.isArray(item)) { pending.push(...item); continue; }
    const object = record(item);
    if (typeof object.$ref === 'string' && object.$ref !== '#') {
      const key = referenceName(object.$ref);
      // Missing local definitions must fail rather than yield misleading unsafe types.
      if (!key || !Object.hasOwn(definitions, key)) throw new Error('Fused schema contains an unresolved reference');
      if (!Object.hasOwn(kept, key)) { kept[key] = definitions[key] as JsonSchema; pending.push(definitions[key]); }
    }
    for (const [key, value] of Object.entries(object)) {
      if (schemaMaps.has(key)) pending.push(...Object.values(record(value)));
      else if (schemaValues.has(key)) pending.push(value);
      else if (schemaArrays.has(key) && Array.isArray(value)) pending.push(...value);
    }
  }
  return { ...root, ...(Object.keys(kept).length ? { $defs: kept } : {}) };
}

/** Match Engine's flat operation input without requiring handwritten provider schemas. */
function inputSchema(operation: Record<string, unknown>, scope: SchemaScope, authParameters: Set<string>): JsonSchema {
  const { definitions, schema } = scope;
  let requiredBodyAlternatives: string[] = [];
  const properties: Record<string, unknown> = Object.create(null); const required = new Set<string>();
  for (const raw of Array.isArray(operation.parameters) ? operation.parameters : []) {
    const parameter = record(raw); const name = parameter.name;
    // Credentials are supplied by Fused after consent, never by authored operation arguments.
    if (typeof name === 'string' && (name.toLowerCase().startsWith('fused_') || String(parameter.in).toLowerCase() === 'header' && name.toLowerCase() === 'authorization' || authParameters.has(authParameterKey(String(parameter.in), name)))) continue;
    // Unknown or duplicate parameters are ambiguous in the flat Engine input object.
    if (typeof name !== 'string' || !name || name === '_headers' || Object.hasOwn(properties, name)) throw new Error('Invalid Fused parameter contract');
    const content = Object.values(record(parameter.content)).map(record).find(value => value.schema);
    properties[name] = parameter.schema ? schema(parameter.schema) : content ? schema(content.schema) : parameter.type ? { type: parameter.type } : {};
    if (parameter.required === true) required.add(name);
  }
  const parameterNames = new Set(Object.keys(properties));
  const content = record(operation.request_content); let additional: unknown = false;
  // A request body is optional metadata, but when present its selected representation must be unambiguous.
  if (operation.request_content) {
    const representations = Array.isArray(content.representations) ? content.representations.map(record) : [];
    const representation = representations.length === 1 ? representations[0] : representations.find(value => typeof content.default_media_type === 'string' && typeof value.media_type === 'string' && value.media_type.toLowerCase() === content.default_media_type.toLowerCase());
    if (!representation) throw new Error('Fused request has no unambiguous schema representation');
    const body = bodyRoot(schema(representation.schema), definitions);
    const fields = record(body.properties);
    // Encoding-only fields are admitted by Engine even when the object schema omits them.
    for (const key of Object.keys(record(representation.encoding))) if (!Object.hasOwn(fields, key)) fields[key] = {};
    if (!representation.item_schema && (Object.keys(fields).length || body.additionalProperties === true || typeof body.additionalProperties === 'object')) {
      for (const [key, value] of Object.entries(fields)) {
        if (key === '_headers') throw new Error('Fused body field conflicts with a reserved parameter');
        // Provider parameters take precedence over identically named body fields in Engine.
        if (!Object.hasOwn(properties, key)) properties[key] = value;
      }
      const bodyRequired = Array.isArray(body.required) ? body.required.filter((key): key is string => typeof key === 'string') : [];
      // Flat dispatch routes declared parameters before the body, so required collisions cannot be supplied.
      if (bodyRequired.some(key => parameterNames.has(key))) throw new Error('Required Fused body field conflicts with a routed parameter');
      for (const key of bodyRequired) required.add(key);
      // A required closed body with optional fields still needs at least one authored body value.
      if (content.required === true && !bodyRequired.length && body.additionalProperties === false) requiredBodyAlternatives = Object.keys(fields).filter(key => !parameterNames.has(key));
      if (content.required === true && !bodyRequired.length && body.additionalProperties === false && !requiredBodyAlternatives.length) throw new Error('Required Fused body has no routable fields');
      additional = body.additionalProperties ?? false;
    } else {
      const name = typeof content.payload_parameter === 'string' && content.payload_parameter ? content.payload_parameter : 'body';
      if (name === '_headers' || Object.hasOwn(properties, name)) throw new Error('Fused body field conflicts with a parameter');
      properties[name] = representation.item_schema ? { type: 'array', items: schema(representation.item_schema) } : schema(representation.schema);
      // Raw serialization always requires its payload field in Engine, even for historically optional metadata.
      if (content.required === true || representation.serialization === 'raw') required.add(name);
    }
  }
  properties._headers = { type: 'object', additionalProperties: { type: 'string' } };
  return withDefinitions({ type: 'object', properties, required: [...required].sort(), additionalProperties: additional, ...(requiredBodyAlternatives.length ? { anyOf: requiredBodyAlternatives.map(key => ({ required: [key] })) } : {}) }, definitions);
}

/** Successful response alternatives retain their declared schemas and shared recursive types. */
function outputSchema(operation: Record<string, unknown>, scope: SchemaScope): JsonSchema {
  const { definitions, schema } = scope;
  const alternatives: JsonSchema[] = [];
  for (const [status, raw] of Object.entries(record(operation.responses))) {
    // Error bodies are not successful return types of Engine execution.
    if (!/^2(?:[0-9]{2}|XX)$/i.test(status)) continue;
    const response = record(raw);
    for (const value of Array.isArray(response.representations) ? response.representations : []) {
      const representation = record(value);
      alternatives.push(representation.item_schema ? { type: 'array', items: schema(representation.item_schema) } : schema(representation.schema));
    }
  }
  // A missing response schema must remain unknown, never an unsafe inferred provider shape.
  const result = alternatives.length === 1 ? alternatives[0]! : alternatives.length ? { anyOf: alternatives } : {};
  return withDefinitions(result, definitions);
}

/** Header names are case-insensitive while query and cookie parameter names retain provider spelling. */
function authParameterKey(location: string, name: string): string {
  const place = location.toLowerCase();
  return place + ':' + (place === 'header' ? name.toLowerCase() : name);
}

/** Exclude only credential parameters belonging to the operation's declared security alternatives. */
function operationAuthParameters(operation: Record<string, unknown>, configs: Record<string, unknown>[]): Set<string> {
  const names = new Set<string>();
  // Omitted operation security inherits provider defaults; an explicit empty list is anonymous.
  const requirements = Array.isArray(operation.security_requirements) ? operation.security_requirements : undefined;
  const schemes = requirements && new Set(requirements.flatMap(value => Array.isArray(record(value).schemes) ? (record(value).schemes as unknown[]).map(scheme => record(scheme).scheme) : []));
  for (const config of configs) {
    if (schemes && !schemes.has(config.name)) continue;
    const placement = record(config.oauth_token_placement);
    const location = typeof placement.location === 'string' ? placement.location : config.location;
    const name = typeof placement.name === 'string' ? placement.name : config.key_name;
    if (typeof location === 'string' && typeof name === 'string' && ['query', 'header', 'cookie'].includes(location.toLowerCase())) names.add(authParameterKey(location, name));
  }
  return names;
}

/** Resolve exact read-only Registry contracts through the selected Engine's authorization boundary. */
export async function resolveSelectedContracts(selections: ServiceSelections, options: ContractResolverOptions): Promise<ResolvedOperation[]> {
  compileSelectedManifest(selections, 'contract-resolution', '1.0.0');
  const result: ResolvedOperation[] = [];
  for (const [alias, selected] of Object.entries(selections).sort(([left], [right]) => left.localeCompare(right))) {
    const parts = selected.service.startsWith('@') ? selected.service.slice(1).split('/') : [selected.service];
    const data = await query(options, 'query MinionVersions($serviceId:String!,$provider:String){serviceVersions(serviceId:$serviceId,provider:$provider){id service_id name contract_version required_capabilities}}', { serviceId: parts.at(-1), ...(parts.length === 2 ? { provider: parts[0] } : {}) });
    const versions = Array.isArray(data.serviceVersions) ? data.serviceVersions.map(record).filter(value => value.name === selected.version || value.id === selected.version) : [];
    // A single immutable revision must be established before reading operation schema bytes.
    if (versions.length !== 1) throw new Error(`Fused service version is unavailable: ${selected.service}@${selected.version}`);
    const version = versions[0]!;
    if (typeof version.id !== 'string' || !uuid.test(version.id) || typeof version.service_id !== 'string' || !uuid.test(version.service_id) || version.contract_version !== 2) throw new Error('Unsupported Fused service contract identity');
    const dataContract = await query(options, 'query MinionContracts($refs:[ServiceVersionRefInput!]!,$capabilities:[String!]!){serviceRuntimeContracts(refs:$refs,engine_contract_version:2,engine_capabilities:$capabilities,retain_generation_contract:false){service_id service_version_id version schema_definitions service{auth_configs{name type location key_name oauth_token_placement{location name}}} operations{id name security_requirements{schemes{scheme}} parameters{name in required type schema content} request_content responses}}}', { refs: [{ service_id: version.service_id, version: version.id }], capabilities: version.required_capabilities ?? [] });
    const contracts = Array.isArray(dataContract.serviceRuntimeContracts) ? dataContract.serviceRuntimeContracts.map(record) : [];
    const contract = contracts[0];
    // Remote responses must prove the same exact identity; no latest-version fallback is permitted.
    if (contracts.length !== 1 || !contract || contract.service_id !== version.service_id || contract.service_version_id !== version.id || contract.version !== version.name) throw new Error('Fused resolved a different service contract');
    const authConfigs = Array.isArray(record(contract.service).auth_configs) ? (record(contract.service).auth_configs as unknown[]).map(record) : [];
    const operations = Array.isArray(contract.operations) ? contract.operations.map(record) : [];
    for (const endpoint of [...selected.operations].sort()) {
      const matches = operations.filter(value => value.name === endpoint);
      if (matches.length !== 1 || typeof matches[0]!.id !== 'string' || !uuid.test(matches[0]!.id)) throw new Error(`Fused operation is unavailable or ambiguous: ${selected.service}.${endpoint}`);
      const operation = matches[0]!;
      const scope = createSchemaScope(record(contract.schema_definitions));
      const authParameters = operationAuthParameters(operation, authConfigs);
      result.push({ alias, service: selected.service, version: selected.version, serviceId: version.service_id, serviceVersionId: version.id, endpointId: operation.id as string, endpoint, inputSchema: inputSchema(operation, scope, authParameters), outputSchema: outputSchema(operation, scope) });
    }
  }
  return result;
}

/** Resolve only local schema references carried by this immutable contract. */
function referenceName(ref: string): string | undefined {
  const match = /^#\/(?:\$defs|definitions|components\/schemas)\/(.+)$/.exec(ref);
  return match?.[1]?.replace(/~1/g, '/').replace(/~0/g, '~');
}

/** Generate safe TypeScript literals without allowing schema text to become source code. */
function literal(value: unknown): string {
  return value === null || ['string', 'number', 'boolean'].includes(typeof value) ? JSON.stringify(value) : 'unknown';
}

/** Render schema structure into types while preserving required fields and recursive references. */
function render(value: JsonSchema, aliases: Map<string, string>, rootName: string, depth = 0): string {
  // Bounds protect builds against hostile nesting; false is the only impossible schema.
  if (depth > 64) throw new Error('Fused schema exceeds type-generation depth');
  if (typeof value === 'boolean') return value ? 'unknown' : 'never';
  const schema = record(value);
  const child = (item: unknown): string => render(typeof item === 'boolean' ? item : record(item), aliases, rootName, depth + 1);
  // OpenAPI 3.0 nullable remains a union even when its source schema uses an enum or reference.
  if (schema.nullable === true || schema['x-nullable'] === true) return `(${child({ ...schema, nullable: false, 'x-nullable': false })} | null)`;
  const compositions = (['anyOf', 'oneOf', 'allOf'] as const).filter(key => Array.isArray(schema[key]));
  if (compositions.length) {
    const base = { ...schema }; for (const key of compositions) delete base[key];
    const parts = compositions.map(key => { const variants = schema[key] as unknown[]; return variants.length ? '(' + variants.map(child).join(key === 'allOf' ? ' & ' : ' | ') + ')' : key === 'allOf' ? 'unknown' : 'never'; });
    if (base.type || base.properties || base.$ref || base.enum || Object.hasOwn(base, 'const')) parts.unshift(child(base));
    return parts.join(' & ');
  }
  if (typeof schema.$ref === 'string') {
    if (schema.$ref === '#') return rootName;
    const name = referenceName(schema.$ref); const target = name && aliases.get(name);
    if (!target) throw new Error('Fused schema contains an unresolved reference');
    const { $ref: _reference, ...siblings } = schema;
    // Draft 2020-12 reference siblings constrain the same value rather than replacing the target.
    return siblings.type || siblings.properties || siblings.required || siblings.enum || Object.hasOwn(siblings, 'const') ? `(${target} & ${child(siblings)})` : target;
  }
  if (Object.hasOwn(schema, 'const')) return literal(schema.const);
  if (Array.isArray(schema.enum)) return schema.enum.map(literal).join(' | ') || 'never';
  if (Array.isArray(schema.type)) return schema.type.map(type => child({ ...schema, type })).join(' | ');
  if (schema.type === 'array') return `Array<${child(schema.items)}>`;
  if (schema.type === 'object' || schema.properties || schema.required) {
    const properties = { ...record(schema.properties) }; const required = new Set(Array.isArray(schema.required) ? schema.required : []);
    // JSON Schema may require a field without declaring its value shape.
    for (const key of required) if (typeof key === 'string' && !Object.hasOwn(properties, key)) properties[key] = {};
    const fields = Object.entries(properties).sort(([a], [b]) => a.localeCompare(b)).map(([key, item]) => `${JSON.stringify(key)}${required.has(key) ? '' : '?'}: ${child(item)};`);
    const additional = schema.additionalProperties ?? schema.additional_properties;
    if (additional && typeof additional === 'object' && !fields.length) return `Record<string, ${child(additional)}>`;
    if (additional === true || additional && typeof additional === 'object') fields.push('[key: string]: unknown;');
    return `{ ${fields.join(' ')} }`;
  }
  return ({ string: 'string', number: 'number', integer: 'number', boolean: 'boolean', null: 'null' } as Record<string, string>)[String(schema.type)] ?? 'unknown';
}

/** Emit named recursive aliases without leaking provider record names into TypeScript identifiers. */
function renderNamed(schema: JsonSchema, name: string): string {
  const definitions = { ...record(record(schema).definitions), ...record(record(schema).$defs) };
  const aliases = new Map(Object.keys(definitions).sort().map((key, index) => [key, `${name}Definition${index}`]));
  const lines = [...aliases].map(([key, target]) => `type ${target} = ${render(definitions[key] as JsonSchema, aliases, name)};`);
  lines.push(`type ${name} = ${render(schema, aliases, name)};`);
  return lines.join('\n');
}

/** Augment Minion's native services import with exactly the selected provider types. */
export function generateServicesDeclaration(operations: ResolvedOperation[]): string {
  const groups = new Map<string, string[]>(); const lines = ["import '@fused/minion';"];
  operations.forEach((operation, index) => {
    lines.push(renderNamed(operation.inputSchema, `MinionInput${index}`), renderNamed(operation.outputSchema, `MinionOutput${index}`));
    if (!groups.has(operation.alias)) groups.set(operation.alias, []);
    groups.get(operation.alias)!.push(`${JSON.stringify(operation.endpoint)}: { input: MinionInput${index}; output: MinionOutput${index} };`);
  });
  lines.push(`declare module '@fused/minion' { interface ServiceContracts { ${[...groups].map(([alias, methods]) => `${JSON.stringify(alias)}: { ${methods.join(' ')} };`).join(' ')} } }\n`);
  return lines.join('\n');
}
