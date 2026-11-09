// Opt-in, real VM transport fault. Browser layout requires a separate enabled browser check.
// Runs the shipped dashboard bundle with real fetch/SSE in a DOM harness; scroll calls are instrumented.
import assert from 'node:assert/strict';
import {readFileSync,writeFileSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import vm from 'node:vm';
import https from 'node:https';
import {parseHTML} from 'linkedom';

const name=process.env.HOSTLIER_UI_FAULT_VM;
assert.equal(name,'hostlier-ui-outage','Only the dedicated disposable VM may be interrupted');
const token=process.env.HOSTLIER_UI_TOKEN;assert.ok(token,'Set HOSTLIER_UI_TOKEN from the running CLI URL');
const origin=process.env.HOSTLIER_UI_URL||'http://127.0.0.1:4177';assert.equal(new URL(origin).hostname,'127.0.0.1');
const unit='hostlier-ui-outage-tls.service';
const guest=(...args)=>execFileSync('limactl',['shell',name,'sudo',...args],{encoding:'utf8',timeout:20000,maxBuffer:1024*1024}).trim();
assert.equal(guest('cat','/root/hostlier-ui-outage/fixture'),'hostlier-ui-outage-v1');
assert.equal(guest('hostname'),'lima-hostlier-ui-outage');
const read=async path=>{const response=await fetch(origin+path,{headers:{'X-Hostlier-UI':token},signal:AbortSignal.timeout(15000)});assert.equal(response.status,200,path);return response.json();};
const connection=await read('/api/connection');assert.equal(new URL(connection.url).port,'17464');
const probeTransport=()=>new Promise((resolve,reject)=>{const request=https.get(connection.url+'/v1/status/events',{ca:readFileSync(connection.cli.caFile),timeout:3000},response=>{response.destroy();resolve();});request.on('error',reject);request.on('timeout',()=>request.destroy(new Error('Transport timed out')));});
await probeTransport();
const app=await read('/api/deployments/coexist-new');assert.equal(app.status,'running');
const baseline={controller:guest('systemctl','show','hostlier','--property=MainPID','--value'),workload:guest('docker','inspect','--format','{{.Id}} {{.State.StartedAt}} {{.RestartCount}}',app.runtimeId)};
const query=new URLSearchParams({app:'coexist-new',component:'app',revision:app.revision});
const {document,window}=parseHTML(readFileSync(new URL('./src/index.html',import.meta.url),'utf8'));
const timers=new Set(),intervals=new Set(),requests=new Set(),scrollCalls=[],focusCalls=[];
let stopped=false,context,armed=false;const realTimeout=setTimeout,realInterval=setInterval;
Object.defineProperty(window,'scrollY',{configurable:true,writable:true,value:0});
window.scrollTo=(x,y)=>{window.scrollY=typeof x==='object'?x.top:y;scrollCalls.push(window.scrollY);};
window.HTMLElement.prototype.focus=function(){focusCalls.push(this.id||this.tagName);};
const sandbox={document,window,URL,URLSearchParams,TextDecoder,JSON,Date,console,Promise,AbortController,
 location:new URL('/observability?'+query,origin),
 navigator:{clipboard:{writeText:async()=>{}}},
 sessionStorage:{getItem:key=>key==='hostlier-ui'?token:null,setItem:()=>{}},
 addEventListener:(...args)=>window.addEventListener(...args),
 setInterval:(fn,ms)=>{if(stopped)return 0;const timer=realInterval(fn,ms);intervals.add(timer);return timer;},clearInterval,
 setTimeout:(fn,ms)=>{if(stopped)return 0;const timer=realTimeout(fn,ms);timers.add(timer);return timer;},clearTimeout,
 fetch:async(path,options={})=>{const controller=new AbortController();requests.add(controller);return fetch(new URL(path,origin),{...options,signal:controller.signal});},
};
sandbox.history={pushState:(_,__,url)=>{context.location=new URL(url,context.location);},replaceState:(_,__,url)=>{context.location=new URL(url,context.location);}};
Object.defineProperty(window.HTMLAnchorElement.prototype,'pathname',{configurable:true,get(){return new URL(this.href,context.location).pathname;}});
context=vm.createContext(sandbox);
const wait=ms=>new Promise(resolve=>realTimeout(resolve,ms));
async function until(predicate,label,timeout=60000){const start=Date.now();while(Date.now()-start<timeout){if(predicate())return;await wait(250);}throw Error('Timed out: '+label);}
const timerUnit='hostlier-ui-outage-recover-'+Date.now();
let report;
try{
 vm.runInContext(readFileSync(new URL('../internal/localui/assets/app.js',import.meta.url),'utf8'),context,{filename:'embedded-app.js'});
 await until(()=>document.querySelector('.series-card')&&document.querySelector('.log-record')&&document.getElementById('connection').textContent==='Up to date','real metrics, logs and fresh shared capture');
 // Applied filters remain in the URL; unsent input must also survive refresh and recovery.
 const controls=[...document.querySelectorAll('.observation-filters input')];controls.find(input=>input.name==='revision').value='typed-but-not-applied';
 const filters=controls.map(input=>input.value),url=context.location.href;
 const metric=document.querySelector('.series-card'),log=document.querySelector('.log-record');metric.querySelector('details').open=true;log.querySelector('details').open=true;
 window.scrollY=920;document.documentElement.scrollTop=920;const initialScrollCalls=scrollCalls.length,initialFocus=focusCalls.length;
 function stable(){
  assert.equal(context.location.href,url,'Applied URL filters changed');
  controls.forEach((input,index)=>{assert.equal(document.querySelector(`input[name="${input.name}"]`),input,'Filter control rebuilt');assert.equal(input.value,filters[index],'Typed filter changed');});
  assert.equal(window.scrollY,920,'Dashboard changed instrumented scroll offset');assert.equal(scrollCalls.length,initialScrollCalls,'Dashboard issued a scroll reset');assert.equal(focusCalls.length,initialFocus,'Dashboard moved focus');
  assert.equal(document.getElementById('notice').hidden,true,'A connection banner changed page layout');
  assert.ok(metric.isConnected,'Metric scroll anchor removed');assert.ok(log.isConnected,'Log scroll anchor removed');
  assert.equal(metric.querySelector('details').open,true,'Metric labels collapsed');assert.equal(log.querySelector('details').open,true,'Log labels collapsed');
 }
 console.log(JSON.stringify({phase:'ready',realMetrics:document.querySelectorAll('.series-card').length,realLogs:document.querySelectorAll('.log-record').length}));
 guest('systemd-run','--unit',timerUnit,'--on-active=65s','/bin/systemctl','start',unit);armed=true;
 const started=Date.now();guest('systemctl','stop',unit);
 assert.equal(guest('systemctl','is-active','hostlier'),'active');
 let transportRejected=false;try{await probeTransport();}catch{transportRejected=true;}assert.ok(transportRejected,'VM TLS transport was not actually interrupted');
 await until(()=>document.getElementById('connection').textContent==='Reconnecting','real disconnected state',15000);stable();
 const beforeCapture=document.getElementById('captured').textContent;
 await until(()=>document.getElementById('capture-status').textContent.includes('Stale'),'explicit stale capture state',25000);stable();
 assert.match(document.querySelector('.facts').textContent,/Stale · last known delivery/);
 await until(()=>document.getElementById('connection').textContent==='Updates paused','sustained outage state',45000);stable();
 while(Date.now()-started<40000){await wait(250);stable();}
 const retainedCapture=document.getElementById('captured').textContent;assert.equal(retainedCapture,beforeCapture,'Outage timestamp incorrectly advanced');
 console.log(JSON.stringify({phase:'stale',header:document.getElementById('connection').textContent,caption:document.getElementById('capture-status').textContent,dataRetained:true,filtersRetained:true,scrollCallsUnchanged:true}));
 const restored=Date.now();guest('systemctl','start',unit);
 await until(()=>document.getElementById('connection').textContent==='Up to date'&&document.getElementById('captured').textContent!==retainedCapture,'fresh capture after transport recovery',30000);
 await until(()=>!document.querySelector('.history-status').textContent.includes('Retrying'),'live history resumes',20000);
 await wait(6000);stable();
 assert.equal(guest('systemctl','show','hostlier','--property=MainPID','--value'),baseline.controller,'Controller restarted');
 assert.equal(guest('docker','inspect','--format','{{.Id}} {{.State.StartedAt}} {{.RestartCount}}',app.runtimeId),baseline.workload,'Workload restarted');
 report={checkedAt:new Date().toISOString(),fault:'Stopped VM TLS transport for 40 seconds',recoverySeconds:Number(((Date.now()-restored)/1000).toFixed(2)),retainedData:true,appliedFilters:true,unsentFilters:true,expandedLabels:true,staleIndicator:true,freshCaptureResumed:true,controllerUnchanged:true,workloadUnchanged:true,scrollCallsUnchanged:true,instrumentedScrollY:window.scrollY,renderedScrollVerified:false,renderedScrollLimitation:'In-app browser control is disabled in Work settings; pixel/layout scroll requires an enabled browser check.'};
 console.log(JSON.stringify(report,null,2));
 if(process.env.HOSTLIER_UI_AUDIT_REPORT)writeFileSync(process.env.HOSTLIER_UI_AUDIT_REPORT,JSON.stringify(report,null,2)+'\n',{mode:0o600});
}finally{
 // Restore the exact test transport even on assertion failure; the guest timer is a second safeguard.
 if(armed){try{guest('systemctl','start',unit);guest('systemctl','stop',timerUnit+'.timer');}catch(error){console.error('Transport recovery safeguard:',error.message);}}
 stopped=true;for(const timer of intervals)clearInterval(timer);for(const timer of timers)clearTimeout(timer);for(const controller of requests)controller.abort();
}
