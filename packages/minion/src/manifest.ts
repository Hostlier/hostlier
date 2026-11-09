import { isServiceIdentity } from './service-identity.js';
import ts from 'typescript';
import { createHash } from 'node:crypto';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

/** API app requirements contain public identities, including an optional caller-selected credential bucket. */
export interface AccessManifest {
  schema_version: 1;
  name: string;
  version: string;
  bucket?: string;
  services: Record<string, { version: string; operations: string[] }>;
}

/** Inspect the bundled module graph without importing or evaluating application code. */
export function compileAccessManifest(files: string[], name: string, version: string): AccessManifest {
  if (!/^[a-z][a-z0-9-]{0,62}$/.test(name) || !/^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.test(version) || version.length > 128) throw new Error('Consent requires an app name and explicit semantic version');
  const paths = files.map(file => resolve(file));
  const framework = fileURLToPath(new URL('./index.d.ts', import.meta.url));
  const program = ts.createProgram(paths, { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.NodeNext, moduleResolution: ts.ModuleResolutionKind.NodeNext, allowJs: true, paths: { '@fused/minion': [framework] } });
  const checker = program.getTypeChecker();
  const services: AccessManifest['services'] = Object.create(null);
  const seen = new Set<ts.Node>();

  /** Follow immutable bindings, including imported constants, but never execute expressions. */
  function unwrap(node: ts.Node, trail = new Set<ts.Node>()): ts.Node {
    if (trail.has(node)) throw new Error('Cyclic Fused declaration');
    trail.add(node);
    if (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isSatisfiesExpression(node)) return unwrap(node.expression, trail);
    if (ts.isIdentifier(node)) {
      let symbol = checker.getSymbolAtLocation(node);
      if (symbol && symbol.flags & ts.SymbolFlags.Alias) symbol = checker.getAliasedSymbol(symbol);
      const declaration = symbol?.valueDeclaration;
      if (declaration && ts.isVariableDeclaration(declaration) && declaration.initializer && ts.isVariableDeclarationList(declaration.parent) && declaration.parent.flags & ts.NodeFlags.Const) return unwrap(declaration.initializer, trail);
    }
    return node;
  }

  /** Recognize actual Minion imports rather than matching unrelated function names. */
  function helper(node: ts.Expression): string | undefined {
    let symbol = checker.getSymbolAtLocation(ts.isPropertyAccessExpression(node) ? node.name : node);
    // Re-exports retain the framework symbol even when imported through a local barrel module.
    if (symbol && symbol.flags & ts.SymbolFlags.Alias) {
      const target = checker.getAliasedSymbol(symbol);
      if (target.declarations?.some(declaration => resolve(declaration.getSourceFile().fileName) === framework)) return target.name;
    }
    const declaration = symbol?.declarations?.[0];
    if (declaration && ts.isImportSpecifier(declaration)) {
      const source = declaration.parent.parent.parent.moduleSpecifier;
      if (ts.isStringLiteral(source) && source.text === '@fused/minion') return (declaration.propertyName ?? declaration.name).text;
    }
    if (ts.isPropertyAccessExpression(node)) {
      const namespace = checker.getSymbolAtLocation(node.expression)?.declarations?.[0];
      if (namespace && ts.isNamespaceImport(namespace)) {
        const source = namespace.parent.parent.moduleSpecifier;
        if (ts.isStringLiteral(source) && source.text === '@fused/minion') return node.name.text;
      }
    }
    // Immutable local aliases are safe to resolve without invoking application code.
    const resolved = unwrap(node);
    if (resolved !== node && ts.isIdentifier(resolved)) return helper(resolved);
    return undefined;
  }

  /** Require explicit object members so spreads and computed declarations cannot hide permissions. */
  function object(input: ts.Node): Map<string, ts.Expression> {
    const node = unwrap(input);
    if (!ts.isObjectLiteralExpression(node)) throw new Error('Fused declarations must be statically defined objects');
    const result = new Map<string, ts.Expression>();
    for (const property of node.properties) {
      if (!ts.isPropertyAssignment(property) && !ts.isShorthandPropertyAssignment(property)) throw new Error('Fused declaration spreads and methods are unsupported');
      if (!ts.isIdentifier(property.name) && !ts.isStringLiteral(property.name)) throw new Error('Computed Fused declaration names are unsupported');
      if (result.has(property.name.text)) throw new Error('Duplicate Fused declaration member');
      result.set(property.name.text, ts.isShorthandPropertyAssignment(property) ? property.name : property.initializer);
    }
    return result;
  }

  /** Literal identities are stable across build and consent; runtime expressions are not. */
  function literal(input: ts.Node | undefined): string {
    const node = input && unwrap(input);
    if (!node || !ts.isStringLiteralLike(node) || !node.text || node.text.length > 512 || /[\x00-\x20]/.test(node.text)) throw new Error('Fused service, version, and endpoint must be static strings');
    return node.text;
  }

  /** Collect all declared operations, including ones not reached by a particular action invocation. */
  function service(input: ts.Node): void {
    const node = unwrap(input);
    if (seen.has(node)) return;
    if (!ts.isCallExpression(node) || helper(node.expression) !== 'defineService' || node.arguments.length !== 1) throw new Error('Consent builds require defineService with an exact service and version');
    seen.add(node);
    const definition = object(node.arguments[0]!);
    const slug = literal(definition.get('service'));
    const pinnedVersion = literal(definition.get('version'));
    // Registry identities can include an owner namespace; preserve it in consent scope.
    if (!isServiceIdentity(slug)) throw new Error('Invalid Fused service identity');
    const operationsNode = definition.get('operations');
    if (!operationsNode) throw new Error('Fused service requires operations');
    const operations: string[] = [];
    for (const value of object(operationsNode).values()) {
      const operation = unwrap(value);
      const declaration = ts.isCallExpression(operation) && helper(operation.expression) === 'defineOperation' && operation.arguments.length === 1 ? operation.arguments[0]! : operation;
      const endpoint = literal(object(declaration).get('endpoint'));
      if (endpoint === '*') throw new Error('Wildcard Fused operations cannot be consented');
      operations.push(endpoint);
    }
    if (!operations.length) throw new Error('Fused service requires at least one operation');
    const prior = services[slug];
    if (prior && prior.version !== pinnedVersion) throw new Error('Conflicting Fused service versions');
    services[slug] = { version: pinnedVersion, operations: [...new Set([...(prior?.operations ?? []), ...operations])].sort() };
  }

  /** Traverse only application inputs included in the server build, excluding dependency implementations. */
  function visit(node: ts.Node): void {
    if (ts.isCallExpression(node) && helper(node.expression) === 'defineServices') {
      if (node.arguments.length !== 1) throw new Error('Invalid defineServices declaration');
      for (const value of object(node.arguments[0]!).values()) service(value);
    }
    if (ts.isCallExpression(node) && helper(node.expression) === 'defineService') service(node);
    ts.forEachChild(node, visit);
  }
  for (const path of paths) {
    const source = program.getSourceFile(path);
    if (source) visit(source);
  }
  if (!Object.keys(services).length) throw new Error('Consent build has no statically declared Fused services');
  const owners = new Map<string, string>();
  for (const [service, definition] of Object.entries(services)) for (const operation of definition.operations) {
    // The current Engine wire selects an endpoint by name, so cross-service collisions need an explicit protocol extension.
    if (owners.has(operation) && owners.get(operation) !== service) throw new Error('Ambiguous Fused operation across services: ' + operation);
    owners.set(operation, service);
  }
  const manifest: AccessManifest = { schema_version: 1, name, version, services: Object.fromEntries(Object.keys(services).sort().map(key => [key, services[key]!])) };
  if (Object.keys(services).length > 64 || owners.size > 1000 || Buffer.byteLength(JSON.stringify(manifest)) > 65536) throw new Error('Fused manifest exceeds consent limits (64 services, 1000 operations, 64 KiB)');
  return manifest;
}

/** Hash the emitted manifest bytes; consumers must submit those exact bytes for consent. */
export function manifestDigest(manifest: AccessManifest): string {
  return 'sha256:' + createHash('sha256').update(JSON.stringify(manifest)).digest('hex');
}

export { withSelectedBucket } from './selection/rules.js';
