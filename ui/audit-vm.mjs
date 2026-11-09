// Opt-in integration audit: render real VM responses, follow links, and verify CLI handoffs.
// Run with HOSTLIER_UI_TOKEN from the local dashboard URL; credentials stay in the CLI.
import assert from 'node:assert/strict';
import {readFileSync,writeFileSync} from 'node:fs';
import {spawnSync} from 'node:child_process';
import {parseHTML} from 'linkedom';
import {Pages} from './src/views.js';

const origin=new URL(process.env.HOSTLIER_UI_URL||'http://127.0.0.1:4177');
assert.ok(['127.0.0.1','localhost','[::1]'].includes(origin.hostname),'Audit a loopback CLI dashboard');
const token=process.env.HOSTLIER_UI_TOKEN;
assert.ok(token,'Set HOSTLIER_UI_TOKEN to the session fragment printed by hostlier ui');
const headers={'X-Hostlier-UI':token};
const html=readFileSync(new URL('./src/index.html',import.meta.url),'utf8');
const cache=new Map(),apiFailures=[],pending=new Set();
async function read(path){
 if(!cache.has(path))cache.set(path,(async()=>{
  const response=await fetch(new URL(path,origin),{headers,signal:AbortSignal.timeout(20000)});
  const data=await response.json();
  if(!response.ok){apiFailures.push({path,status:response.status,code:data.error?.code});throw Error(data.error?.message||`HTTP ${response.status}`);}
  return data;
 })());
 return cache.get(path);
}
function api(path){const promise=read(path);pending.add(promise);promise.then(()=>pending.delete(promise),()=>pending.delete(promise));return promise;}
async function settle(){
 for(let i=0;i<20;i++){await Promise.allSettled([...pending]);await new Promise(setImmediate);if(!pending.size)return;}
 throw Error('Page did not finish reading');
}
async function snapshot(){
 const response=await fetch(new URL('/api/events',origin),{headers,signal:AbortSignal.timeout(15000)});
 assert.equal(response.status,200);const reader=response.body.getReader();let buffer='';
 try{while(!buffer.includes('\n\n')){const {value,done}=await reader.read();if(done)break;buffer+=new TextDecoder().decode(value);assert.ok(buffer.length<4194304);}}
 finally{await reader.cancel();}
 const line=buffer.split('\n').find(line=>line.startsWith('data: '));assert.ok(line,'Status stream has an event');
 const event=JSON.parse(line.slice(6));assert.ok(event.connected&&event.snapshot,'VM is connected');
 assert.ok(Date.now()-new Date(event.snapshot.observedAt)<15000,'Shared capture is fresh');return event.snapshot;
}
const connection=await read('/api/connection'),capture=await snapshot();
assert.ok(connection.name&&connection.cli?.binary,'Selected controller and CLI invocation are available');
const routes=new Set(['/overview','/deployments','/operations','/observability','/resources','/hosts','/recovery','/access','/settings']);
const inventories=[];for(const path of ['/api/deployments','/api/operations','/api/resources','/api/hosts','/api/recovery/jobs'])inventories.push(await read(path));
for(const [i,key,page]of [[0,'applications','deployments'],[1,'operations','operations'],[2,'resources','resources']])for(const row of inventories[i][key]||[])routes.add('/'+page+'/'+encodeURIComponent(row.id||row.name));
for(const [i,key,page]of [[3,'hosts','hosts'],[4,'jobs','recovery']])for(const [id,row]of Object.entries(inventories[i][key]||{}))routes.add('/'+page+'/'+encodeURIComponent(row.name||row.id||id));
const links=new Set(),commands=new Map(),pages=[];
for(const route of routes){
 const shell=await fetch(new URL(route,origin),{signal:AbortSignal.timeout(10000)});assert.equal(shell.status,200,'Deep link '+route);
 const {document,window}=parseHTML(html);
 Object.assign(globalThis,{document,window,location:new URL(route,origin),setInterval:()=>0,addEventListener:()=>{}});window.scrollTo=()=>{};
 globalThis.history={pushState:(_,__,url)=>{globalThis.location=new URL(url,location);},replaceState:(_,__,url)=>{globalThis.location=new URL(url,location);}};
 Object.defineProperty(window.HTMLAnchorElement.prototype,'pathname',{configurable:true,get(){return new URL(this.href,location).pathname;}});
 const page=new Pages(document.getElementById('page-content'),{api});page.setConnection(connection);page.update(capture,{live:true,busy:false});await settle();
 const root=document.getElementById('page-content');assert.equal(page.error.hidden,true,'Page error on '+route+': '+page.error.textContent);
 assert.doesNotMatch(root.textContent,/Uncaptured endpoint|\[object Object\]|undefined/,'Rendered values on '+route);
 for(const link of document.querySelectorAll('a[data-route]')){
  const target=new URL(link.href,origin);assert.equal(target.origin,origin.origin,'Internal link origin');
  const relative=target.pathname+target.search;links.add(relative);routes.add(relative);
  const parts=target.pathname.split('/').filter(Boolean);
  if(parts.length===2)await read('/api/'+parts.join('/'));
 }
 for(const code of root.querySelectorAll('.cli-command'))commands.set(code.textContent,{line:code.textContent,args:JSON.parse(code.dataset.cliArgs)});
 // Exercise the real copy listener with controller-specific command text.
 const copied=[];Object.defineProperty(globalThis,'navigator',{configurable:true,value:{clipboard:{writeText:async value=>{copied.push(value);}}}});
 const copy=root.querySelector('.command-card button');if(copy){copy.dispatchEvent(new window.Event('click'));await new Promise(setImmediate);assert.ok([...commands.keys()].includes(copied[0]),'Copy command on '+route);}
 // Exercise every declared component's log selector, including the real VM sidecars.
 if(page.component)for(const option of page.component.querySelectorAll('option'))await read('/api/deployments/'+encodeURIComponent(page.identity)+'/logs'+(option.value?'?component='+encodeURIComponent(option.value):''));
 pages.push({route,title:document.title});
}
assert.deepEqual(apiFailures,[],'All real inventory/detail reads should work');
let syntaxValidated=0,executedReads=0,templates=0;
for(const {line,args}of commands.values()){
 const help=spawnSync('/bin/sh',['-c',line+' --help'],{encoding:'utf8',timeout:15000,maxBuffer:512*1024});
 assert.equal(help.status,0,'CLI argument validation: '+args.join(' ')+': '+help.stderr);syntaxValidated++;
 const safe=['status','list','doctor','operation'].includes(args[0])||(args[0]==='resource'&&['status','upgrade-status'].includes(args[1]))||(args[0]==='backup'&&['list','job'].includes(args[1]))||(args[0]==='authorizations'&&args[1]==='list');
 if(!safe){templates++;continue;}
 const result=spawnSync('/bin/sh',['-c',line],{encoding:'utf8',timeout:15000,maxBuffer:1024*1024});
 const operation=args[0]==='operation'?inventories[1].operations.find(row=>row.id===args[1]):null;
 const terminalFailure=operation&&['failed','interrupted'].includes(operation.status)&&result.status===1&&/OPERATION_FAILED|OPERATION_INTERRUPTED|operation failed|Operation failed/.test(result.stderr);
 assert.ok(result.status===0||terminalFailure,'CLI read: '+args.join(' ')+': '+result.stderr);executedReads++;
}
// History can be empty before a real application emits logs. Both signal contracts must work.
const signals={};
for(const signal of ['metrics','logs']){
 const path='/api/observability?signal='+signal+'&limit=100';const data=await read(path);signals[signal]=(data.records||[]).length;
 for(const record of data.records||[]){assert.equal(record.signal,signal);assert.ok(record.identity?.application,'Owned workload identity');}
 if(signal==='logs'&&data.nextBefore){const older=await read(path+'&before='+data.nextBefore);assert.ok((older.records||[]).every(row=>row.id<data.nextBefore),'Log history cursor');}
}
const report={checkedAt:new Date().toISOString(),pages:pages.length,links:links.size,apiReads:cache.size,cli:{syntaxValidated,executedReads,templates},signals,unconfigured:{fleetPeers:Object.keys(inventories[3].hosts||{}).length===0,recoveryJobs:Object.keys(inventories[4].jobs||{}).length===0}};
if(process.env.HOSTLIER_UI_AUDIT_REPORT)writeFileSync(process.env.HOSTLIER_UI_AUDIT_REPORT,JSON.stringify(report,null,2)+'\n',{mode:0o600});
console.log(JSON.stringify(report,null,2));
