import { createHash } from 'node:crypto';
import { mkdir, open, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { isDeepStrictEqual } from 'node:util';
import YAML from 'yaml';
import { controllerRunner } from './controller.js';

const namePattern = /^[a-z][a-z0-9-]{0,62}$/;
const variablePattern = /^[A-Za-z_][A-Za-z0-9_]*$/;
const sizeLimit = 1024 * 1024;

function identifier(value) {
  if (typeof value !== 'string' || !namePattern.test(value) || value.endsWith('-')) throw new Error('Invalid Hostlier application name');
  return value;
}

function parseProject(source) {
  if (Buffer.byteLength(source) > sizeLimit) throw new Error('Bundle exceeds Hostlier’s 1 MiB configuration limit');
  const document = YAML.parseDocument(source, { uniqueKeys: true, strict: true });
  if (document.errors.length || document.warnings.length) throw new Error('Invalid bundled .hostlier YAML');
  const project = document.toJS({ maxAliasCount: 0 });
  if (!project || typeof project !== 'object' || Array.isArray(project)) throw new Error('Bundle must contain one project mapping');
  rejectLocalInputs(project);
  return project;
}

function rejectLocalInputs(project) {
  // A saved instance must not depend on mutable build files outside its pinned bundle.
  if (project.source && (!project.source.image || Object.keys(project.source).some(key => key !== 'image')) || project.package || project.packages || Object.values(project.configs ?? {}).some(file => file?.file)) throw new Error('Cloud SDK bundles require a container image and inline configuration; local source files and project package hooks are unsupported');
  for (const member of Object.values(project.sidecars ?? {})) rejectLocalInputs(member);
}

function environment(values) {
  if (!values || typeof values !== 'object' || Array.isArray(values)) throw new Error('Environment must be a string map');
  for (const [key, value] of Object.entries(values)) {
    if (!variablePattern.test(key) || typeof value !== 'string' || Buffer.byteLength(value) > 65536 || value.includes('\0')) throw new Error('Invalid environment entry');
  }
  return values;
}

function secretBindings(declared, values) {
  if (!values || typeof values !== 'object' || Array.isArray(values)) throw new Error('Secrets must be a stored secret reference map');
  for (const [key, value] of Object.entries(values)) {
    if (!variablePattern.test(key) || !Object.hasOwn(declared ?? {}, key)) throw new Error(`Bundle does not declare secret ${key}`);
    if (typeof value !== 'string' || !/^secret:\/\/[^\s]{1,247}$/.test(value)) throw new Error('Secrets must use stored secret:// references');
    declared[key] = value;
  }
}

function sidecarSecretBindings(project, values) {
  if (!values || typeof values !== 'object' || Array.isArray(values)) throw new Error('Sidecar secrets must map sidecar names to stored secret references');
  for (const [name, bindings] of Object.entries(values)) {
    if (!Object.hasOwn(project.sidecars ?? {}, name)) throw new Error(`Bundle does not declare sidecar ${name}`);
    secretBindings(project.sidecars[name].secrets, bindings);
  }
}

function resources(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value) || typeof value.cpu !== 'string' || typeof value.memory !== 'string') throw new Error('CPU and memory must both be supplied as Hostlier quantities');
  return { cpu: value.cpu, memory: value.memory };
}

function volumeBytes(value) {
  const parts = /^([1-9][0-9]*)(Ki|Mi|Gi|Ti)$/.exec(value);
  if (!parts) return undefined;
  const shift = { Ki: 10n, Mi: 20n, Gi: 30n, Ti: 40n }[parts[2]];
  const bytes = BigInt(parts[1]) << shift;
  return bytes <= 9223372036854775807n ? bytes : undefined;
}

function render(project) {
  const source = YAML.stringify(project);
  if (Buffer.byteLength(source) > sizeLimit) throw new Error('Rendered project exceeds Hostlier’s configuration limit');
  return source;
}

async function atomicWrite(path, value) {
  const temporary = `${path}.${process.pid}.tmp`;
  try {
    await writeFile(temporary, value, { flag: 'wx', mode: 0o600 });
    await rename(temporary, path);
  } finally {
    await rm(temporary, { force: true });
  }
}

async function locked(directory, task) {
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const lockPath = join(directory, '.lock');
  const lock = await open(lockPath, 'wx', 0o600).catch(error => {
    if (error.code === 'EEXIST') throw new Error('This instance is already being changed; inspect its operation before retrying');
    throw error;
  });
  try { return await task(); } finally { await lock.close(); await rm(lockPath, { force: true }); }
}

