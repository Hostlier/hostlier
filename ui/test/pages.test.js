import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {parseHTML} from 'linkedom';
import {Pages,MetricSeries} from '../src/views.js';

const html=readFileSync(new URL('../src/index.html',import.meta.url),'utf8');
const deployment={name:'faq-demo',status:'running',revision:'rev-27',runtimeId:'container-27',spec:{name:'faq-demo',image:'example/faq:v27',port:8080,sidecars:{worker:{image:'example/worker:v1'}}},resourceRefs:{cache:'cache'},revisions:[{id:'rev-26'}]};
const resource={name:'cache',type:'redis',status:'running',dependents:['faq-demo'],spec:{cpu:'0.5',memory:'128Mi',storage:'1Gi'}};
const recoveryJob={id:'job-1',kind:'backup',source:'cache',status:'interrupted',phase:'restore-test',createdAt:1793296800000};
const operation={id:'op-1',name:'faq-demo',status:'failed',kind:'deploy',error:{code:'RUNTIME_FAILED',message:'Readiness probe timed out'}};
const snapshot={hostname:'vm.test',runtime:'docker',generation:4,observedAt:new Date().toISOString(),applications:[{name:'faq-demo',available:true,running:true,desiredReplicas:1,readyReplicas:1,observedAt:new Date().toISOString()}],telemetry:{collectors:1,accepted:54,rejected:2,fresh:true,lastReceivedAt:new Date().toISOString()}};
const flush=async()=>{await new Promise(setImmediate);await new Promise(setImmediate);};
function environment(path='/overview',custom) {
  const {document,window}=parseHTML(html);globalThis.document=document;globalThis.window=window;
  globalThis.location=new URL(path,'http://127.0.0.1:4177');const handlers={};const intervals=[];
  globalThis.history={pushState:(_,__,value)=>{globalThis.location=new URL(value,location);},replaceState:(_,__,value)=>{globalThis.location=new URL(value,location);}};
  globalThis.addEventListener=(type,fn)=>{handlers[type]=fn;};globalThis.setInterval=fn=>{intervals.push(fn);return intervals.length;};window.scrollTo=()=>{};
  Object.defineProperty(window.HTMLAnchorElement.prototype,'pathname',{configurable:true,get(){return new URL(this.href,location).pathname;}});
  const calls=[];const api=async path=>{
    calls.push(path);if(custom){const value=custom(path);if(value!==undefined)return value;}
    if(path==='/api/resources')return {resources:[resource]};
    if(path==='/api/resources/cache')return resource;
    if(path==='/api/resources/cache/logs')return {text:'Ready to accept connections'};
    if(path==='/api/hosts')return {hosts:{}};
    if(path==='/api/recovery/jobs')return {jobs:{'job-1':recoveryJob}};
    if(path==='/api/recovery/job-1')return recoveryJob;
    if(path==='/api/recovery/policies')return {policies:{},points:{}};
    if(path==='/api/recovery/backups')return {backups:{}};
    if(path==='/api/recovery/destinations')return {destinations:[]};
    if(path==='/api/access/clients')return {clients:[{id:'client-1',name:'Laptop',publicKey:'public-key',revoked:false}]};
    if(path==='/api/access/authorizations')return {authorizations:{}};
    if(path==='/api/access/approval')return {enrolled:true,humanApprovalRequired:true,method:'openssh-fido'};
    if(path==='/api/settings/secrets')return {secrets:[{name:'DB_PASSWORD',revision:'rev-1'}]};
    if(path==='/api/settings/domains')return {domains:['apps.example.com']};
    if(path==='/api/settings/doctor')return {docker:true,caddy:true};
    if(path==='/api/deployments/faq-demo/workflows')return [{step:'provision-cache',status:'ready'}];
    if(path==='/api/tunnels')return [];
    if(path==='/api/tunnels/faq-demo')return {name:'faq-demo',bridgeId:'laptop',status:'connected',url:'https://faq-demo.tunnels.example.com',connections:4,expiresAt:1794000000000};
    if(path==='/api/deployments')return {applications:[deployment]};
    if(path.startsWith('/api/activity?'))return {entries:[{sequence:1,timestamp:new Date().toISOString(),level:'info',message:'Operation failed',operationId:'op-1',application:'faq-demo'}],retentionDays:30};
    if(path==='/api/operations')return {operations:[operation]};
    if(path==='/api/deployments/faq-demo')return deployment;
    if(path.startsWith('/api/deployments/faq-demo/logs'))return {text:'<script>doNotExecute()</script> Application started'};
    if(path==='/api/operations/op-1')return operation;
    if(path==='/api/operations/op-1/events')return {entries:[{timestamp:'now',level:'error',message:'Readiness probe timed out'}]};
    if(path.startsWith('/api/observability?'))return new URL(path,location).searchParams.get('signal')==='logs'?{records:[{id:2,identity:{application:'faq-demo'},observedAt:new Date().toISOString(),body:'request completed'}],nextBefore:2}:{records:[]};
    throw Error('Unexpected request '+path);
  };
  const pages=new Pages(document.getElementById('page-content'),{api,propose:()=>{}});pages.update(snapshot,{live:true,busy:false});
  const click=node=>{assert.ok(node,'click target must exist');const event=new window.Event('click',{bubbles:true,cancelable:true});event.button=0;node.dispatchEvent(event);};
  return {document,pages,calls,click,handlers,intervals};
}

