import { readFile, mkdir, writeFile, rename, rm } from 'node:fs/promises';
import { homedir } from 'node:os';
import { resolve, dirname } from 'node:path';
import { createHash, randomUUID } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';
import { withSelectedBucket } from './manifest.js';
import { compileSelectedManifest, resolveSelectedContracts, generateServicesDeclaration, type ServiceSelections, type ResolvedOperation } from './contracts.js';

export interface NativeFusedOptions {
  engineUrl: string; version: string; services: ServiceSelections; bucket?: string;
}
export interface PreparedServices {
  manifest: ReturnType<typeof compileSelectedManifest>;
  operations: ResolvedOperation[];
  declaration: string;
  lock: { schema_version: 1; engine: string; selections: ServiceSelections; digest: string; operations: ResolvedOperation[] };
}

/** A saved CLI login is usable only at its original TLS Engine, never at a project-supplied alternative. */
export async function contractAPIKey(engine: string, configHome = process.env.XDG_CONFIG_HOME || resolve(homedir(), '.config')): Promise<string | undefined> {
  if (process.env.FUSED_API_KEY) return process.env.FUSED_API_KEY;
  const url = new URL(engine);
  if (url.protocol !== 'https:' || url.href !== url.origin + '/') return undefined;
  try {
    const saved = JSON.parse(await readFile(resolve(configHome, 'fused/config.json'), 'utf8'));
    if (saved.engine_url !== url.origin && saved.engine_url !== url.origin + '/') return undefined;
    if (saved.api_key_expires_at != null && !(Date.parse(saved.api_key_expires_at) > Date.now())) return undefined;
    return typeof saved.api_key === 'string' ? saved.api_key : undefined;
  } catch { return undefined; }
}

/** Resolve the selected contract without creating apps, obtaining execution tokens or running provider calls. */
export async function prepareServices(name: string, fused: NativeFusedOptions): Promise<PreparedServices> {
  const manifest = withSelectedBucket(compileSelectedManifest(fused.services, name, fused.version), fused.bucket);
  // Only declared non-secret selection fields may enter the generated artifact.
  const selections: ServiceSelections = Object.fromEntries(Object.entries(fused.services).sort(([a], [b]) => a.localeCompare(b)).map(([alias, selected]) => [alias, { service: selected.service, version: selected.version, operations: [...selected.operations].sort() }]));
  const operations = await resolveSelectedContracts(selections, { engineUrl: fused.engineUrl, apiKey: await contractAPIKey(fused.engineUrl) });
  const declaration = generateServicesDeclaration(operations);
  const digest = 'sha256:' + createHash('sha256').update(JSON.stringify(operations)).digest('hex');
  return { manifest, operations, declaration, lock: { schema_version: 1, engine: new URL(fused.engineUrl).origin, selections, digest, operations } };
}

/** Replace changed metadata atomically while preserving identical files so development watchers settle. */
async function replaceFile(path: string, value: string): Promise<void> {
  try {
    // Rebuilding unchanged contracts must not trigger another rebuild in custom generated directories.
    if (await readFile(path, 'utf8') === value) return;
  } catch (error) {
    // Only a missing output permits creation; other read failures must remain visible.
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
  }
  const temporary = path + '.' + randomUUID() + '.tmp';
  try { await writeFile(temporary, value); await rename(temporary, path); }
  finally { await rm(temporary, { force: true }); }
}

/** Keep generated types discoverable by ordinary TypeScript projects without an authored generated import. */
export async function writePreparedServices(directory: string, prepared: PreparedServices): Promise<void> {
  await mkdir(directory, { recursive: true });
  const lockPath = resolve(directory, 'fused-contract.json');
  let previous: PreparedServices['lock'] | undefined;
  try { previous = JSON.parse(await readFile(lockPath, 'utf8')); }
  catch (error) {
    // Corrupt locks require explicit user repair rather than silently accepting a new source contract.
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
  }
  // Immutable Registry versions must retain their contract bytes even when a fresh build resolves them again.
  if (previous?.engine === prepared.lock.engine) {
    for (const operation of prepared.operations) {
      const prior = previous.operations.find(value => value.service === operation.service && value.version === operation.version && value.endpoint === operation.endpoint);
      // Alias renames are local authoring changes and cannot hide a changed pinned provider contract.
      if (prior) {
        const { alias: _oldAlias, ...oldContract } = prior;
        const { alias: _newAlias, ...newContract } = operation;
        if (JSON.stringify(oldContract) !== JSON.stringify(newContract)) throw new Error('A pinned Fused operation contract changed; review the Registry version before rebuilding');
      }
    }
  }
  await replaceFile(lockPath, JSON.stringify(prepared.lock, null, 2) + '\n');
  await replaceFile(resolve(directory, 'services.d.ts'), prepared.declaration);
}

/** Typecheck application code against precisely the generated service bindings shipped in its artifact. */
export function checkNativeApplication(entry: string, declaration: string): void {
  const configPath = ts.findConfigFile(dirname(entry), ts.sys.fileExists);
  let settings: ts.CompilerOptions = { target: ts.ScriptTarget.ES2023, module: ts.ModuleKind.NodeNext, moduleResolution: ts.ModuleResolutionKind.NodeNext, strict: true, jsx: ts.JsxEmit.ReactJSX, skipLibCheck: true };
  let ambient: string[] = [];
  // Preserve authored ambient declarations while replacing previously generated service declarations.
  if (configPath) {
    const config = ts.readConfigFile(configPath, ts.sys.readFile);
    if (config.error) throw new Error(ts.flattenDiagnosticMessageText(config.error.messageText, '\n'));
    const parsed = ts.parseJsonConfigFileContent(config.config, ts.sys, dirname(configPath));
    if (parsed.errors.length) throw new Error(ts.formatDiagnosticsWithColorAndContext(parsed.errors, diagnosticHost));
    settings = { ...settings, ...parsed.options };
    ambient = parsed.fileNames.filter(path => /\.d\.[cm]?ts$/.test(path) && !/[\\/]\.minion[\\/]services\.d\.ts$/.test(path) && resolve(path) !== resolve(declaration));
  }
  const framework = fileURLToPath(new URL('./index.d.ts', import.meta.url));
  // Force the same Minion version used by the bundler and override old generated declarations from project includes.
  const program = ts.createProgram([entry, declaration, ...ambient], { ...settings, noEmit: true, incremental: false, composite: false, rootDir: undefined, paths: { ...settings.paths, '@fused/minion': [framework] } });
  const diagnostics = ts.getPreEmitDiagnostics(program);
  if (diagnostics.length) throw new Error(ts.formatDiagnosticsWithColorAndContext(diagnostics, diagnosticHost));
}
const diagnosticHost: ts.FormatDiagnosticsHost = { getCanonicalFileName: path => path, getCurrentDirectory: () => process.cwd(), getNewLine: () => '\n' };
