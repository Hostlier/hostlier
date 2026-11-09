// All dynamic content is text; authority stays in server-retained ceremonies.
const el = (id) => document.getElementById(id);
const query = new URLSearchParams(location.search);
el('command').textContent = `hostlier connect ${location.origin}`;
const decode = (value) => Uint8Array.from(atob(value.replace(/-/g, '+').replace(/_/g, '/')), c => c.charCodeAt(0));
const encode = (value) => btoa(String.fromCharCode(...new Uint8Array(value))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

// Propagate only safe server errors and never place returned markup into the DOM.
async function api(path, body, ceremony) {
  const response = await fetch(path, {method: body === undefined ? 'GET' : 'POST', credentials: 'omit', headers: {'Content-Type':'application/json', ...(ceremony ? {'X-Hostlier-Ceremony': ceremony} : {})}, ...(body === undefined ? {} : {body: JSON.stringify(body)})});
  const result = await response.json();
  if (!response.ok) throw new Error(result.error?.message || 'The controller rejected this request.');
  return result;
}

// Convert WebAuthn JSON options without changing challenge, RP, or verification requirements.
function options(value) {
  const p = value.publicKey;
  p.challenge = decode(p.challenge);
  if (p.user) p.user.id = decode(p.user.id);
  for (const key of ['allowCredentials','excludeCredentials']) for (const item of p[key] || []) item.id = decode(item.id);
  return {publicKey:p};
}

// Serialize the browser proof; private authenticator keys never leave the authenticator.
function proof(credential) {
  const response = {};
  for (const key of ['clientDataJSON','attestationObject','authenticatorData','signature','userHandle']) {
    if (credential.response[key]) response[key] = encode(credential.response[key]);
  }
  if (credential.response.getTransports) response.transports = credential.response.getTransports();
  return {id:credential.id, rawId:encode(credential.rawId), type:credential.type, response, clientExtensionResults:credential.getClientExtensionResults(), authenticatorAttachment:credential.authenticatorAttachment};
}

// Each explicit click starts a fresh ceremony tied to the reviewed server-side intent.
async function ceremony(kind, body, registration = false) {
  const start = await api(`/v1/access/passkey/${kind}/begin`, body);
  const credential = await navigator.credentials[registration ? 'create' : 'get'](options(start.options));
  if (!credential) throw new Error('Passkey prompt was cancelled.');
  return api(`/v1/access/passkey/${kind}/finish`, proof(credential), start.ceremony);
}

let busy = false;
let complete = false;
let expired = false;
let expiryTimer;
el('controller').textContent = location.host;

function status(message, tone = 'info') {
  el('status').hidden = false;
  el('status').textContent = message;
  el('status').dataset.tone = tone;
}
function progress(step) {
  for (const id of ['review', 'verify', 'done']) {
    el(`step-${id}`).removeAttribute('aria-current');
    if (id === step) el(`step-${id}`).setAttribute('aria-current', 'step');
  }
}
function heading(title, intro) {
  el('title').textContent = title;
  el('intro').textContent = intro;
  document.title = `Hostlier · ${title.replace(/\.$/, '')}`;
}
function lockControls() {
  for (const field of document.querySelectorAll('#scope input, #actions button')) field.disabled = busy || complete || expired;
  el('actions').setAttribute('aria-busy', String(busy));
}
function finish(title, message, state = 'Approved') {
  complete = true;
  clearInterval(expiryTimer);
  progress('done');
  el('decision-title').textContent = title;
  el('decision-description').textContent = 'This request no longer needs your approval.';
  el('request-state').textContent = state;
  el('request-state').dataset.state = 'success';
  el('actions').replaceChildren();
  el('expiry-row').hidden = true;
  status(message, 'success');
  lockControls();
  el('status').focus();
}
function watchExpiry(value) {
  if (!Number.isFinite(value)) return;
  el('expiry-row').hidden = false;
  const tick = () => {
    const seconds = Math.max(0, Math.ceil((value - Date.now()) / 1000));
    el('expiry').textContent = seconds ? `In ${Math.floor(seconds / 60)}m ${seconds % 60}s` : 'Expired';
    if (!seconds && !complete) {
      expired = true;
      clearInterval(expiryTimer);
      el('request-state').textContent = 'Expired';
      status('This request has expired. Start a new request from your CLI or dashboard.', 'error');
      lockControls();
    }
  };
  expiryTimer = setInterval(tick, 1000);
  tick();
}

// All actions and scope fields lock together, keeping the visible selection identical
// to the scope captured before the passkey prompt. The server remains authoritative.
function button(label, run, secondary = false) {
  const b = document.createElement('button');
  b.textContent = label; b.type = 'button';
  if (secondary) b.className = 'secondary';
  b.addEventListener('click', async () => {
    if (busy || complete || expired) return;
    busy = true; lockControls();
    try {
      await run();
    } catch (error) {
      status(error.name === 'NotAllowedError' ? 'Passkey verification was cancelled or timed out. Try again when you’re ready.' : error.message, 'error');
      progress('review');
    } finally {
      busy = false; lockControls();
    }
  });
  el('actions').append(b);
  lockControls();
}
async function verify(kind, body, registration = false) {
  progress('verify');
  status('Waiting for your passkey. Follow your browser’s verification prompt.');
  return ceremony(kind, body, registration);
}
function restricted() { return el('mode-restricted').checked; }
function updateScopeReview() {
  const limited = restricted();
  el('scope-fields').hidden = !limited;
  for (const id of ['apps', 'minutes', 'uses']) el(id).required = limited;
  el('permission').textContent = limited ? 'Restricted deployments' : 'All access';
  el('scope-notice').dataset.restricted = String(limited);
  el('scope-label').textContent = limited ? 'Bounded deployment access' : 'Full controller access';
  el('scope-summary').textContent = limited
    ? 'Only these apps and secrets, within the limits you choose.'
    : 'Full read and write access, with no expiry. Includes controller settings and secrets.';
}
el('scope').addEventListener('input', updateScopeReview);
el('scope').addEventListener('change', updateScopeReview);
el('scope').addEventListener('submit', event => event.preventDefault());

function scope() {
  if (!restricted()) return {};
  // Validate explicitly because inputs are locked while the action runs.
  const names = id => el(id).value.split(',').map(v => v.trim()).filter(Boolean);
  const apps = names('apps'), secrets = names('secrets'), createSecrets = names('create-secrets');
  const minutes = Number(el('minutes').value), uses = Number(el('uses').value);
  if (!apps.length || apps.length > 20 || !Number.isInteger(minutes) || minutes < 1 || minutes > 480 || !Number.isInteger(uses) || uses < 1 || uses > 100) {
    throw new Error('Enter 1–20 application names, a lifetime of 1–480 minutes, and a limit of 1–100 deployments.');
  }
  if (secrets.length > 20 || createSecrets.some(name => !secrets.includes(name))) throw new Error('Allow at most 20 secrets, and include each secret to create in Allowed secret names.');
  return {apps, secrets, createSecrets, durationSeconds: minutes * 60, maxUses: uses};
}

function fact(root, label, value, mono = false) {
  if (value === undefined || value === null || value === '') return;
  const row = document.createElement('div'), dt = document.createElement('dt'), dd = document.createElement('dd');
  dt.textContent = label; dd.textContent = String(value);
  if (mono) dd.className = 'mono';
  row.append(dt, dd); root.append(row);
}
const humanLabel = key => key.replace(/([a-z])([A-Z])/g, '$1 $2').replace(/^./, c => c.toUpperCase());
// Retain every server-reviewed field, including nested deployment plans and warnings.
// Text-only rendering prevents request names or provider data from becoming markup.
function reviewFields(root, value, depth = 0) {
  if (value === null || typeof value !== 'object') {
    const p = document.createElement('p'); p.className = 'review-value'; p.textContent = String(value); root.append(p); return;
  }
  if (depth > 4) {
    const pre = document.createElement('pre'); pre.textContent = JSON.stringify(value, null, 2); root.append(pre); return;
  }
  const facts = document.createElement('dl'); facts.className = 'facts'; root.append(facts);
  for (const [key, entry] of Object.entries(value)) {
    if (entry !== null && typeof entry === 'object') {
      const group = document.createElement('section'); group.className = 'review-group';
      const title = document.createElement('h3'); title.textContent = Array.isArray(value) ? `Item ${Number(key) + 1}` : humanLabel(key);
      group.append(title); root.append(group);
      if (!Object.keys(entry).length) { const p = document.createElement('p'); p.className = 'helper'; p.textContent = 'None'; group.append(p); }
      else reviewFields(group, entry, depth + 1);
    } else fact(facts, humanLabel(key), entry === null ? 'None' : entry === '' ? '(empty)' : entry);
  }
}
function showReview(review) {
  el('command-panel').hidden = true;
  el('details').hidden = false;
  el('review').textContent = JSON.stringify(review, null, 2);
  fact(el('request-facts'), 'Request ID', review.id, true);
}
function passkeySupported() {
  if (!window.PublicKeyCredential || !navigator.credentials) throw new Error('Passkeys are unavailable in this browser. Open this controller’s HTTPS link in a browser that supports passkeys.');
}

async function start() {
  if (location.pathname === '/security') {
    const bootstrap = new URLSearchParams(location.hash.slice(1));
    const token = bootstrap.get('enroll'), request = bootstrap.get('request');
    history.replaceState(null, '', '/security');
    heading('Set up your passkey', 'Use your device or a security key to approve access.');
    el('command-title').textContent = 'Set up owner access';
    el('command-hint').textContent = 'Enrollment begins with trusted server administration.';
    el('command').textContent = 'Choose your device’s passkey manager or a physical security key when prompted.';
    el('permission').textContent = 'Owner approval';
    el('decision-title').textContent = 'Create your passkey.';
    if (!token) throw new Error('Open the one-time enrollment URL generated by your server administrator.');
    passkeySupported();
    button('Create passkey', async () => {
      await verify('register', {token}, true);
      if (request) { location.replace('/connect?request=' + encodeURIComponent(request)); return; }
      finish('You’re ready.', 'Passkey enrolled. Start a connection from your CLI or dashboard to review device access.', 'Enrolled');
    });
  } else if (location.pathname === '/connect' && query.get('request')) {
    heading('Connect this device', 'Choose its access, then confirm with your passkey.');
    el('command-panel').hidden = true;
    status('Loading the connection request…');
    const id = query.get('request');
    const review = await api(`/v1/access/pair/review/${encodeURIComponent(id)}`);
    el('status').hidden = true;
    showReview(review);
    el('device-name').textContent = review.name;
    fact(el('request-facts'), 'Device name', review.name);
    fact(el('request-facts'), 'Fingerprint', review.deviceFingerprint, true);
    el('details-hint').textContent = 'Compare this fingerprint with the device that started the request.';
    if (review.approved) { finish('Already approved.', 'Return to the CLI or dashboard that requested this connection.'); return; }
    el('scope').hidden = false; updateScopeReview();
    el('decision-title').textContent = 'Ready to connect?';
    watchExpiry(review.expiresAt);
    passkeySupported();
    button('Approve connection', async () => {
      const result = await verify('pair', {id, scope: scope()});
      el('fallback').hidden = !result.code; el('code').textContent = result.code || '';
      finish('Connection approved.', 'Return to your CLI or dashboard. Your device will finish connecting automatically.');
    });
  } else if (location.pathname === '/approve') {
    heading('Approve this operation', 'Your approval applies only to the changes below.');
    el('command-panel').hidden = true;
    if (!query.get('request')) throw new Error('This link is missing its request ID. Open the approval link from your CLI or dashboard.');
    status('Loading the operation request…');
    const review = await api(`/v1/human/review/${encodeURIComponent(query.get('request'))}`);
    el('status').hidden = true;
    showReview(review);
    el('details-title').textContent = 'Changes to approve';
    el('details-hint').textContent = 'These details come from the controller’s saved request.';
    fact(el('request-facts'), 'Fingerprint', review.fingerprint, true);
    el('operation-review').hidden = false;
    reviewFields(el('operation-review'), review.review);
    el('permission').textContent = 'This request only';
    el('decision-title').textContent = 'Approve these changes?';
    if (review.status !== 'pending') { finish('Request already reviewed.', `Current status: ${review.status}. Return to the requesting CLI or dashboard for progress.`, review.status); return; }
    watchExpiry(review.expiresAt);
    passkeySupported();
    for (const decision of ['approve', 'reject']) button(decision === 'approve' ? 'Approve operation' : 'Reject request', async () => {
      await verify('decision', {id: review.id, fingerprint: review.fingerprint, decision});
      finish(decision === 'approve' ? 'Operation approved.' : 'Request rejected.', decision === 'approve' ? 'Return to your CLI or dashboard to follow execution. Approval does not mean the operation has completed.' : 'This request was rejected. Return to your CLI or dashboard.', decision === 'approve' ? 'Approved' : 'Rejected');
    }, decision === 'reject');
  }
}
start().catch(error => {
  el('actions').replaceChildren();
  el('decision-title').textContent = 'Unable to continue.';
  el('decision-description').textContent = 'Review the message below before trying again.';
  status(error.message, 'error');
});
