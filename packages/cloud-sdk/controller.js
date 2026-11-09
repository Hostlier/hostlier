import { createHash, createPrivateKey, createPublicKey, randomBytes, sign, timingSafeEqual } from 'node:crypto';
import { readFile, stat } from 'node:fs/promises';
import { basename } from 'node:path';
import YAML from 'yaml';
import { controllerRPC } from './grpc.js';

const keyPrefix = Buffer.from('302e020100300506032b657004220420', 'hex');
const storedSecretName = /^[A-Za-z_][A-Za-z0-9_.-]{0,127}$/;
const environmentName = /^[A-Za-z_][A-Za-z0-9_]*$/;

function requestId() { return randomBytes(16).toString('hex'); }

// Match the CLI's secret:// lowering before submitting a controller plan.
function lowerStoredReferences(project, allowNormalized) {
  if (project.secrets !== undefined) {
    if (!project.secrets || typeof project.secrets !== 'object' || Array.isArray(project.secrets)) throw new Error('Invalid Hostlier stored secret bindings');
    for (const [key, raw] of Object.entries(project.secrets)) {
      if (typeof raw !== 'string') throw new Error('Invalid Hostlier stored secret binding');
      let name = raw;
      if (name.startsWith('secret://')) name = name.slice('secret://'.length);
      else if (!allowNormalized) throw new Error('Cloud bundles require explicit stored secret references');
      if (!environmentName.test(key) || !storedSecretName.test(name)) throw new Error('Invalid Hostlier stored secret binding');
      project.secrets[key] = name;
    }
  }
  if (project.sidecars !== undefined) {
    if (!project.sidecars || typeof project.sidecars !== 'object' || Array.isArray(project.sidecars)) throw new Error('Invalid Hostlier sidecar bindings');
    for (const sidecar of Object.values(project.sidecars)) {
      if (!sidecar || typeof sidecar !== 'object' || Array.isArray(sidecar)) throw new Error('Invalid Hostlier sidecar bindings');
      lowerStoredReferences(sidecar, allowNormalized);
    }
  }
  return project;
}
function pause(signal) { return new Promise((resolve, reject) => {
  if (signal?.aborted) { reject(signal.reason); return; }
  const done = () => { signal?.removeEventListener('abort', aborted); resolve(); };
  const aborted = () => { clearTimeout(timer); reject(signal.reason); };
  const timer = setTimeout(done, 500);
  signal?.addEventListener('abort', aborted, { once: true });
}); }

function credential(value) {
  if (typeof value !== 'string' || !value.startsWith('device-v1:')) throw new Error('Hostlier SDK requires an enrolled device credential');
  let parsed;
  try { parsed = JSON.parse(Buffer.from(value.slice(10), 'base64').toString('utf8')); }
  catch (cause) { throw new Error('Invalid Hostlier device credential', { cause }); }
  if (typeof parsed?.token !== 'string' || !parsed.token || typeof parsed.privateKey !== 'string') throw new Error('Invalid Hostlier device credential');
  const raw = Buffer.from(parsed.privateKey, 'base64');
  if (raw.length !== 64) throw new Error('Invalid Hostlier device private key');
  const key = createPrivateKey({ key: Buffer.concat([keyPrefix, raw.subarray(0, 32)]), format: 'der', type: 'pkcs8' });
  const publicKey = createPublicKey(key).export({ format: 'der', type: 'spki' }).subarray(-32);
  if (!timingSafeEqual(raw.subarray(32), publicKey)) throw new Error('Invalid Hostlier device private key');
  return { token: parsed.token, key };
}

function controllerOrigin(value, allowInsecureLocalhost) {
  let url;
  try { url = new URL(value); } catch (cause) { throw new Error('Invalid Hostlier controller URL', { cause }); }
  const local = url.protocol === 'http:' && ['127.0.0.1', '::1', '[::1]', 'localhost'].includes(url.hostname);
  if ((url.protocol !== 'https:' && !(allowInsecureLocalhost && local)) || url.username || url.password || url.search || url.hash || url.pathname !== '/' || !url.hostname) throw new Error('Hostlier controller requires an HTTPS origin');
  return url.origin;
}