test('controller is an app launcher and app navigation owns separate monitoring views',async()=>{
 const {document,pages,calls,click,handlers}=environment();await flush();
 assert.equal(document.title,'Hostlier · Apps & tunnels');assert.deepEqual(calls,['/api/deployments','/api/tunnels']);
 assert.deepEqual([...document.querySelectorAll('.nav-title')].map(node=>node.textContent),['Apps & tunnels','Logs','Activity','Settings']);
 assert.equal(document.querySelectorAll('.application-card').length,1);assert.equal(document.querySelector('.metrics-grid'),null);
 click(document.querySelector('.application-card'));await flush();
 assert.equal(location.pathname,'/deployments/faq-demo');assert.match(document.getElementById('page-content').textContent,/example\/faq:v27/);
 assert.equal(document.querySelector('.log-tail'),null);assert.equal(document.querySelector('.cli-command'),null);
 assert.deepEqual([...document.querySelectorAll('.nav-title')].map(node=>node.textContent),['Overview','Metrics','Logs','Activity','Resources','Settings']);
 click(document.querySelector('.nav-link[data-key="logs"]'));await flush();
 pages.runtimePanel.open=true;await pages.refreshRuntime();assert.match(document.querySelector('.log-tail').textContent,/<script>doNotExecute/);assert.equal(document.querySelectorAll('#page-content script').length,0);
 click(document.querySelector('.nav-link[data-key="activity"]'));await flush();click(document.querySelector('.history-entry [href^="/operations/"]'));await flush();
 assert.equal(location.pathname,'/operations/op-1');assert.equal(new URLSearchParams(location.search).get('app'),'faq-demo');assert.match(document.getElementById('page-content').textContent,/Failure details/);
 assert.equal(document.querySelector('.nav-link[aria-current="page"]').dataset.key,'activity');
 location=new URL('/deployments/faq-demo',location);handlers.popstate();await flush();assert.ok(document.querySelector('.detail-actions'));
 click(document.querySelector('.back-to-controller'));await flush();assert.equal(document.querySelectorAll('.nav-link').length,4);
});

test('route changes discard late detail responses from the previous page',async()=>{
  let resolve;
  const delayed=new Promise(done=>{resolve=done;});
  const {document,pages}=environment('/deployments/faq-demo',path=>path==='/api/deployments/faq-demo'?delayed:undefined);
  history.pushState(null,'','/operations/op-1');pages.navigate();await flush();
  resolve(deployment);await flush();assert.equal(location.pathname,'/operations/op-1');assert.match(document.getElementById('page-content').textContent,/Operation ID/);assert.equal(document.querySelector('.detail-actions'),null);
});

