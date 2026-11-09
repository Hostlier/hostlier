import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, randomBytes, verify } from 'node:crypto';
import { mkdtemp, readFile, writeFile } from 'node:fs/promises';
import grpc from '@grpc/grpc-js';
import { Controller } from '../grpc.js';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { createHostlierCloud } from '../index.js';
import { controllerRunner } from '../controller.js';

const epoch = 'a'.repeat(64);
const image = `ghcr.io/acme/tool@sha256:${'b'.repeat(64)}`;
const bundle = `version: 2\nname: template\nsource:\n  image: ghcr.io/acme/tool:latest\nworkload: service\nendpoint:\n  exposure: public\n  port: 3000\n  route: template.example.test\nresources:\n  cpu: "1"\n  memory: 1Gi\nenv:\n  MODE: production\nsecrets:\n  API_KEY: secret://template-key\nsidecars:\n  storage:\n    source:\n      image: example.com/storage:latest\n    secrets:\n      ROOT_KEY: secret://template-storage-key\n`;

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), 'hostlier-direct-node-'));
  const bundlePath = join(root, '.hostlier');
  const credentialFile = join(root, 'controller-credential.json');
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const seed = privateKey.export({ format: 'der', type: 'pkcs8' }).subarray(-32);
  const publicBytes = publicKey.export({ format: 'der', type: 'spki' }).subarray(-32);
  const signedKey = Buffer.concat([seed, publicBytes]);
  const credential = 'device-v1:' + Buffer.from(JSON.stringify({ token: 'fixture-token', privateKey: signedKey.toString('base64').replace(/=+$/, '') })).toString('base64').replace(/=+$/, '');
  await writeFile(bundlePath, bundle, { mode: 0o600 });
  const state = { apps: new Map(), operations: new Map(), plans: new Map(), approvals: new Map(), requests: [], secrets: new Map(), redirects: false, rejectPlan: false, planning: new Map() };
  const server = new grpc.Server();
  function dispatch(call, method, path, bytes, reply) {
    try {
      const request={method,headers:call.metadata.getMap()};
      const raw=bytes ?? Buffer.alloc(0);
      const body=raw.length ? JSON.parse(raw) : undefined;
      if (path === '/v1/email/session') { reply(200, { epoch }); return; }
      const fields = [request.headers['x-hostlier-epoch'], request.method, path, request.headers['x-hostlier-time'], request.headers['x-hostlier-nonce'], createHash('sha256').update(raw).digest('hex')];
      const signed = Buffer.from(['hostlier-device-v1', ...fields].join('\0'));
      const signature = Buffer.from(request.headers['x-hostlier-signature'] ?? '', 'base64');
      if (request.headers.authorization !== 'Bearer fixture-token' || fields[0] !== epoch || !verify(null, signed, publicKey, signature)) { reply(401, { error: { code: 'UNAUTHORIZED', message: 'Invalid device proof' } }); return; }
      state.requests.push({ method: request.method, path, body, headers: request.headers });
      if (state.redirects && path === '/v1/status') { reply(403,{error:{code:'DENIED',message:'Access denied'}}); return; }
      if (request.method === 'POST' && path === '/v1/planning') {
        assert.match(body.requestId, /^[a-f0-9]{32}$/);
        const id = `planning-${state.planning.size + 1}`;
        state.planning.set(id, body.request.spec);
        reply(200, { id, status: 'queued' }); return;
      }
      if (request.method === 'GET' && path.startsWith('/v1/planning/')) {
        const spec = state.planning.get(path.split('/')[3]);
        const id = `plan-${state.plans.size + 1}`;
        state.plans.set(id, { spec });
        reply(200, { id: path.split('/')[3], status: 'completed', plan: { id, fingerprint: `fingerprint-${id}` } }); return;
      }
      if (request.method === 'POST' && path === '/v1/plans') {
        if (state.rejectPlan) { reply(400, { error: { code: state.rejectPlan === true ? 'INVALID_CONFIG' : state.rejectPlan, message: 'Plan rejected before apply' } }); return; }
        const id = `plan-${state.plans.size + 1}`;
        state.plans.set(id, body);
        reply(200, { id, fingerprint: `fingerprint-${id}` }); return;
      }
      if (request.method === 'POST' && path === '/v1/approvals') {
        const id = `approval-${state.approvals.size + 1}`;
        state.approvals.set(id, body);
        reply(200, { id, status: 'approved' }); return;
      }
      const approval = /^\/v1\/approvals\/([^/]+)\/execute$/.exec(path);
      if (request.method === 'POST' && approval) {
        const accepted = state.approvals.get(approval[1]);
        if (!accepted) { reply(404, { error: { code: 'NOT_FOUND', message: 'Approval unavailable' } }); return; }
        const id = `operation-${state.operations.size + 1}`;
        const approved = accepted.body;
        let operation;
        if (accepted.path.startsWith('/v1/plans/')) {
          const planId = accepted.path.split('/')[3];
          const plan = state.plans.get(planId);
          assert.equal(approved.fingerprint, `fingerprint-${planId}`);
          assert.match(approved.requestId, /^[a-f0-9]{32}$/);
          const spec = structuredClone(plan.spec);
          spec.image = image;
          spec.source = { image };
          state.apps.set(spec.name, { name: spec.name, status: 'running', url: `https://${spec.route}`, spec, runtimeStatus: { running: true, readyReplicas: 1 } });
          operation = { id, kind: 'deploy', name: spec.name, status: 'succeeded' };
        } else if (accepted.path.startsWith('/v1/apps/')) {
          const name = accepted.path.split('/')[3];
          assert.deepEqual(approved, { action: 'delete', purge: true });
          state.apps.delete(name);
          operation = { id, kind: 'action', name, action: 'delete', purge: true, status: 'succeeded' };
        } else if (accepted.path.startsWith('/v1/secrets/')) {
          const name = accepted.path.split('/')[3];
          if (accepted.method === 'PUT') state.secrets.set(name, approved);
          else state.secrets.delete(name);
          reply(200, { name }); return;
        } else { reply(404, {}); return; }
        state.operations.set(id, operation);
        reply(200, { operationId: id }); return;
      }
      if (request.method === 'GET' && path === '/v1/status') { reply(200, { applications: [...state.apps.values()], operations: [...state.operations.values()] }); return; }
      if (request.method === 'GET' && path.startsWith('/v1/apps/')) { const app = state.apps.get(path.split('/')[3]); reply(app ? 200 : 404, app ?? { error: { code: 'NOT_FOUND', message: 'Application unavailable' } }); return; }
      if (request.method === 'GET' && path.startsWith('/v1/operations/')) { const operation = state.operations.get(path.split('/')[3]); reply(operation ? 200 : 404, operation ?? { error: { code: 'NOT_FOUND', message: 'Operation unavailable' } }); return; }
      reply(404, { error: { code: 'NOT_FOUND', message: 'Endpoint unavailable' } });
    } catch(error) {reply(500,{error:{code:'FIXTURE',message:error.message}});}
  }
  function rpcError(value) {
    const metadata = new grpc.Metadata();
    metadata.set('hostlier-error-code',value.error?.code ?? 'NOT_FOUND');
    return Object.assign(new Error(value.error?.message ?? 'Not found'),{code:grpc.status.FAILED_PRECONDITION,metadata});
  }
  function watch(call,kind,path) {
    dispatch(call,'GET',path,Buffer.alloc(0),(status,value)=>{
      if(status!==200) {call.emit('error',rpcError(value));return;}
      call.write({kind,jsonData:Buffer.from(JSON.stringify(value))});call.end();
    });
  }
  server.addService(Controller.service,{
    command(call,done) {
      dispatch(call,call.request.method,call.request.route,call.request.jsonBody,(status,value)=>{
        if(status!==200)done(rpcError(value));
        else done(null,{jsonBody:Buffer.from(JSON.stringify(value))});
      });
    },
    watchOperation(call) {
      const id = call.request.operationId;
      if (state.watchCursors) {
        state.watchCursors.push(call.request.cursor || '');
        if (state.watchCursors.length === 1) {
          const log = { operationId: id, sequence: 7 };
          const frame = { kind: 'log', cursor: id + ':7', jsonData: Buffer.from(JSON.stringify(log)) };
          // End the stream before the terminal snapshot. The SDK must resume
          // observation, without a progress lookup or another execute.
          if (state.resetCode !== undefined) call.write(frame, () => call.call.stream.close(state.resetCode));
          else { call.write(frame); call.end(); }
          return;
        }
      }
      watch(call, 'operation', '/v1/operations/' + id + '/events');
    },
    watchJob(call){watch(call,'receipt',call.request.route);},
  });
  const port=await new Promise((resolve,reject)=>server.bindAsync('127.0.0.1:0',grpc.ServerCredentials.createInsecure(),(err,port)=>err?reject(err):resolve(port)));
  t.after(()=>server.forceShutdown());
  const controllerUrl = `http://127.0.0.1:${port}`;
  await writeFile(credentialFile, JSON.stringify({ controllerUrl, credential, clientId: randomBytes(16).toString('hex') }), { mode: 0o600 });
  const options = { bundlePath, stateDirectory: join(root, 'instances'), credentialFile, allowInsecureLocalhost: true };
  return { state, options, credentialFile, sdk: await createHostlierCloud(options) };
}

