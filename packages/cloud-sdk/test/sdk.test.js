import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import YAML from 'yaml';
import { createHostlierCloud } from '../index.js';

const bundle = `version: 2
name: vendor-template
source:
  image: ghcr.io/acme/tool:1.0.0
workload: service
endpoint:
  exposure: public
  port: 3000
  route: template.apps.example.com
health:
  path: /health
resources:
  cpu: "1"
  memory: 1Gi
env:
  NODE_ENV: production
secrets:
  API_KEY: secret://vendor-key
volumes:
  data:
    mount: /app/data
    size: 5Gi
sidecars:
  worker:
    source:
      image: ghcr.io/acme/worker:1.0.0
    workload: worker
    endpoint:
      exposure: none
    command: [worker]
    health:
      exec: [test, -f, /tmp/ready]
    secrets:
      WORKER_KEY: secret://template-worker-key
`;

const postgresBundle = `${bundle}workflow:
  steps:
    - id: database
      package: postgres
      config:
        mode: new-database
      secrets:
        connection: postgres-admin
  bindings:
    DATABASE_URL: database.url
`;

async function fixture(source = bundle) {
  const root = await mkdtemp(join(tmpdir(), 'hostlier-cloud-sdk-'));
  const bundlePath = join(root, 'vendor.hostlier');
  await writeFile(bundlePath, source);
  const calls = [];
  let deployed;
  let isReady = false;
  const run = async args => {
    const file = args.indexOf('--file');
    const project = file < 0 ? undefined : YAML.parse(await readFile(args[file + 1], 'utf8'));
    calls.push({ args, project });
    if (args[0] === 'deploy' || args[0] === 'update') {
      const image = 'ghcr.io/acme/tool@sha256:' + 'a'.repeat(64);
      deployed = { ...project, image, source: { image } };
    }
    if (args[0] === 'scale') deployed.resources = { cpu: args[3], memory: args[5] };
    if (args[0] === 'status' && args[1] === '--json') return { stdout: JSON.stringify({ applications: deployed ? [{ name: deployed.name }] : [], operations: [] }), stderr: '' };
    if (args[0] === 'status') return { stdout: JSON.stringify({ name: deployed.name, spec: deployed, ...(isReady ? { status: 'running', url: 'https://acme.apps.example.com', runtimeStatus: { running: true, readyReplicas: 1 } } : {}) }), stderr: '' };
    return { stdout: '{}', stderr: '' };
  };
  const options = { bundlePath, stateDirectory: join(root, 'state'), server: 'production', run };
  return { root, calls, options, sdk: await createHostlierCloud(options), setReady(value) { isReady = value; } };
}

test('customer creation and later changes retain the complete bundled project', async () => {
  const f = await fixture();
  await f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com', env: { CUSTOMER_ID: 'acme' } });
  await f.sdk.setCompute('acme-app', { cpu: '0.5', memory: '512Mi' });
  await f.sdk.setEnvironment('acme-app', { FEATURE_ENABLED: 'true' });
  assert.deepEqual(f.calls.map(call => call.args[0]), ['validate', 'deploy', 'status', 'status', 'validate', 'scale', 'status', 'status', 'validate', 'update', 'status']);
  assert.deepEqual(f.calls[5].args.slice(0, 6), ['scale', 'acme-app', '--cpu', '0.5', '--memory', '512Mi']);
  const deployedUpdate = f.calls[9].project;
  assert.equal(deployedUpdate.image, 'ghcr.io/acme/tool@sha256:' + 'a'.repeat(64));
  assert.equal(deployedUpdate.endpoint.route, 'acme.apps.example.com');
  assert.equal(deployedUpdate.health.path, '/health');
  assert.equal(deployedUpdate.secrets.API_KEY, 'secret://vendor-key');
  assert.equal(deployedUpdate.volumes.data.size, '5Gi');
  assert.equal(deployedUpdate.sidecars.worker.source.image, 'ghcr.io/acme/worker:1.0.0');
  assert.deepEqual(deployedUpdate.resources, { cpu: '0.5', memory: '512Mi' });
  assert.deepEqual(deployedUpdate.env, { NODE_ENV: 'production', CUSTOMER_ID: 'acme', FEATURE_ENABLED: 'true' });
});