test('observability filters, pause/resume and older logs retain correct query scope',async()=>{
  const {document,pages,calls,click,intervals}=environment('/observability?app=faq-demo');await flush();
  assert.ok(calls.some(path=>path.includes('app=faq-demo')&&path.includes('signal=metrics')));assert.ok(calls.some(path=>path.includes('signal=logs')));
  assert.match(document.getElementById('page-content').textContent,/request completed/);assert.match(document.getElementById('page-content').textContent,/No matching metric samples/);
  document.querySelector('input[name="component"]').value='worker';await pages.refresh();assert.equal(calls.at(-1).includes('component=worker'),false);
  document.querySelector('form.observation-filters').dispatchEvent(new window.Event('submit',{bubbles:true,cancelable:true}));await flush();assert.ok(calls.at(-1).includes('component=worker'));
  const older=[...document.querySelectorAll('button')].find(node=>node.textContent==='Older logs');click(older);await flush();assert.equal(pages.paused,true);assert.ok(calls.at(-1).includes('before=2'));
  const before=calls.length;intervals[0]();await flush();assert.equal(calls.length,before);
  click(pages.pause);await flush();assert.equal(pages.paused,false);assert.equal(calls.at(-1).includes('before='),false);
  click(pages.pause);history.pushState(null,'','/operations');pages.navigate();await flush();assert.equal(pages.paused,false);
});

test('a selected-controller reset cannot render an earlier in-flight detail',async()=>{
  let resolve;const delayed=new Promise(done=>{resolve=done;});let old=true;
  const {document,pages}=environment('/deployments/faq-demo',path=>path==='/api/deployments/faq-demo'&&old?delayed:undefined);
  old=false;pages.reset();await flush();resolve({...deployment,spec:{image:'OLD-CONTROLLER'}});await flush();assert.doesNotMatch(document.getElementById('page-content').textContent,/OLD-CONTROLLER/);
});

test('metric series keep different instances and attributes separate and handle sparse samples',()=>{
  environment();const base={name:'cpu.usage',unit:'s',kind:'sum',temporality:2,observedAt:'2026-10-29T12:00:00Z',identity:{application:'faq-demo',instance:'one'},attributes:{cpu:'0'},value:4};
  const node=MetricSeries([base,{...base,value:5,observedAt:'2026-10-29T12:00:05Z'},{...base,attributes:{cpu:'1'}},{...base,identity:{application:'faq-demo',instance:'two'}}]);
  assert.equal(node.querySelectorAll('.series-card').length,3);assert.equal(node.querySelectorAll('svg').length,1);assert.match(node.textContent,/cumulative/);assert.match(node.textContent,/Waiting for more samples/);
});

test('changing a log component discards a late response from the previously selected component',async()=>{
  let resolve;const delayed=new Promise(done=>{resolve=done;});
  const {document,pages}=environment('/deployments/faq-demo?view=logs',path=>path==='/api/deployments/faq-demo/logs'?delayed:path.includes('/logs?component=worker')?{text:'WORKER LOGS'}:undefined);
  await flush();pages.runtimePanel.open=true;pages.refreshRuntime();await flush();pages.component.children[0].selected=false;pages.component.children[1].selected=true;
  pages.component.dispatchEvent(new window.Event('change'));await flush();
  resolve({text:'OLD MAIN LOGS'});await flush();
  assert.match(document.querySelector('.log-tail').textContent,/WORKER LOGS/);assert.doesNotMatch(document.querySelector('.log-tail').textContent,/OLD MAIN/);
});

test('memory remains visible during CPU warmup and measured idle CPU displays zero',async()=>{
  const {document,pages}=environment('/deployments/faq-demo');await flush();
  const usage={available:false,memoryBytes:2097152,reason:'CPU sampling is warming up; memory is available'};
  const measured={...snapshot,applications:[{...snapshot.applications[0],usage}]};pages.update(measured,{live:true,busy:false});
  const facts=()=>Object.fromEntries([...document.querySelectorAll('.facts > div')].map(row=>[row.querySelector('dt').textContent,row.querySelector('dd').textContent]));
  assert.equal(facts().Memory,'2 MiB');assert.equal(facts().CPU,'Unavailable');assert.match(facts().Measurements,/warming up/);
  usage.available=true;usage.cpuCores=0;usage.reason='';pages.update(measured,{live:true,busy:false});assert.equal(facts().CPU,'0.000 cores');assert.equal(facts().Memory,'2 MiB');
});

