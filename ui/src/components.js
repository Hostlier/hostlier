// Shared DOM components. User and controller values are always assigned as text.
export const text = (node, value) => { const next=String(value??'—');if(node.textContent!==next)node.textContent=next; };
export const bytes = value => value == null ? '—' : value >= 1073741824 ? `${(value / 1073741824).toFixed(2)} GiB` : `${(value / 1048576).toFixed(0)} MiB`;

const paths = {
  grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  database: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v14c0 4 16 4 16 0V5M4 12c0 4 16 4 16 0"/>',
  server: '<rect x="3" y="3" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/><path d="M7 6.5h.01M7 17.5h.01M11 6.5h6M11 17.5h6"/>',
  history: '<path d="M3 11a9 9 0 1 1 2.5 7M3 4v7h7M12 7v5l3 2"/>',
  shield: '<path d="m12 3 8 3v6c0 4-4 7-8 9-4-2-8-5-8-9V6l8-3Z"/><path d="m8 12 3 3 5-6"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="m10 3-1 3-3 1-3-1-1 4 3 2v2l-2 2 2 3 3-1 2 1 1 3 4-1 1-3 2-1 3 1 1-4-3-2v-2l2-2-2-3-3 1-2-1-1-3-3 1Z"/>',
  box: '<path d="m12 3 9 5v8l-9 5-9-5V8l9-5Z"/><path d="m3 8 9 5 9-5M12 13v8M7.5 5.5l9 5"/>',
  activity: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
  cpu: '<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 1v5m6-5v5M9 18v5m6-5v5M1 9h5m-5 6h5m12-6h5m-5 6h5"/><rect x="10" y="10" width="4" height="4"/>',
  memory: '<rect x="3" y="6" width="18" height="12" rx="2"/><path d="M7 10v4m5-4v4m5-4v4M7 3v3m5-3v3m5-3v3M7 18v3m5-3v3m5-3v3"/>',
  arrow: '<path d="M5 12h14m-5-5 5 5-5 5"/>',
  play: '<path d="m8 5 11 7-11 7V5Z"/>',
  stop: '<rect x="6" y="6" width="12" height="12" rx="1"/>',
  restart: '<path d="M20 10a8 8 0 1 0-2.5 8M20 4v6h-6"/>',
};

function element(tag, className, value) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (value !== undefined) text(node, value);
  return node;
}

export function Icon(name) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '1.5');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('aria-hidden', 'true');
  // Only fixed, authored icon paths enter markup; external values never do.
  svg.innerHTML = paths[name] || paths.box;
  return svg;
}

export function Button({ label, icon, onClick, variant = 'default' }) {
  const button = element('button', variant === 'primary' ? 'button button-primary' : 'button');
  button.type = 'button';
  if (icon) button.append(Icon(icon));
  button.append(element('span', '', label));
  if (onClick) button.addEventListener('click', onClick);
  return button;
}

export function StatusBadge() {
  const node = element('span', 'badge');
  const dot = element('span', 'badge-dot');
  dot.setAttribute('aria-hidden', 'true');
  const label = element('span');
  node.append(dot, label);
  return {
    node,
    update(value, tone = 'default') {
      text(label, value);
      node.dataset.tone = tone;
      text(dot, tone === 'attention' ? '!' : '');
    },
  };
}

export function MetricCard({ label, caption, icon }) {
  const node = element('article', 'metric');
  const heading = element('div', 'metric-label');
  heading.append(element('span', '', label), Icon(icon));
  const value = element('strong', 'metric-value', '—');
  node.append(heading, value, element('span', 'metric-caption', caption));
  return { node, update: next => text(value, next) };
}

export function EmptyState(title, description, icon = 'box') {
  const node = element('div', 'empty-state');
  node.append(Icon(icon), element('strong', '', title), element('p', '', description));
  return node;
}