/** Build an API runner for the same controller routes and approval protocol as the CLI. */
export async function controllerRunner({ credentialFile, allowInsecureLocalhost = false }) {
  if (typeof credentialFile !== 'string' || !credentialFile) throw new Error('credentialFile is required for direct Hostlier access');
  const info = await stat(credentialFile);
  if (!info.isFile() || info.mode & 0o077) throw new Error('Hostlier credential file must be private (mode 0600)');
  let settings;
  try { settings = JSON.parse(await readFile(credentialFile, 'utf8')); }
  catch (cause) { throw new Error('Invalid Hostlier credential file', { cause }); }
  const origin = controllerOrigin(settings.controllerUrl, allowInsecureLocalhost);
  const device = credential(settings.credential);

  const rpc = controllerRPC(origin);
  async function auth(method, path, bytes, signal) {
    const headers = {};
    const session = await rpc.command('GET','/v1/email/session',Buffer.alloc(0),{},signal);
    if (typeof session.epoch !== 'string' || !/^[a-f0-9]{64}$/i.test(session.epoch)) throw new Error('Invalid Hostlier device session');
    const time = String(Math.floor(Date.now() / 1000));
    const nonce = randomBytes(32).toString('hex');
    const digest = createHash('sha256').update(bytes).digest('hex');
    const message = Buffer.from(['hostlier-device-v1', session.epoch, method, path, time, nonce, digest].join('\0'));
    headers.authorization = `Bearer ${device.token}`;
    headers['x-hostlier-epoch'] = session.epoch;
    headers['x-hostlier-time'] = time;
    headers['x-hostlier-nonce'] = nonce;
    headers['x-hostlier-signature'] = sign(null, message, device.key).toString('base64').replace(/=+$/, '');
    return headers;
  }
  async function commandJSON(method, path, body, signal) {
    const bytes = body === undefined ? Buffer.alloc(0) : Buffer.from(JSON.stringify(body));
    return rpc.command(method,path,bytes,await auth(method,path,bytes,signal),signal);
  }
  function watch(kind,id,path,signal) {
    return rpc.watch(kind,id,path,() => auth('GET',path,Buffer.alloc(0),signal),signal);
  }

  async function approved(method, path, body, signal) {
    let approval = await commandJSON('POST', '/v1/approvals', { method, path, body }, signal);
    if (typeof approval.id !== 'string' || !approval.id) throw new Error('Hostlier returned no approval ID');
    const approvalId = approval.id;
    if (approval.status === 'pending') approval = await watch('receipt',approvalId,`/v1/approvals/${encodeURIComponent(approvalId)}`,signal);
    if (!['approved', 'completed'].includes(approval.status)) throw new Error(`Hostlier approval ${approvalId} is ${approval.status}`);
    const execute = () => commandJSON('POST', `/v1/approvals/${encodeURIComponent(approvalId)}/execute`, {}, signal);
    try { return await execute(); }
    catch (error) {
      if (error.code === 'CONNECTION') return execute();
      throw error;
    }
  }

  async function waitOperation(id, signal) {
    if (typeof id !== 'string' || !id) throw new Error('Hostlier returned no operation ID');
    const operation = await watch('operation',id,`/v1/operations/${encodeURIComponent(id)}/events`,signal);
    if (['succeeded','completed','success'].includes(operation.status)) return operation;
    const error = new Error(`Hostlier operation ${id} ${operation.status}`);
    error.operationId = id;
    throw error;
  }

  async function reviewedPlan(body, signal) {
    if (!body.spec?.workflow) return commandJSON('POST', '/v1/plans', body, signal);
    const request = { requestId: requestId(), request: body };
    let planning;
    for (;;) {
      try { planning = await commandJSON('POST', '/v1/planning', request, signal); break; }
      catch (error) {
        if (error.code !== 'CONNECTION') throw error;
        await pause(signal);
      }
    }
    if (typeof planning.id !== 'string' || !planning.id) throw new Error('Hostlier returned no planning ID');
    const planningId = planning.id;
    if (['queued','running'].includes(planning.status)) planning = await watch('receipt',planningId,`/v1/planning/${encodeURIComponent(planningId)}`,signal);
    if (planning.error) throw Object.assign(new Error(planning.error.message ?? 'Hostlier planning failed'),{code:planning.error.code});
    if (planning.status !== 'completed') throw new Error('Hostlier planning requires recovery');
    if (!planning.plan) throw new Error('Hostlier returned no reviewed plan');
    return planning.plan;
  }

  async function plan(project, action, signal) {
    for (let attempt = 0; attempt < 3; attempt++) {
      let body;
      try { body = { spec: lowerStoredReferences(structuredClone(typeof project === 'function' ? await project() : project), action !== 'deploy') }; }
      catch (error) { error.beforeApply = true; throw error; }
      if (action === 'scale') body.action = 'scale';
      let review;
      try { review = await reviewedPlan(body, signal); }
      catch (error) { if (['INVALID_CONFIG', 'IMAGE_RESOLUTION', 'REGISTRY_CREDENTIALS'].includes(error.code)) error.beforeApply = true; throw error; }
      if (typeof review.id !== 'string' || typeof review.fingerprint !== 'string') throw new Error('Hostlier returned an incomplete deployment plan');
      let receipt;
      try { receipt = await approved('POST', `/v1/plans/${encodeURIComponent(review.id)}/apply`, { approved: true, fingerprint: review.fingerprint, requestId: requestId() }, signal); }
      catch (error) { if (error.code === 'STALE_PLAN' && attempt < 2) continue; throw error; }
      await waitOperation(receipt.operationId, signal);
      return { stdout: JSON.stringify(receipt), stderr: '' };
    }
    throw new Error('Hostlier plan stayed stale');
  }

  return async (args, { signal, input } = {}) => {
    const [command, target] = args;
    let result;
    if (command === 'validate') {
      const path = args[args.indexOf('--file') + 1];
      if (!path) throw new Error('Hostlier project file required');
      const source = await readFile(path, 'utf8');
      const document = YAML.parseDocument(source, { uniqueKeys: true, strict: true });
      const project = document.toJS({ maxAliasCount: 0 });
      if (document.errors.length || document.warnings.length || !(project?.source?.image || project?.image)) throw new Error('Invalid Hostlier cloud project');
      result = {};
    } else if (command === 'deploy' || command === 'update') {
      const path = args[args.indexOf('--file') + 1];
      const project = YAML.parse(await readFile(path, 'utf8'));
      return plan(project, command, signal);
    } else if (command === 'scale') {
      const cpu = args[args.indexOf('--cpu') + 1];
      const memory = args[args.indexOf('--memory') + 1];
      return plan(async () => {
        const current = await commandJSON('GET', `/v1/apps/${encodeURIComponent(target)}`, undefined, signal);
        const project = structuredClone(current.spec);
        project.resources = { ...(project.resources ?? {}), cpu, memory };
        delete project.sleepAfter;
        delete project.autoscaling;
        return project;
      }, 'scale', signal);
    } else if (command === 'status') {
      const path = target === '--json' ? '/v1/status' : `/v1/apps/${encodeURIComponent(target)}`;
      result = await commandJSON('GET', path, undefined, signal);
    } else if (command === 'operation') {
      result = await commandJSON('GET', `/v1/operations/${encodeURIComponent(target)}`, undefined, signal);
    } else if (command === 'delete') {
      if (!args.includes('--purge')) throw new Error('Cloud SDK only supports permanent deletion');
      const receipt = await approved('POST', `/v1/apps/${encodeURIComponent(target)}/action`, { action: 'delete', purge: true }, signal);
      await waitOperation(receipt.operationId, signal);
      result = receipt;
    } else if (command === 'secret' && target === 'set') {
      if (input === undefined) throw new Error('Secret value must be private input');
      const name = args[2];
      const value = Buffer.from(input).toString('utf8');
      if (!value || value.includes('\0')) throw new Error('Invalid Hostlier secret input');
      const owner = args.includes('--app') ? args[args.indexOf('--app') + 1] : undefined;
      const body = { value, requestId: requestId(), ...(owner ? { owner } : {}) };
      result = await approved('PUT', `/v1/secrets/${encodeURIComponent(name)}`, body, signal);
    } else if (command === 'secret' && target === 'delete') {
      result = await approved('DELETE', `/v1/secrets/${encodeURIComponent(args[2])}`, {}, signal);
    } else {
      throw new Error(`Unsupported Hostlier cloud SDK operation: ${basename(command ?? '')}`);
    }
    return { stdout: JSON.stringify(result), stderr: '' };
  };
}
