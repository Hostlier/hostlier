import test from 'node:test';
import assert from 'node:assert/strict';
import {parseHTML} from 'linkedom';
import {HistoryView} from '../src/history.js';
const hour=3600000;
const flush=async()=>{await new Promise(setImmediate);await new Promise(setImmediate);};
function fixture({kind='activity',app='demo',response,path='/deployments/demo?view=activity'}={}){
 const {window,document}=parseHTML('<html><body><main></main></body></html>');globalThis.window=window;globalThis.document=document;globalThis.location=new URL(path,'https://dashboard.test');globalThis.history={replaceState:(_,__,path)=>{globalThis.location=new URL(path,location);}};
 const now=Date.parse('2026-11-02T12:00:00Z'),calls=[];
 const api=async path=>{calls.push(new URL(path,location));return response?response(calls.at(-1),calls.length):{entries:[{sequence:1,timestamp:new Date(now-1000).toISOString(),level:'info',message:'Deployment ready',application:'demo'}],retentionDays:30};};
 const view=new HistoryView(document.querySelector('main'),{api,current:()=>true,kind,app,now:()=>now});
 const click=node=>node.dispatchEvent(new window.Event('click',{bubbles:true}));const submit=()=>document.querySelector('form').dispatchEvent(new window.Event('submit',{bubbles:true,cancelable:true}));
 const select=(node,value)=>{for(const option of node.children)option.selected=false;[...node.children].find(option=>option.value===value).selected=true;};
 return {document,window,view,calls,now,click,submit,select};
}

test('defaults to two hours, shifts by one hour, and expands without changing app scope',async()=>{
 const e=fixture();await e.view.refresh();let q=e.calls.at(-1).searchParams;
 assert.equal(Date.parse(q.get('to'))-Date.parse(q.get('from')),2*hour);assert.equal(q.get('app'),'demo');
 e.click(e.view.olderWindow);await flush();q=e.calls.at(-1).searchParams;assert.equal(Date.parse(q.get('to')),e.now-hour);assert.equal(Date.parse(q.get('from')),e.now-3*hour);
 e.click(e.view.newerWindow);await flush();assert.equal(e.view.to,e.now);
 e.click(e.view.expand);await flush();assert.equal(e.view.to-e.view.from,4*hour);
 e.click(e.view.live);await flush();assert.equal(e.view.relative,2*hour);assert.equal(e.view.newerWindow.disabled,true);
});

test('custom dates, text and level are sent to the server and survive a shareable URL',async()=>{
 const e=fixture();await e.view.refresh();e.select(e.view.range,'custom');e.view.fromInput.value='2026-11-01T09:00';e.view.toInput.value='2026-11-01T11:00';
 e.view.search.value='timeout 100%';e.select(e.view.level,'error');e.submit();await flush();
 const q=e.calls.at(-1).searchParams;assert.equal(q.get('q'),'timeout 100%');assert.equal(q.get('level'),'error');assert.equal(q.get('from'),new Date('2026-11-01T09:00').toISOString());assert.equal(new URLSearchParams(location.search).get('q'),'timeout 100%');
 const before=e.calls.length;e.view.toInput.value='2026-10-01T11:00';e.submit();await flush();assert.equal(e.calls.length,before);assert.match(e.view.error.textContent,/start before the end/);
});

test('pagination freezes its time range and sends both cursor components',async()=>{
 const stamp='2026-11-02T11:59:00Z';const e=fixture({response:(_,n)=>n===1?{entries:[{sequence:2,timestamp:stamp,message:'second'}],nextBefore:2,nextBeforeTime:stamp}:{entries:[{sequence:1,timestamp:stamp,message:'first'}]}});
 await e.view.refresh();const original=e.calls[0].searchParams;e.click(e.view.more);await flush();const q=e.calls.at(-1).searchParams;
 assert.equal(q.get('before'),'2');assert.equal(q.get('beforeTime'),stamp);assert.equal(q.get('from'),original.get('from'));assert.equal(e.view.records.length,2);assert.equal(e.view.relative,0);
});

test('late search results cannot replace a newer window',async()=>{
 let resolve;const pending=new Promise(done=>resolve=done);const e=fixture({response:(_,n)=>n===1?pending:{entries:[{sequence:2,timestamp:'2026-11-02T10:00:00Z',message:'new window'}]}});
 e.view.refresh();e.click(e.view.olderWindow);await flush();resolve({entries:[{sequence:1,timestamp:'2026-11-02T11:00:00Z',message:'OLD WINDOW'}]});await flush();assert.match(e.view.list.textContent,/new window/);assert.doesNotMatch(e.view.list.textContent,/OLD WINDOW/);
});

test('logs preserve arbitrary text safely and new filters clear cached results on failure',async()=>{
 let fail=false;const e=fixture({kind:'logs',response:()=>fail?Promise.reject(Error('Unavailable')):{records:[{id:1,observedAt:'2026-11-02T11:00:00Z',body:{stringValue:'<script>unsafe()</script>'}}]}});
 await e.view.refresh();assert.equal(e.calls[0].searchParams.get('signal'),'logs');assert.equal(e.document.querySelector('script'),null);assert.match(e.view.list.textContent,/<script>/);
 fail=true;e.view.search.value='different';e.submit();await flush();assert.equal(e.document.querySelector('.history-entry'),null);assert.match(e.view.error.textContent,/Unavailable/);
});

test('30-day preset uses an exact bounded interval and survives reload',async()=>{
 const e=fixture();e.select(e.view.range,'720');e.submit();await flush();assert.equal(e.view.to-e.view.from,30*24*hour);assert.equal(e.view.expand.disabled,true);assert.equal(new URLSearchParams(location.search).get('window'),'720');
});

test('live refresh preserves expanded entries and their DOM identity',async()=>{
 const e=fixture();await e.view.refresh();const row=e.document.querySelector('.history-entry');row.open=true;
 await e.view.refresh();assert.equal(e.document.querySelector('.history-entry'),row);assert.equal(row.open,true);
});

test('live refresh preserves a time range being edited before Search',async()=>{
 const e=fixture();await e.view.refresh();e.select(e.view.range,'custom');e.view.range.dispatchEvent(new e.window.Event('change'));
 e.view.fromInput.value='2026-11-01T09:00';await e.view.refresh();assert.equal(e.view.range.value,'custom');assert.equal(e.view.fromInput.value,'2026-11-01T09:00');assert.equal(e.view.custom.hidden,false);
});