test('create overrides only the declared PostgreSQL database and preserves it on update', async () => {
  const f = await fixture(postgresBundle);
  const desired = { name: 'acme-app', route: 'acme.apps.example.com', databaseName: 'workspace_acme' };
  await f.sdk.create(desired);
  const deployed = f.calls.find(call => call.args[0] === 'deploy').project;
  assert.equal(deployed.workflow.steps[0].config.database, 'workspace_acme');
  assert.equal(deployed.workflow.steps[0].config.mode, 'new-database');
  assert.equal(deployed.workflow.steps[0].package, 'postgres');
  assert.equal(deployed.workflow.steps[0].secrets.connection, 'postgres-admin');
  const saved = YAML.parse(await readFile(join(f.options.stateDirectory, 'acme-app', '.hostlier'), 'utf8'));
  assert.equal(saved.workflow.steps[0].config.database, 'workspace_acme');
  await f.sdk.ensure(desired);
  assert.equal(f.calls.filter(call => call.args[0] === 'deploy').length, 1);
  await f.sdk.setEnvironment('acme-app', { FEATURE_ENABLED: 'true' });
  assert.equal(f.calls.find(call => call.args[0] === 'update').project.workflow.steps[0].config.database, 'workspace_acme');
  await assert.rejects(f.sdk.ensure(desired), /differs from the retained create intent/);
  await f.sdk.create({ name: 'beta-app', route: 'beta.apps.example.com', databaseName: 'workspace_beta' });
  assert.equal(f.calls.filter(call => call.args[0] === 'deploy')[1].project.workflow.steps[0].config.database, 'workspace_beta');
});

test('omitting databaseName leaves the template ready for package-derived naming', async () => {
  const f = await fixture(postgresBundle);
  await f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com' });
  const step = f.calls.find(call => call.args[0] === 'deploy').project.workflow.steps[0];
  assert.deepEqual(step.config, { mode: 'new-database' });
  const saved = YAML.parse(await readFile(join(f.options.stateDirectory, 'acme-app', '.hostlier'), 'utf8'));
  assert.deepEqual(saved.workflow.steps[0].config, { mode: 'new-database' });
});