test('Node SDK provisions and purges through signed controller approvals without a CLI', async t => {
  const f = await fixture(t);
  await f.sdk.setInstanceSecret('acme-app', 'acme-key', 'private-value');
  assert.equal(f.state.secrets.get('acme-key').owner, 'acme-app');
  const created = await f.sdk.create({ name: 'acme-app', route: 'acme.example.test', secrets: { API_KEY: 'secret://acme-key' } });
  assert.equal(JSON.parse(created.stdout).operationId, 'operation-1');
  const planned = f.state.plans.get('plan-1').spec;
  assert.equal(planned.secrets.API_KEY, 'acme-key');
  assert.equal(planned.sidecars.storage.secrets.ROOT_KEY, 'template-storage-key');
  assert.equal((await f.sdk.statusSnapshot('acme-app')).ready, true);
  await f.sdk.setCompute('acme-app', { cpu: '0.5', memory: '512Mi' });
  await f.sdk.setEnvironment('acme-app', { FEATURE: 'on' });
  assert.equal(f.state.apps.get('acme-app').spec.env.FEATURE, 'on');
  assert.equal(f.state.apps.get('acme-app').spec.resources.cpu, '0.5');
  const approved = [...f.state.approvals.values()];
  assert(approved.some(item => item.path === '/v1/apps/acme-app/action' && item.body.purge === true) === false);
  await f.sdk.purge('acme-app');
  assert.equal(f.state.apps.has('acme-app'), false);
  assert(approved.every(item => item.path.startsWith('/v1/plans/') || item.path.startsWith('/v1/secrets/')));
  assert(f.state.requests.every(item => item.headers['x-hostlier-signature']));
  assert(f.state.requests.every(item => !item.path.includes('private-value')));
  assert.equal(JSON.parse(await readFile(join(f.options.stateDirectory, 'acme-app', 'retired.json'), 'utf8')).disposition, 'purged');
});