test('resources connect deployments to live inspection, recovery and controller-scoped CLI commands',async()=>{
 const {document,pages,click}=environment('/resources');await flush();pages.setConnection({name:'linux-vm'});
 assert.match(document.getElementById('page-content').textContent,/Last recorded state/);assert.ok(document.querySelector('[href="/deployments/faq-demo"]'));
 click(document.querySelector('td [href="/resources/cache"]'));await flush();
 assert.equal(location.pathname,'/resources/cache');assert.match(document.getElementById('page-content').textContent,/Ready to accept connections/);
 assert.ok(document.querySelector('[href="/recovery?resource=cache"]'));
 assert.match(document.querySelector('.cli-command').textContent,/hostlier resource status cache --server linux-vm/);
 assert.equal(document.querySelector('#page-content button[type="submit"]'),null);
});

test('recovery failures stay visible beside working metadata and jobs have meaningful details',async()=>{
 const {document,click}=environment('/recovery',path=>path==='/api/recovery/destinations'?Promise.reject(new Error('Storage unavailable')):undefined);await flush();
 assert.match(document.getElementById('page-content').textContent,/Storage unavailable/);assert.match(document.getElementById('page-content').textContent,/interrupted/);
 click(document.querySelector('[href="/recovery/job-1"]'));await flush();assert.match(document.getElementById('page-content').textContent,/restore-test/);assert.ok(document.querySelector('[href="/resources/cache"]'));assert.match(document.querySelector('.cli-command').textContent,/backup job job-1 --watch/);
});

test('access and settings expose identities and secret metadata with terminal handoffs',async()=>{
 const {document,pages}=environment('/access');await flush();assert.match(document.getElementById('page-content').textContent,/Device signature/);assert.match(document.getElementById('page-content').textContent,/Laptop/);assert.match(document.getElementById('page-content').textContent,/clients revoke client-1/);
 history.pushState(null,'','/settings');pages.navigate();await flush();assert.match(document.getElementById('page-content').textContent,/DB_PASSWORD/);assert.match(document.getElementById('page-content').textContent,/apps.example.com/);assert.match(document.getElementById('page-content').textContent,/secret set SECRET_NAME --stdin/);
});

test('app resources, activity and settings keep their scope and CLI handoffs',async()=>{
 const {document,pages,calls,click}=environment('/deployments/faq-demo?view=resources');await flush();pages.setConnection({name:'production'});
 click(document.querySelector('[href="/resources/cache?app=faq-demo"]'));await flush();
 assert.equal(document.querySelector('.nav-link[aria-current="page"]').dataset.key,'resources');
 click(document.querySelector('.nav-link[data-key="activity"]'));await flush();assert.match(document.getElementById('page-content').textContent,/Operation failed/);
 click(document.querySelector('.nav-link[data-key="settings"]'));await flush();
 assert.match(document.getElementById('page-content').textContent,/scale faq-demo --cpu CPU_CORES --memory MEMORY_SIZE --server production/);
 assert.match(document.getElementById('page-content').textContent,/rev-26/);assert.match(document.getElementById('page-content').textContent,/example\/worker:v1/);
 assert.equal(calls.some(path=>path.includes('/api/actions')),false);assert.equal(document.querySelectorAll('details[open]').length,0);
});

test('resource operations link to resources and real controller diagnostic checks are visible',async()=>{
 const {document,pages}=environment('/operations/op-1',path=>path==='/api/operations/op-1'?{...operation,kind:'resource-backup',name:'cache'}:path==='/api/settings/doctor'?{runtime:'docker',checks:[{name:'docker',ok:true},{name:'caddy',ok:false}]}:undefined);await flush();
 assert.ok(document.querySelector('[href="/resources/cache"]'));assert.equal(document.querySelector('[href="/deployments/cache"]'),null);
 history.pushState(null,'','/settings');pages.navigate();await flush();
 const diagnostic=[...document.querySelectorAll('.detail-panel')].find(p=>p.textContent.includes('Controller diagnostics'));assert.match(diagnostic.textContent,/dockerAvailable/);assert.match(diagnostic.textContent,/caddyUnavailable/);
});