test('databaseName rejects unsafe names and templates without the declared PostgreSQL step', async () => {
  const f = await fixture(postgresBundle);
  for (const databaseName of ['', 'bad-name', 'Uppercase', '1starts_with_digit', 'a'.repeat(49), 4]) {
    await assert.rejects(f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com', databaseName }), /Invalid PostgreSQL database name/);
  }
  assert.equal(f.calls.length, 0);
  for (const source of [bundle, postgresBundle.replace('package: postgres', 'package: redis'), postgresBundle.replace('mode: new-database', 'mode: new-instance')]) {
    const other = await fixture(source);
    await assert.rejects(other.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com', databaseName: 'workspace_acme' }), /does not declare a PostgreSQL/);
    assert.equal(other.calls.length, 0);
  }
});

test('an interrupted PostgreSQL create retains the resolved database for recovery', async () => {
  const f = await fixture(postgresBundle);
  const desired = { name: 'acme-app', route: 'acme.apps.example.com', databaseName: 'workspace_acme' };
  const run = f.options.run;
  f.options.run = async (args, options) => {
    const result = await run(args, options);
    if (args[0] === 'deploy') throw new Error('connection interrupted after acceptance');
    return result;
  };
  const sdk = await createHostlierCloud(f.options);
  await assert.rejects(sdk.create(desired), /connection interrupted/);
  const pending = JSON.parse(await readFile(join(f.options.stateDirectory, 'acme-app', 'pending.json'), 'utf8'));
  assert.equal(pending.project.workflow.steps[0].config.database, 'workspace_acme');
  f.setReady(true);
  await sdk.recoverPending('acme-app', 'ghcr.io/acme/tool@sha256:' + 'a'.repeat(64));
  await sdk.ensure(desired);
  const saved = YAML.parse(await readFile(join(f.options.stateDirectory, 'acme-app', '.hostlier'), 'utf8'));
  assert.equal(saved.workflow.steps[0].config.database, 'workspace_acme');
});

test('ensure creates once and observes only the same complete project', async () => {
  const f = await fixture(postgresBundle);
  const desired = { name: 'acme-app', route: 'acme.apps.example.com', databaseName: 'workspace_acme', env: { CUSTOMER_ID: 'acme', REGION: 'uk' } };
  const initial = await f.sdk.ensure(desired);
  assert.equal(initial.ready, false);
  f.setReady(true);
  const repeated = await f.sdk.ensure({ ...desired, env: { REGION: 'uk', CUSTOMER_ID: 'acme' } });
  assert.equal(repeated.ready, true);
  assert.equal(f.calls.filter(call => call.args[0] === 'deploy').length, 1);
  assert.equal(f.calls.filter(call => call.args[0] === 'validate').length, 1);
  for (const changed of [
    { ...desired, route: 'other.apps.example.com' },
    { ...desired, databaseName: 'workspace_other' },
    { ...desired, env: { CUSTOMER_ID: 'other' } },
  ]) {
    await assert.rejects(f.sdk.ensure(changed), /differs from the retained create intent/);
  }
  assert.equal(f.calls.filter(call => call.args[0] === 'deploy').length, 1);
});

test('ensure refuses uncertain creation and out-of-band changes', async () => {
  const f = await fixture(postgresBundle);
  const desired = { name: 'acme-app', route: 'acme.apps.example.com', databaseName: 'workspace_acme' };
  const run = f.options.run;
  f.options.run = async (args, options) => {
    const result = await run(args, options);
    if (args[0] === 'deploy') throw new Error('connection interrupted after acceptance');
    return result;
  };
  const sdk = await createHostlierCloud(f.options);
  await assert.rejects(sdk.ensure(desired), /connection interrupted/);
  await assert.rejects(sdk.ensure(desired), /uncertain result/);
  f.setReady(true);
  await sdk.recoverPending('acme-app', 'ghcr.io/acme/tool@sha256:' + 'a'.repeat(64));
  const originalRun = f.options.run;
  f.options.run = async (args, options) => {
    const result = await originalRun(args, options);
    if (args[0] !== 'status' || args[1] === '--json') return result;
    const status = JSON.parse(result.stdout);
    status.spec.env.NODE_ENV = 'changed-outside-sdk';
    return { ...result, stdout: JSON.stringify(status) };
  };
  const changed = await createHostlierCloud(f.options);
  await assert.rejects(changed.ensure(desired), /changed outside this SDK/);
  assert.equal(f.calls.filter(call => call.args[0] === 'deploy').length, 1);
});

test('a new bundle cannot silently alter an existing instance', async () => {
  const f = await fixture();
  await f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com' });
  await writeFile(f.options.bundlePath, bundle.replace('1.0.0', '2.0.0'));
  const newer = await createHostlierCloud(f.options);
  await assert.rejects(newer.setEnvironment('acme-app', { FEATURE_ENABLED: 'true' }), /bundle differs/);
  assert.equal(f.calls.length, 3);
});

test('an interrupted mutation retains its intent and blocks a second mutation', async () => {
  const f = await fixture();
  await f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com' });
  const originalRun = f.options.run;
  f.options.run = async (args, options) => {
    if (args[0] === 'update') throw new Error('connection interrupted');
    return originalRun(args, options);
  };
  const sdk = await createHostlierCloud(f.options);
  await assert.rejects(sdk.setEnvironment('acme-app', { FEATURE_ENABLED: 'true' }), /connection interrupted/);
  const pending = JSON.parse(await readFile(join(f.options.stateDirectory, 'acme-app', 'pending.json'), 'utf8'));
  assert.equal(pending.project.env.FEATURE_ENABLED, 'true');
  await assert.rejects(sdk.setCompute('acme-app', { cpu: '0.5', memory: '512Mi' }), /uncertain result/);
});

test('an external deployment stops an environment update before planning', async () => {
  const f = await fixture();
  await f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com' });
  const originalRun = f.options.run;
  f.options.run = async (args, options) => {
    const result = await originalRun(args, options);
    if (args[0] !== 'status') return result;
    const status = JSON.parse(result.stdout);
    status.spec.env.NODE_ENV = 'changed-outside-sdk';
    return { ...result, stdout: JSON.stringify(status) };
  };
  const sdk = await createHostlierCloud(f.options);
  await assert.rejects(sdk.setEnvironment('acme-app', { FEATURE_ENABLED: 'true' }), /changed outside this SDK/);
  assert.equal(f.calls.filter(call => call.args[0] === 'update').length, 0);
});

test('invalid environment entries do not submit a controller operation', async () => {
  const f = await fixture();
  await assert.rejects(f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com', env: { 'INVALID-KEY': 'x' } }), /Invalid environment/);
  assert.equal(f.calls.length, 0);
});

test('per-instance stored secrets replace only declared bindings', async () => {
  const f = await fixture();
  await f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com', secrets: { API_KEY: 'secret://acme-key' } });
  assert.equal(f.calls[1].project.secrets.API_KEY, 'secret://acme-key');
  await assert.rejects(f.sdk.create({ name: 'other-app', route: 'other.apps.example.com', secrets: { UNKNOWN: 'secret://key' } }), /does not declare/);
  await assert.rejects(f.sdk.create({ name: 'third-app', route: 'third.apps.example.com', secrets: { API_KEY: 'plaintext' } }), /stored secret/);
  assert.equal(f.calls.length, 3);
});

test('per-instance sidecar secrets replace declared bindings only', async () => {
  const f = await fixture();
  await f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com', sidecarSecrets: { worker: { WORKER_KEY: 'secret://acme-worker-key' } } });
  assert.equal(f.calls[1].project.sidecars.worker.secrets.WORKER_KEY, 'secret://acme-worker-key');
  await assert.rejects(f.sdk.create({ name: 'other-app', route: 'other.apps.example.com', sidecarSecrets: { unknown: { WORKER_KEY: 'secret://key' } } }), /does not declare sidecar/);
  await assert.rejects(f.sdk.create({ name: 'third-app', route: 'third.apps.example.com', sidecarSecrets: { worker: { WORKER_KEY: 'plaintext' } } }), /stored secret/);
  assert.equal(f.calls.length, 3);
});

test('secret writes use private input and preserve controller errors', async () => {
  const f = await fixture();
  let seen;
  f.options.run = async (args, options) => { seen = { args, input: options.input }; return { stdout: '{}', stderr: '' }; };
  const sdk = await createHostlierCloud(f.options);
  await sdk.setSecret('acme-key', 'private-secret-marker');
  assert.deepEqual(seen.args, ['secret', 'set', 'acme-key', '--stdin', '--server', 'production']);
  assert.equal(seen.input, 'private-secret-marker');
  assert(!seen.args.join(' ').includes(seen.input));
  const controllerError = Object.assign(new Error('Too many retained approval requests'), { code: 'APPROVAL_LIMIT' });
  f.options.run = async () => { throw controllerError; };
  const failing = await createHostlierCloud(f.options);
  await assert.rejects(failing.setSecret('acme-key', 'private-secret-marker'), error => error === controllerError);
  await assert.rejects(failing.setInstanceSecret('acme-app', 'acme-key', 'private-secret-marker'), error => error === controllerError);
});

test('stored secret deletion uses the existing protected CLI command', async () => {
  const f = await fixture();
  await f.sdk.deleteSecret('acme-s3-access');
  assert.deepEqual(f.calls[0].args, ['secret', 'delete', 'acme-s3-access', '--server', 'production']);
  await assert.rejects(f.sdk.deleteSecret('bad/name'), /Invalid Hostlier/);
  assert.equal(f.calls.length, 1);
});

test('instance secret registration passes owner only through CLI arguments', async () => {
  const f = await fixture();
  let seen;
  f.options.run = async (args, options) => { seen = { args, input: options.input }; return { stdout: '{}', stderr: '' }; };
  const sdk = await createHostlierCloud(f.options);
  await sdk.setInstanceSecret('acme-app', 'acme-app-key', 'private-value');
  assert.deepEqual(seen.args, ['secret', 'set', 'acme-app-key', '--stdin', '--app', 'acme-app', '--server', 'production']);
  assert.equal(seen.input, 'private-value');
});

test('pending recovery requires reviewed digest, readiness and exact desired project', async () => {
  const f = await fixture();
  const run = f.options.run;
  f.options.run = async (args, options) => {
    const result = await run(args, options);
    if (args[0] === 'deploy') throw new Error('connection interrupted after acceptance');
    return result;
  };
  const sdk = await createHostlierCloud(f.options);
  await assert.rejects(sdk.create({ name: 'acme-app', route: 'acme.apps.example.com' }), /connection interrupted/);
  const image = 'ghcr.io/acme/tool@sha256:' + 'a'.repeat(64);
  await assert.rejects(sdk.recoverPending('acme-app', image), /not confirmed ready/);
  f.setReady(true);
  await assert.rejects(sdk.recoverPending('acme-app', 'ghcr.io/acme/tool@sha256:' + 'b'.repeat(64)), /not confirmed ready/);
  const status = await sdk.recoverPending('acme-app', image);
  assert.equal(status.ready, true);
  assert.equal(status.url, 'https://acme.apps.example.com');
  await assert.rejects(readFile(join(f.options.stateDirectory, 'acme-app', 'pending.json')), { code: 'ENOENT' });
});

test('an interrupted purge needs the exact successful controller operation', async () => {
  const f = await fixture();
  await f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com' });
  const originalRun = f.options.run;
  let operation = { id: 'op-delete', kind: 'action', name: 'acme-app', action: 'delete', status: 'succeeded', purge: false };
  f.options.run = async (args, options) => {
    if (args[0] === 'delete') throw new Error('connection interrupted');
    if (args[0] === 'operation') return { stdout: JSON.stringify(operation), stderr: '' };
    if (args[0] === 'status' && args[1] === '--json' && operation.purge) return { stdout: JSON.stringify({ applications: [], operations: [operation] }), stderr: '' };
    return originalRun(args, options);
  };
  const sdk = await createHostlierCloud(f.options);
  await assert.rejects(sdk.purge('acme-app'), /connection interrupted/);
  await assert.rejects(sdk.confirmPurge('acme-app', 'op-delete'), /did not confirm/);
  operation = { ...operation, purge: true };
  await sdk.confirmPurge('acme-app', 'op-delete');
  assert.equal(JSON.parse(await readFile(join(f.options.stateDirectory, 'acme-app', 'retired.json'), 'utf8')).disposition, 'purged');
  await assert.rejects(sdk.create({ name: 'acme-app', route: 'acme.apps.example.com' }), /cannot be reused/);
});

test('sidecar volume expansion preserves the original bundle for later scaling', async () => {
  const source = bundle.replace('    secrets:\n      WORKER_KEY:', '    volumes:\n      data:\n        mount: /data\n        size: 1Gi\n    secrets:\n      WORKER_KEY:');
  const f = await fixture(source);
  await f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com' });
  const updates = () => f.calls.filter(call => call.args[0] === 'update').length;
  await assert.rejects(f.sdk.expandSidecarVolume('acme-app', 'worker', 'data', '512Mi'), /valid expansion/);
  await assert.rejects(f.sdk.expandSidecarVolume('acme-app', 'worker', 'data', '1Gi'), /valid expansion/);
  assert.equal(updates(), 0);
  await f.sdk.expandSidecarVolume('acme-app', 'worker', 'data', '10Gi');
  assert.equal(f.calls.findLast(call => call.args[0] === 'update').project.sidecars.worker.volumes.data.size, '10Gi');
  await f.sdk.setCompute('acme-app', { cpu: '0.5', memory: '512Mi' });
});

test('pending purge recovery confirms a new operation or safely retries', async () => {
  const f = await fixture();
  await f.sdk.create({ name: 'acme-app', route: 'acme.apps.example.com' });
  const originalRun = f.options.run;
  let failed = true;
  let removed = false;
  let accepted = false;
  f.options.run = async (args, options) => {
    if (args[0] === 'status' && args[1] === '--json') return { stdout: JSON.stringify({ applications: removed ? [] : [{ name: 'acme-app' }], operations: accepted ? [{ id: 'op-purge', kind: 'action', name: 'acme-app', action: 'delete', purge: true, status: 'succeeded' }] : [] }), stderr: '' };
    if (args[0] === 'delete') {
      if (failed) throw new Error('connection interrupted');
      removed = true;
      return { stdout: '{}', stderr: '' };
    }
    return originalRun(args, options);
  };
  const sdk = await createHostlierCloud(f.options);
  await assert.rejects(sdk.purge('acme-app'), /connection interrupted/);
  failed = false;
  await sdk.recoverPendingPurge('acme-app');
  assert.equal(removed, true);

  const g = await fixture();
  await g.sdk.create({ name: 'other-app', route: 'other.apps.example.com' });
  const base = g.options.run;
  g.options.run = async (args, options) => {
    if (args[0] === 'delete') throw new Error('connection interrupted');
    if (args[0] === 'status' && args[1] === '--json') return { stdout: JSON.stringify({ applications: accepted ? [] : [{ name: 'other-app' }], operations: accepted ? [{ id: 'new-purge', kind: 'action', name: 'other-app', action: 'delete', purge: true, status: 'succeeded' }] : [] }), stderr: '' };
    return base(args, options);
  };
  const recovering = await createHostlierCloud(g.options);
  await assert.rejects(recovering.purge('other-app'), /connection interrupted/);
  accepted = true;
  await recovering.recoverPendingPurge('other-app');
  assert.equal(JSON.parse(await readFile(join(g.options.stateDirectory, 'other-app', 'retired.json'), 'utf8')).disposition, 'purged');
});