/** Bind an immutable vendor template to a direct controller credential. */
export async function createHostlierCloud(options) {
  if (!options?.bundlePath || !options?.stateDirectory) throw new Error('bundlePath and stateDirectory are required');
  const source = await readFile(resolve(options.bundlePath), 'utf8');
  const template = parseProject(source);
  const digest = createHash('sha256').update(source).digest('hex');
  const root = resolve(options.stateDirectory);
  const execute = options.run ?? await controllerRunner({ credentialFile: options.credentialFile, allowInsecureLocalhost: options.allowInsecureLocalhost, fetch: options.fetch });
  const profile = options.server ? ['--server', options.server] : [];
  const invoke = (args, signal, cwd = root) => execute([...args, ...profile], { cwd, signal });
  const pathFor = id => join(root, identifier(id));

  async function load(id) {
    const directory = pathFor(id);
    const metadata = JSON.parse(await readFile(join(directory, 'instance.json'), 'utf8'));
    const project = parseProject(await readFile(join(directory, '.hostlier'), 'utf8'));
    if (metadata.name !== id || metadata.bundleDigest !== digest || project.name !== id) throw new Error('Instance bundle differs from this SDK release; use its pinned bundle or an explicit upgrade');
    return { directory, metadata, project };
  }

  async function save(directory, project, metadata) {
    await atomicWrite(join(directory, '.hostlier'), render(project));
    await atomicWrite(join(directory, 'instance.json'), JSON.stringify(metadata));
  }

  async function validate(directory, project, signal) {
    const candidate = join(directory, 'candidate.hostlier');
    await atomicWrite(candidate, render(project));
    try {
      await invoke(['validate', '--file', candidate], signal, directory);
      return candidate;
    } catch (error) { await rm(candidate, { force: true }); throw error; }
  }

  async function apply(directory, project, command, signal) {
    const candidate = await validate(directory, project, signal);
    try {
      await pending(directory, command, project);
      try { return await invoke([command, '--file', candidate], signal, directory); }
      catch (error) {
        if (error.beforeApply) await rm(join(directory, 'pending.json'), { force: true });
        throw error;
      }
    } finally { await rm(candidate, { force: true }); }
  }

  async function pending(directory, action, project) {
    await atomicWrite(join(directory, 'pending.json'), JSON.stringify({ action, project, bundleDigest: digest }));
  }

  async function finish(directory, project, metadata) {
    await save(directory, project, metadata);
    await rm(join(directory, 'pending.json'), { force: true });
  }

  async function markRetired(directory, name) {
    await atomicWrite(join(directory, 'retired.json'), JSON.stringify({ name, bundleDigest: digest, disposition: 'purged' }));
    for (const file of ['instance.json', '.hostlier', 'pending.json']) await rm(join(directory, file), { force: true });
  }

  async function readStatus(name, signal, directory) {
    const response = await invoke(['status', name, '--json'], signal, directory);
    let status;
    try { status = JSON.parse(response.stdout); } catch { throw new Error('Hostlier returned invalid application status after a change'); }
    if (status?.name !== name || status.spec?.name !== name) throw new Error('Hostlier returned invalid application status');
    return status;
  }

  async function readInventory(signal, directory) {
    const result = await invoke(['status', '--json'], signal, directory);
    let inventory;
    try { inventory = JSON.parse(result.stdout); } catch { throw new Error('Hostlier returned invalid controller operation inventory'); }
    if (!Array.isArray(inventory?.applications) || !Array.isArray(inventory?.operations)) throw new Error('Hostlier returned invalid controller operation inventory');
    return inventory;
  }

  function confirmedSpec(status, name) {
    if (status?.name !== name || status.spec?.name !== name || typeof status.spec.image !== 'string' || !status.spec.image.includes('@sha256:') || status.spec.source && status.spec.source.image !== status.spec.image) throw new Error('Hostlier did not confirm the pinned deployed image; inspect status before another change');
    return parseProject(render(status.spec));
  }

  async function observed(name, signal, directory) {
    return confirmedSpec(await readStatus(name, signal, directory), name);
  }

  function ready(status) {
    return status.status === 'running' && typeof status.url === 'string' && !!status.url && status.runtimeStatus?.running !== false && (status.runtimeStatus?.readyReplicas === undefined || status.runtimeStatus.readyReplicas > 0);
  }

  async function assertNoPending(directory) {
    try { await readFile(join(directory, 'pending.json')); }
    catch (error) { if (error.code === 'ENOENT') return; throw error; }
    throw new Error('A previous change has an uncertain result; inspect Hostlier status and the saved pending.json before retrying');
  }

  async function assertCurrent(name, project, signal, directory) {
    if (!isDeepStrictEqual(await observed(name, signal, directory), project)) throw new Error('Hostlier desired configuration changed outside this SDK; inspect and reconcile it before another change');
  }

  return {
    bundleDigest: digest,
    async setSecret(name, value, { signal } = {}) {
      identifier(name);
      if (!(typeof value === 'string' || value instanceof Uint8Array) || Buffer.byteLength(value) < 1 || Buffer.byteLength(value) > 65536) throw new Error('Invalid stored secret value');
      await execute(['secret', 'set', name, '--stdin', ...profile], { cwd: root, signal, input: value });
    },
    async setInstanceSecret(app, name, value, { signal } = {}) {
      identifier(app); identifier(name);
      if (!(typeof value === 'string' || value instanceof Uint8Array) || Buffer.byteLength(value) < 1 || Buffer.byteLength(value) > 65536) throw new Error('Invalid stored secret value');
      await execute(['secret', 'set', name, '--stdin', '--app', app, ...profile], { cwd: root, signal, input: value });
    },
    async deleteSecret(name, { signal } = {}) {
      identifier(name);
      await invoke(['secret', 'delete', name], signal);
    },
    async statusSnapshot(name, { signal } = {}) {
      identifier(name);
      const status = await readStatus(name, signal, root);
      return { ...status, ready: ready(status) };
    },
    async recoverPending(name, expectedImageDigest, { signal } = {}) {
      const directory = pathFor(name);
      return locked(directory, async () => {
        const record = JSON.parse(await readFile(join(directory, 'pending.json'), 'utf8'));
        if (record.bundleDigest !== digest || record.project?.name !== name || !['deploy', 'update', 'compute'].includes(record.action)) throw new Error('Invalid or mismatched pending Hostlier operation');
        const status = await readStatus(name, signal, directory);
        const actual = confirmedSpec(status, name);
        const wanted = parseProject(render(record.project));
        wanted.image = actual.image;
        if (wanted.source) wanted.source.image = actual.image;
        if (!ready(status) || actual.image !== expectedImageDigest || !isDeepStrictEqual(wanted, actual)) throw new Error('Pending operation is not confirmed ready with the exact desired project; reconcile manually');
        await finish(directory, actual, { name, bundleDigest: digest });
        return { ...status, ready: true };
      });
    },
    async purge(name, { signal } = {}) {
      const directory = pathFor(name);
      return locked(directory, async () => {
        await assertNoPending(directory);
        const { project } = await load(name);
        await assertCurrent(name, project, signal, directory);
        const inventory = await readInventory(signal, directory);
        await atomicWrite(join(directory, 'pending.json'), JSON.stringify({ action: 'purge', project, bundleDigest: digest, knownOperations: inventory.operations.map(operation => operation.id) }));
        const result = await invoke(['delete', name, '--purge'], signal, directory);
        await markRetired(directory, name);
        return result;
      });
    },
    async recoverPendingPurge(name, { signal } = {}) {
      const directory = pathFor(name);
      return locked(directory, async () => {
        const record = JSON.parse(await readFile(join(directory, 'pending.json'), 'utf8'));
        if (record.action !== 'purge' || record.bundleDigest !== digest || record.project?.name !== name || !Array.isArray(record.knownOperations)) throw new Error('No recoverable pending purge');
        const inventory = await readInventory(signal, directory);
        const exists = inventory.applications.some(app => app.name === name);
        const known = new Set(record.knownOperations);
        const newActions = inventory.operations.filter(operation => !known.has(operation.id) && operation.kind === 'action' && operation.name === name);
        const successfulPurges = newActions.filter(operation => operation.action === 'delete' && operation.status === 'succeeded' && operation.purge === true);
        if (!exists && newActions.length === 1 && successfulPurges.length === 1) return markRetired(directory, name);
        if (exists && newActions.length === 0) {
          await assertCurrent(name, record.project, signal, directory);
          const result = await invoke(['delete', name, '--purge'], signal, directory);
          await markRetired(directory, name);
          return result;
        }
        throw new Error('Controller did not confirm a safe permanent deletion or retry');
      });
    },
    async confirmPurge(name, operationId, { signal } = {}) {
      if (typeof operationId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(operationId)) throw new Error('Invalid Hostlier operation ID');
      const directory = pathFor(name);
      return locked(directory, async () => {
        const record = JSON.parse(await readFile(join(directory, 'pending.json'), 'utf8'));
        if (record.action !== 'purge' || record.bundleDigest !== digest || record.project?.name !== name) throw new Error('No matching pending purge');
        if (record.knownOperations?.includes(operationId)) throw new Error('Operation predates the pending purge');
        const result = await invoke(['operation', operationId, '--json'], signal, directory);
        let operation;
        try { operation = JSON.parse(result.stdout); } catch { throw new Error('Hostlier returned invalid operation status'); }
        if (operation.id !== operationId || operation.kind !== 'action' || operation.name !== name || operation.action !== 'delete' || operation.status !== 'succeeded' || operation.purge !== true) throw new Error('Controller did not confirm this permanent deletion');
        const inventory = await readInventory(signal, directory);
        if (inventory.applications.some(app => app.name === name)) throw new Error('Application still exists after alleged purge');
        await markRetired(directory, name);
      });
    },
    async create({ name, route, routes = {}, env = {}, secrets = {}, sidecarSecrets = {}, compute, replicas, signal }) {
      const directory = pathFor(name);
      return locked(directory, async () => {
        const project = structuredClone(template);
        project.name = name;
        if (!routes || typeof routes !== 'object' || Array.isArray(routes)) throw new Error('routes must map endpoint names to customer addresses');
        if (project.endpoints && Object.keys(project.endpoints).length) {
          const names = Object.keys(project.endpoints).sort();
          const primary = project.primaryEndpoint || ['http', 'main', 'default'].find(name => name in project.endpoints) || names[0];
          for (const endpointName of Object.keys(routes)) if (!(endpointName in project.endpoints)) throw new Error('Route names an unknown endpoint');
          for (const [endpointName, endpoint] of Object.entries(project.endpoints)) {
            if (endpoint.exposure !== 'public') continue;
            const address = endpointName === primary ? route ?? routes[endpointName] : routes[endpointName];
            if (typeof address !== 'string' || !address) throw new Error(`Customer route required for public endpoint ${endpointName}`);
            endpoint.route = address;
            if (endpointName === primary) project.route = address;
          }
        } else if (project.version === 1 || project.endpoint?.exposure === 'public') {
          if (typeof route !== 'string' || !route) throw new Error('Customer route required for public application');
          project.route = route;
          if (project.endpoint) project.endpoint.route = route;
        } else if (route !== undefined) {
          throw new Error('Private application cannot receive a public route');
        }
        project.env = { ...project.env, ...environment(env) };
        secretBindings(project.secrets, secrets);
        sidecarSecretBindings(project, sidecarSecrets);
        if (compute) project.resources = resources(compute);
        if (replicas !== undefined) project.replicas = replicas;
        const metadata = { name, bundleDigest: digest };
        await assertNoPending(directory);
        try { await readFile(join(directory, 'retired.json')); throw new Error('Purged instance name cannot be reused without a new identity'); }
        catch (error) { if (error.code !== 'ENOENT') throw error; }
        try { await readFile(join(directory, 'instance.json')); throw new Error('Instance already exists in the SDK store'); }
        catch (error) { if (error.code !== 'ENOENT') throw error; }
        const result = await apply(directory, project, 'deploy', signal);
        await finish(directory, await observed(name, signal, directory), metadata);
        return result;
      });
    },
    async setEnvironment(name, changes, { signal } = {}) {
      const directory = pathFor(name);
      return locked(directory, async () => {
        await assertNoPending(directory);
        const { metadata, project } = await load(name);
        await assertCurrent(name, project, signal, directory);
        project.env = { ...project.env, ...environment(changes) };
        const result = await apply(directory, project, 'update', signal);
        await finish(directory, await observed(name, signal, directory), metadata);
        return result;
      });
    },
    async setCompute(name, compute, { signal } = {}) {
      const directory = pathFor(name);
      return locked(directory, async () => {
        await assertNoPending(directory);
        const { metadata, project } = await load(name);
        await assertCurrent(name, project, signal, directory);
        const next = resources(compute);
        project.resources = next;
        const candidate = await validate(directory, project, signal);
        await rm(candidate, { force: true });
        await pending(directory, 'compute', project);
        const result = await invoke(['scale', name, '--cpu', next.cpu, '--memory', next.memory], signal, directory);
        await finish(directory, await observed(name, signal, directory), metadata);
        return result;
      });
    },
    async expandSidecarVolume(name, sidecar, volume, size, { signal } = {}) {
      const directory = pathFor(name);
      return locked(directory, async () => {
        await assertNoPending(directory);
        const { metadata, project } = await load(name);
        await assertCurrent(name, project, signal, directory);
        const declared = project.sidecars?.[sidecar]?.volumes?.[volume];
        const oldBytes = volumeBytes(declared?.size);
        const newBytes = volumeBytes(size);
        if (oldBytes === undefined || newBytes === undefined || newBytes <= oldBytes) throw new Error('Sidecar volume must be declared and new size must be a valid expansion');
        declared.size = size;
        const result = await apply(directory, project, 'update', signal);
        await finish(directory, await observed(name, signal, directory), metadata);
        return result;
      });
    },
    async status(name, { signal } = {}) { identifier(name); return invoke(['status', name, '--json'], signal); },
  };
}