test('logs fail independently from activity and only the selected app section is fetched',async()=>{
 const {document,pages,calls,click}=environment('/deployments/faq-demo?view=logs',path=>path.includes('/logs')?Promise.reject(Error('Log source unavailable')):undefined);await flush();pages.runtimePanel.open=true;await pages.refreshRuntime();
 assert.match(document.getElementById('page-content').textContent,/Log source unavailable/);
 assert.equal(calls.some(path=>path.endsWith('/workflows')),false);
 click(document.querySelector('.nav-link[data-key="activity"]'));await flush();assert.match(document.getElementById('page-content').textContent,/Operation failed/);assert.doesNotMatch(document.getElementById('page-content').textContent,/Log source unavailable/);
});

test('local hosts remain inspectable without fleet enrollment and backup handoffs describe their actual reads',async()=>{
 const {document,pages}=environment('/hosts');await flush();assert.match(document.getElementById('page-content').textContent,/vm.test/);assert.match(document.getElementById('page-content').textContent,/Inspect local workloads/);
 history.pushState(null,'','/recovery');pages.navigate();await flush();assert.match(document.getElementById('page-content').textContent,/Inspect backup policies/);
 history.pushState(null,'','/recovery/job-1');pages.navigate();await flush();assert.doesNotMatch(document.getElementById('page-content').textContent,/Inspect upgrade state/);
});

test('real OTLP string log bodies render as readable application messages',async()=>{
 const {document}=environment('/observability',path=>path.includes('signal=logs')?{records:[{observedAt:new Date().toISOString(),identity:{application:'faq-demo'},body:{stringValue:'GET /vm-audit HTTP/1.1 200'}}]}:undefined);await flush();
 assert.equal(document.querySelector('.log-body').textContent,'GET /vm-audit HTTP/1.1 200');
});

test('controller settings retain secondary pages and mobile navigation closes on selection',async()=>{
 const {document,click}=environment('/resources/cache');await flush();
 assert.deepEqual([...document.querySelectorAll('.nav-group-label')].map(node=>node.textContent),['Controller']);
 assert.equal(document.querySelector('.nav-link[aria-current="page"]').getAttribute('href'),'/settings');
 assert.equal(document.querySelector('#breadcrumbs a').getAttribute('href'),'/resources');
 const toggle=document.getElementById('navigation-toggle');click(toggle);assert.equal(toggle.getAttribute('aria-expanded'),'true');
 click(document.querySelector('.nav-link[href="/settings"]'));await flush();assert.equal(toggle.getAttribute('aria-expanded'),'false');
 assert.ok(document.querySelector('[href="/observability"]'));assert.ok(document.querySelector('[href="/access"]'));
});

test('inventory refresh failures preserve the same data, command nodes and expanded sections',async()=>{
 let failing=false;const {document,pages}=environment('/resources/cache',path=>failing&&path.startsWith('/api/resources/')?Promise.reject(Error('Temporary connection loss')):undefined);await flush();
 const panel=document.querySelector('.detail-panel'),command=document.querySelector('.cli-command');
 failing=true;pages.domain.nextRead=0;await pages.refresh();
 assert.equal(document.querySelector('.detail-panel'),panel);assert.equal(document.querySelector('.cli-command'),command);assert.match(document.getElementById('page-content').textContent,/Ready to accept connections/);assert.match(document.querySelector('.inventory-freshness').textContent,/Keeping last received data/);
 failing=false;pages.domain.nextRead=0;await pages.refresh();assert.equal(document.querySelector('.detail-panel'),panel);assert.doesNotMatch(document.querySelector('.inventory-freshness').textContent,/delayed/);
});

