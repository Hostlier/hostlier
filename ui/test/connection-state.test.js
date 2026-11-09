import test from 'node:test';
import assert from 'node:assert/strict';
import {ConnectionState} from '../src/connection-state.js';
const now=Date.parse('2026-10-30T12:00:00Z');
const snapshot={observedAt:new Date(now).toISOString(),hostname:'vm',applications:[{name:'app'}]};
test('normal stream rotation retains data and keeps the presentation steady',()=>{
 const state=new ConnectionState();state.receive({connected:true,snapshot});
 const before=state.view(true,true,now);assert.equal(before.live,true);
 state.receive({connected:false});const rotating=state.view(true,true,now+100);
 assert.equal(rotating.snapshot,snapshot);assert.equal(rotating.live,false);assert.equal(rotating.label,before.label);assert.equal(rotating.caption,before.caption);assert.equal(rotating.tone,before.tone);
 state.receive({connected:true,snapshot});assert.equal(state.view(true,true,now+400).label,before.label);
});
test('sustained local or upstream interruptions become explicit without clearing data',()=>{
 const state=new ConnectionState();state.receive({connected:true,snapshot});state.view(true,true,now);
 state.view(false,true,now+100);assert.equal(state.view(false,true,now+4000).label,'Reconnecting');
 const paused=state.view(false,true,now+32000);assert.equal(paused.label,'Updates paused');assert.equal(paused.caption,'Stale · showing last capture');assert.equal(paused.stale,true);assert.equal(paused.snapshot,snapshot);
 const recovered={...snapshot,observedAt:new Date(now+33000).toISOString()};state.receive({connected:true,snapshot:recovered});assert.equal(state.view(true,true,now+33000).live,true);
});
test('only an explicit controller reset clears old observations, once per authority change',()=>{
 const state=new ConnectionState();state.receive({connected:true,snapshot});state.receive({connected:false,snapshot:null});assert.equal(state.snapshot,snapshot);
 assert.equal(state.receive({reset:true,connected:false}),true);assert.equal(state.snapshot,null);assert.equal(state.receive({reset:true,connected:false}),false);
});
test('lost authorization is shown immediately even with a fresh retained capture',()=>{
 const state=new ConnectionState();state.receive({connected:true,snapshot});const view=state.view(true,false,now);
 assert.equal(view.live,false);assert.equal(view.label,'Session required');assert.equal(view.tone,'idle');
});
test('repeated initial reset heartbeats cannot hide a sustained first-connection failure',()=>{
 const state=new ConnectionState();state.view(true,true,now);state.receive({reset:true});state.view(true,true,now+5000);state.receive({reset:true});assert.equal(state.view(true,true,now+31000).label,'Unable to connect');
});
