import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {parseHTML} from 'linkedom';
const assets = new URL('../../internal/controller/accessui/', import.meta.url);
const html = readFileSync(new URL('index.html', assets), 'utf8');
const script = readFileSync(new URL('access.js', assets), 'utf8');
const flush = async () => { await new Promise(setImmediate); await new Promise(setImmediate); };
const pair = {id:'request-1', name:'work-laptop', deviceFingerprint:'sha256:device', expiresAt:Date.now()+600000, approved:false};
function environment(path='/connect?request=request-1', review=pair, credential) {
  const {window,document} = parseHTML(html);
  window.PublicKeyCredential = function() {};
  const calls=[], timers=[], redirects=[];
  const context = {document, window, URLSearchParams, Uint8Array, atob, btoa, console,
    location: Object.assign(new URL(path,'https://controller.example.com'), {replace: url => redirects.push(url)}),
    history: {replaceState: (...args) => redirects.push(args[2])},
    setInterval: fn => {timers.push(fn);return timers.length;}, clearInterval:()=>{},
    navigator: {credentials:{get:credential || (async()=>proof), create:async()=>proof}},
    fetch: async (url, init) => {calls.push({url,...init});return {ok:true,json:async()=>url.endsWith('/begin') ? {ceremony:'ceremony-1',options:{publicKey:{challenge:'YQ'}}} : url.endsWith('/finish') ? {code:'ABCD-EFGH'} : review};}
  };
  const proof = {id:'credential',rawId:new Uint8Array([1]).buffer,type:'public-key',response:{clientDataJSON:new Uint8Array([2]).buffer},getClientExtensionResults:()=>({})};
  vm.runInNewContext(script,context);
  const el=id=>document.getElementById(id);
  const click=node=>node.dispatchEvent(new window.Event('click',{bubbles:true}));
  const restrict=()=>{el('mode-all').checked=false;el('mode-restricted').checked=true;el('scope').dispatchEvent(new window.Event('change',{bubbles:true}));};
  return {document,el,calls,timers,redirects,click,restrict};
}

test('pairing shows safe device facts, explicit full access, and completes the existing ceremony',async()=>{
  const e=environment(undefined,{...pair,name:'<img src=x onerror=alert(1)>'});await flush();
  assert.match(e.el('request-facts').textContent,/<img src=x/);assert.equal(e.document.querySelector('img'),null);
  assert.match(e.el('scope-summary').textContent,/write access/);
  e.click(e.el('actions').firstChild);await flush();
  const begin=e.calls.find(c=>c.url.endsWith('/begin'));
  assert.deepEqual(JSON.parse(begin.body),{id:pair.id,scope:{}});
  assert.equal(e.calls.at(-1).headers['X-Hostlier-Ceremony'],'ceremony-1');
  assert.match(e.el('status').textContent,/finish connecting automatically/);assert.equal(e.el('actions').children.length,0);
});
test('empty restricted selection cannot silently become full access',async()=>{
  const e=environment();await flush();e.restrict();e.click(e.el('actions').firstChild);await flush();
  assert.equal(e.calls.length,1);assert.equal(e.el('status').dataset.tone,'error');
  assert.match(e.el('status').textContent,/1–20 application names/);assert.equal(e.el('mode-all').disabled,false);
});
test('restricted apps, secret creation and exact limits are sent to the existing endpoint',async()=>{
  const e=environment();await flush();e.restrict();
  for(const [id,value] of Object.entries({apps:'minion-stocks, portal',minutes:'60',uses:'10',secrets:'api-token','create-secrets':'api-token'}))e.el(id).value=value;
  e.click(e.el('actions').firstChild);await flush();
  assert.deepEqual(JSON.parse(e.calls.find(c=>c.url.endsWith('/begin')).body).scope,{apps:['minion-stocks','portal'],secrets:['api-token'],createSecrets:['api-token'],durationSeconds:3600,maxUses:10});
});
test('competing decisions and scope changes lock during verification; cancellation restores retry',async()=>{
  let cancel;const pending=new Promise((_,reject)=>{cancel=reject;});
  const e=environment('/approve?request=request-1',{...pair,status:'pending',fingerprint:'exact-intent',review:{changes:{purge:true}}},()=>pending);await flush();
  const [approve,reject]=e.el('actions').children;e.click(approve);await flush();
  assert.equal(approve.disabled,true);assert.equal(reject.disabled,true);e.click(reject);await flush();
  assert.equal(e.calls.filter(c=>c.url.endsWith('/begin')).length,1);
  cancel(Object.assign(new Error('cancelled'),{name:'NotAllowedError'}));await flush();
  assert.equal(approve.disabled,false);assert.match(e.el('status').textContent,/cancelled or timed out/);
});
test('operation keeps nested review visible and rejects using the exact fingerprint',async()=>{
  const e=environment('/approve?request=request-1',{...pair,status:'pending',fingerprint:'exact-intent',review:{warning:'Deletes data',plan:{volumes:[{name:'db',retain:false}]}}});await flush();
  assert.match(e.el('operation-review').textContent,/Deletes data/);assert.match(e.el('operation-review').textContent,/false/);
  e.click(e.el('actions').lastChild);await flush();
  assert.deepEqual(JSON.parse(e.calls.find(c=>c.url.endsWith('/begin')).body),{id:pair.id,fingerprint:'exact-intent',decision:'reject'});
  assert.equal(e.el('request-state').textContent,'Rejected');
});
test('expired and previously approved requests cannot initiate another ceremony',async()=>{
  const expired=environment(undefined,{...pair,expiresAt:Date.now()-1000});await flush();
  assert.equal(expired.el('actions').firstChild.disabled,true);expired.click(expired.el('actions').firstChild);await flush();assert.equal(expired.calls.length,1);
  const approved=environment(undefined,{...pair,approved:true});await flush();assert.equal(approved.el('actions').children.length,0);assert.equal(approved.el('scope').hidden,true);
});
test('enrollment removes token from URL and preserves the request handoff',async()=>{
  const e=environment('/security#enroll=one-time-token&request=request-1');await flush();
  assert.equal(e.redirects[0],'/security');e.click(e.el('actions').firstChild);await flush();
  assert.deepEqual(JSON.parse(e.calls[0].body),{token:'one-time-token'});assert.equal(e.redirects.at(-1),'/connect?request=request-1');
});
test('missing operation ID fails without fetching or offering approval',async()=>{
  const e=environment('/approve');await flush();assert.equal(e.calls.length,0);assert.equal(e.el('actions').children.length,0);assert.match(e.el('status').textContent,/missing its request ID/);
});