test('observability keeps successful signals on a failed refresh and never presents them under different filters',async()=>{
 let failing=false;const records=[{observedAt:'2026-10-30T12:00:00Z',identity:{application:'faq-demo'},body:{stringValue:'last good request'}}];
 const {document,pages}=environment('/observability',path=>path.includes('signal=logs')?failing?Promise.reject(Error('Temporarily unavailable')):{records}:undefined);await flush();
 const log=document.querySelector('.log-record');const labels=document.querySelector('.log-record details');labels.open=true;
 failing=true;await pages.refresh();assert.equal(document.querySelector('.log-record'),log);assert.equal(labels.open,true);assert.match(document.querySelector('.history-status').textContent,/Showing last received data/);
 pages.appliedFilters=new URLSearchParams('app=other-app');await pages.refresh();assert.equal(document.querySelector('.log-record'),null);assert.doesNotMatch(document.querySelector('.log-records')?.textContent||'',/last good request/);
});

test('selecting the current navigation destination keeps filters and page controls intact',async()=>{
 const {document,click,calls}=environment('/deployments/faq-demo?view=metrics');await flush();const filter=document.querySelector('input[name="component"]');filter.value='unsent-filter';const before=calls.length;
 click(document.querySelector('.nav-link[data-key="metrics"]'));await flush();assert.equal(document.querySelector('input[name="component"]'),filter);assert.equal(filter.value,'unsent-filter');assert.equal(calls.length,before);
});

test('short metadata and diagnostic read failures do not insert banners or rebuild cached details',async()=>{
 let failing=false;const {document,pages}=environment('/deployments/faq-demo',path=>failing&&path==='/api/deployments/faq-demo'?Promise.reject(Error('Connection interrupted')):undefined);await flush();const detail=document.querySelector('.detail-actions'),log=document.querySelector('.log-tail');
 failing=true;await pages.refresh();assert.equal(document.querySelector('.detail-actions'),detail);assert.equal(document.querySelector('.log-tail'),log);assert.equal(pages.error.hidden,true);assert.equal(pages.error.className,'read-warning');
 pages.readFailures.metadata=Date.now()-11000;await pages.refresh();assert.equal(pages.error.hidden,false);assert.match(pages.error.textContent,/Keeping last received data/);assert.equal(document.querySelector('.detail-actions'),detail);
 failing=false;await pages.refresh();assert.equal(pages.error.hidden,true);
});

test('new samples after recovery preserve metric and log anchors and expanded labels',async()=>{
 let tick=0;const base={id:1,observedAt:'2026-10-30T12:00:00Z',identity:{application:'faq-demo',instance:'one'},name:'cpu.usage',unit:'s',kind:'sum',value:1,attributes:{cpu:'0'}};
 const {document,pages}=environment('/observability',path=>path.includes('signal=metrics')?{records:[{...base,id:tick+1,value:tick+1}]}:path.includes('signal=logs')?{records:[...(tick?[{id:2,observedAt:'2026-10-30T12:00:05Z',identity:base.identity,body:'new message'}]:[]),{id:1,observedAt:base.observedAt,identity:base.identity,body:'retained message'}]}:undefined);await flush();
 const metric=document.querySelector('.series-card'),labels=metric.querySelector('details'),log=document.querySelector('.log-record');labels.open=true;log.querySelector('details').open=true;
 tick++;await pages.refresh();assert.equal(document.querySelector('.series-card'),metric);assert.equal(metric.querySelector('details'),labels);assert.equal(labels.open,true);assert.match(metric.textContent,/2 s/);
 assert.equal(document.querySelectorAll('.log-record')[1],log);assert.equal(log.querySelector('details').open,true);
});


test('app metrics cannot drift to another application and do not load global logs',async()=>{
 const {document,pages,calls}=environment('/deployments/faq-demo?view=metrics&app=other-app');await flush();
 assert.equal(document.querySelector('input[name="app"]'),null);assert.ok(calls.every(path=>path.includes('app=faq-demo')&&path.includes('signal=metrics')));
 document.querySelector('input[name="component"]').value='worker';document.querySelector('.observation-filters').dispatchEvent(new window.Event('submit',{bubbles:true,cancelable:true}));await flush();
 assert.equal(location.pathname,'/deployments/faq-demo');assert.equal(new URLSearchParams(location.search).get('view'),'metrics');assert.ok(calls.at(-1).includes('component=worker'));assert.ok(calls.at(-1).includes('app=faq-demo'));
});