function applicationRow(onAction) {
  const node = element('tr');
  const name = element('a', 'app-name text-link');
  name.dataset.route='';
  const address = element('small', 'app-url');
  const identity = element('div');
  identity.append(name, address);
  const symbol = element('span', 'application-icon');
  symbol.append(Icon('box'));
  const application = element('div', 'application-cell');
  application.append(symbol, identity);
  const nameCell = element('td');
  nameCell.append(application);
  const status = StatusBadge();
  const note = element('small', 'observation-note');
  const statusCell = element('td');
  statusCell.append(status.node, note);
  const replicas = element('td', 'replicas');
  const cpu = element('strong', 'resource-value');
  const memory = element('small', 'resource-caption');
  const resources = element('td');
  resources.append(cpu, memory);
  const actionCell = element('td');
  const actions = element('div', 'actions');
  const inspect=element('a','button','View deployment →');inspect.dataset.route='';actions.append(inspect);
  actionCell.append(actions);
  node.append(nameCell, statusCell, replicas, resources, actionCell);
  return {
    node,
    update(app, { live, busy, observed }) {
      inspect.href='/deployments/'+encodeURIComponent(app.name);
      text(name, app.name);
      name.href='/deployments/'+encodeURIComponent(app.name);
      text(address, app.url || 'Private workload');
      address.title = app.url || 'Private workload';
      const label = !observed ? 'Unobserved' : app.oomKilled ? 'Out of memory' : app.running ? 'Running' : 'Stopped';
      status.update(label, app.oomKilled ? 'attention' : observed && app.running ? 'active' : 'default');
      text(note, app.reason || (app.restartCount ? `${app.restartCount} restarts` : `Desired: ${app.status}`));
      text(replicas, `${app.readyReplicas ?? '—'} / ${app.desiredReplicas}`);
      text(cpu, app.usage?.cpuCores != null ? `${app.usage.cpuCores.toFixed(2)} cores` : '— cores');
      text(memory, app.usage?.memoryBytes != null ? bytes(app.usage.memoryBytes) : app.usage?.reason || 'Metrics unavailable');

    },
  };
}

// Reconcile by stable identity so periodic captures preserve keyboard focus and existing controls.
function keyedList(container, create) {
  const items = new Map();
  return (values, key, update) => {
    const wanted = new Set(values.map(key));
    for (const [id, item] of items) if (!wanted.has(id)) { item.node.remove(); items.delete(id); }
    let previous = null;
    for (const value of values) {
      const id = key(value);
      let item = items.get(id);
      if (!item) { item = create(); items.set(id, item); }
      update(item, value);
      const next = previous ? previous.nextElementSibling : container.firstElementChild;
      if (item.node !== next) container.insertBefore(item.node, next);
      previous = item.node;
    }
  };
}

export function DeploymentTable(container, onAction) {
  const update = keyedList(container, () => applicationRow(onAction));
  return (apps, state) => update(apps, app => app.name, (row, app) => row.update(app, { ...state, observed: state.observed(app) }));
}

function operationRow() {
  const node = element('a', 'operation');
  node.dataset.route='';
  const symbol = element('span', 'operation-icon');
  symbol.append(Icon('arrow'));
  const title = element('span', 'operation-title');
  const kind = element('span', 'operation-kind');
  const id = element('small', 'operation-id');
  const description = element('div');
  const line = element('div');
  line.append(title, kind);
  description.append(line, id);
  const detail = element('div', 'operation-detail');
  detail.append(symbol, description);
  const status = StatusBadge();
  node.append(detail, status.node);
  return {
    node,
    update(op) {
      node.href='/operations/'+encodeURIComponent(op.id);
      text(title, op.name);
      text(kind, op.kind);
      text(id, op.id);
      status.update(op.status, ['succeeded', 'running', 'completed'].includes(op.status) ? 'active' : ['failed', 'interrupted'].includes(op.status) ? 'attention' : 'default');
    },
  };
}

export function OperationList(container) {
  const list = element('div');
  const empty = EmptyState('No operations yet', 'Deployment activity will appear here.', 'activity');
  container.replaceChildren(list, empty);
  const update = keyedList(list, operationRow);
  return operations => { update(operations, op => op.id, (row, op) => row.update(op)); empty.hidden = operations.length > 0; };
}