test('a definite rejected plan does not retain an uncertain pending intent', async t => {
  const f = await fixture(t);
  for (const [index, code] of ['INVALID_CONFIG', 'IMAGE_RESOLUTION', 'REGISTRY_CREDENTIALS'].entries()) {
    const name = `rejected-app-${index}`;
    f.state.rejectPlan = code;
    await assert.rejects(f.sdk.create({ name, route: `${name}.example.test` }), /Plan rejected before apply/);
    await assert.rejects(readFile(join(f.options.stateDirectory, name, 'pending.json'), 'utf8'), { code: 'ENOENT' });
  }
});

test('direct transport rejects unsigned credentials and preserves controller errors', async t => {
  const f = await fixture(t);
  const original = await readFile(f.credentialFile, 'utf8');
  await writeFile(f.credentialFile, JSON.stringify({ controllerUrl: 'https://controller.example', credential: 'legacy-bearer' }), { mode: 0o600 });
  await assert.rejects(createHostlierCloud(f.options), /enrolled device credential/);
  await writeFile(f.credentialFile, JSON.stringify({ controllerUrl: 'http://outside.example', credential: 'legacy-bearer' }), { mode: 0o600 });
  await assert.rejects(createHostlierCloud(f.options), /HTTPS origin/);
  await writeFile(f.credentialFile, original, { mode: 0o600 });
  const run = await controllerRunner(f.options);
  f.state.redirects = true;
  await assert.rejects(run(['status', '--json']), {code:'DENIED'});
});

test('workflow deployments use durable planning before the retained approval', async t => {
  const f = await fixture(t);
  const project = { version: 2, name: 'workflow-app', source: { image: 'ghcr.io/acme/tool:latest' }, workload: 'service', workflow: { steps: [] } };
  const file = join(f.options.stateDirectory, '..', 'workflow.hostlier');
  await writeFile(file, JSON.stringify(project), { mode: 0o600 });
  const run = await controllerRunner(f.options);
  const result = await run(['deploy', '--file', file]);
  assert.equal(JSON.parse(result.stdout).operationId, 'operation-1');
  assert(f.state.requests.some(item => item.path === '/v1/planning'));
  assert(f.state.requests.some(item => item.path === '/v1/planning/planning-1'));
  assert(!f.state.requests.some(item => item.path === '/v1/plans'));
});

for (const resetCode of [undefined, 0, 2]) test(`purge resumes from its cursor without submitting twice (HTTP/2 reset: ${resetCode ?? 'clean end'})`, async t => {
  const f = await fixture(t);
  await f.sdk.create({ name: 'resume', route: 'resume.example.test' });
  f.state.resetCode = resetCode;
  f.state.watchCursors = [];
  const receipt = await f.sdk.purge('resume');
  assert.equal(JSON.parse(receipt.stdout).operationId, 'operation-2');
  assert.deepEqual(f.state.watchCursors, ['', 'operation-2:7']);
  assert.equal(f.state.operations.size, 2);
  assert.equal(f.state.requests.filter(r => r.path.endsWith('/execute')).length, 2);
  assert.equal(f.state.requests.some(r => r.path === '/v1/operations/operation-2'), false);
});