test('app activity requests history only for the selected application',async()=>{
 const {document,calls}=environment('/deployments/faq-demo?view=activity');await flush();
 assert.equal(document.querySelectorAll('.history-entry').length,1);assert.ok(calls.every(path=>path.startsWith('/api/activity?')&&path.includes('app=faq-demo')));assert.equal(calls.some(path=>path.endsWith('/logs')),false);
});

test('leaving app metrics discards late samples and preserves the next app dashboard',async()=>{
 let resolve;const pending=new Promise(done=>resolve=done);
 const {document,pages}=environment('/deployments/faq-demo?view=metrics',path=>path.includes('signal=metrics')?pending:undefined);
 history.pushState(null,'','/deployments/faq-demo?view=logs');pages.navigate();await flush();resolve({records:[{name:'OLD-APP',value:99}]});await flush();
 assert.doesNotMatch(document.getElementById('page-content').textContent,/OLD-APP/);assert.ok(document.querySelector('.log-tail'));
});


test('app activity read failures are visible without pretending the history is empty',async()=>{
 const {document}=environment('/deployments/faq-demo?view=activity',path=>path.startsWith('/api/activity?')?Promise.reject(Error('Activity unavailable')):undefined);await flush();
 assert.match(document.getElementById('page-content').textContent,/Activity unavailable/);assert.doesNotMatch(document.getElementById('page-content').textContent,/No entries in this range/);
});

test('controller cards retain identity when status updates',async()=>{
 const {document,pages}=environment();await flush();const card=document.querySelector('.application-card');
 pages.update({...snapshot,applications:[{...snapshot.applications[0],running:false}]},{live:true,busy:false});
 assert.equal(document.querySelector('.application-card'),card);assert.match(card.textContent,/stopped/);
});


test('overview tags distinguish apps and tunnels with the same name and route to the correct details',async()=>{
 const tunnel={name:'faq-demo',status:'connected',bridgeId:'laptop',url:'https://faq-demo.tunnels.example.com'};
 const {document,click}=environment('/overview',path=>path==='/api/tunnels'?[tunnel]:undefined);await flush();
 const cards=[...document.querySelectorAll('.application-card')];assert.equal(cards.length,2);
 assert.deepEqual(cards.map(card=>card.querySelector('.workload-type').textContent),['App','Tunnel']);
 assert.equal(cards[0].getAttribute('href'),'/deployments/faq-demo');assert.equal(cards[1].getAttribute('href'),'/tunnels/faq-demo');assert.match(cards[1].textContent,/connected/);
 click(cards[1]);await flush();assert.match(document.getElementById('page-content').textContent,/Connection limit4/);assert.match(document.getElementById('page-content').textContent,/laptop/);
 assert.equal(document.querySelector('.nav-link[aria-current="page"]').getAttribute('href'),'/overview');assert.equal(document.querySelector('.log-tail'),null);
});

test('tunnel failures preserve app inventory and the last known tunnel status',async()=>{
 let fail=false;const {document,pages}=environment('/overview',path=>path==='/api/tunnels'?fail?Promise.reject(Error('offline')):[{name:'demo',bridgeId:'laptop',status:'connected'}]:undefined);await flush();
 fail=true;await pages.refresh();assert.equal(document.querySelectorAll('.application-card').length,2);assert.match(document.getElementById('page-content').textContent,/Tunnel status refresh delayed/);
});

test('an unavailable tunnel gateway never hides deployed apps',async()=>{
 const {document}=environment('/overview',path=>path==='/api/tunnels'?Promise.reject(Object.assign(Error('Tunnel gateway is not configured'),{code:'INVALID_CONFIG'})):undefined);await flush();
 assert.equal(document.querySelectorAll('.application-card').length,1);assert.equal(document.querySelector('.workload-type').textContent,'App');assert.match(document.getElementById('page-content').textContent,/not configured/);
});
