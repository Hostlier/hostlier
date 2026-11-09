import {readAPIResponse,DashboardRequestError,sessionRequiredMessage} from './api-response.js';
import {ConnectionState} from './connection-state.js';
import {Pages} from './views.js';
import { text as safeText } from './components.js';
const $ = id => document.getElementById(id);
const supplied = new URLSearchParams(location.hash.slice(1)).get('session');
if (supplied) { sessionStorage.setItem('hostlier-ui', supplied); history.replaceState(null, '', location.pathname+location.search); }
const token = sessionStorage.getItem('hostlier-ui') || '';
let sessionAuthorized=!!token;
const connection = new ConnectionState();
let localConnected = false;
let busy = false;
const validTime = value => value && new Date(value).getUTCFullYear() > 2000;
const pages = new Pages($('page-content'), {api});
async function api(path, body) {
  if(!sessionAuthorized)throw new DashboardRequestError(sessionRequiredMessage,'UI_SESSION_REQUIRED',403);
  const response = await fetch(path, {method:body === undefined ? 'GET' : 'POST', headers:{'X-Hostlier-UI':token, 'Content-Type':'application/json'}, body:body === undefined ? undefined : JSON.stringify(body), cache:'no-store'});
  try{return await readAPIResponse(response);}
  catch(error){if(error.code==='UI_SESSION_REQUIRED'){sessionAuthorized=false;localConnected=false;render();}throw error;}
}
function render() {
  const view=connection.view(localConnected,sessionAuthorized);const snapshot=view.snapshot;
  safeText($('connection'),view.label);$('connection').title=view.detail;
  $('connection-dot').classList.toggle('live',view.tone==='live');
  safeText($('capture-status'),view.caption);
  // The header carries connection state without adding/removing a banner above the current page.
  $('notice').hidden=sessionAuthorized&&!snapshot?.truncated;
  safeText($('notice'),!sessionAuthorized?sessionRequiredMessage:'Overview captures up to 128 applications and operations. The Applications and Activity pages load the complete retained inventory.');
  pages.update(snapshot,{live:view.live,stale:view.stale,busy});
  if (!snapshot) { safeText($('captured'),'Waiting for data');safeText($('server'),pages.connection?.name||'No controller linked');safeText($('runtime'),'Waiting for controller');return; }
  safeText($('server'),snapshot.hostname);safeText($('runtime'),`${snapshot.runtime} runtime`);
  safeText($('captured'),validTime(snapshot.observedAt)?new Date(snapshot.observedAt).toLocaleTimeString():'Capture pending');
}
async function watch() {
  if (!token) { render(); return; }
  while (true) {
    try {
      const response=await fetch('/api/events',{headers:{'X-Hostlier-UI':token},cache:'no-store'});
      if(response.status===403){sessionAuthorized=false;localConnected=false;render();return;}
      if (!response.ok || !response.body) throw new Error('Local stream unavailable');
      localConnected=true;
      const reader=response.body.getReader(); const decoder=new TextDecoder(); let buffer='';
      try {
        while (true) {
          const {value,done}=await reader.read(); if (done) break;
          buffer+=decoder.decode(value,{stream:true});
          if (buffer.length>4194304) throw new Error('Snapshot exceeds limit');
          let boundary;
          while ((boundary=buffer.indexOf('\n\n'))!==-1) {
            const frame=buffer.slice(0,boundary); buffer=buffer.slice(boundary+2);
            const line=frame.split('\n').find(part=>part.startsWith('data: '));
            if (line) { if(connection.receive(JSON.parse(line.slice(6))))pages.reset(); render(); }
          }
        }
      } finally { await reader.cancel(); }
    } catch { /* Cached values remain visible with explicit disconnected state. */ }
    localConnected=false; render();
    await new Promise(resolve=>setTimeout(resolve,1500));
  }
}
setInterval(render,2000);
watch();

let connectionState = null;
let linkTimer = null;
let linking = false;
function showConnection(state) {
  connectionState = state;
  pages.setConnection(state);
  const profiles = state.profiles || [];
  $('saved-connections').hidden = !profiles.length;
  $('profile-select').replaceChildren(...profiles.map(profile => {
    const option = document.createElement('option'); option.value=profile.name;
    option.textContent=`${profile.name} · ${profile.url}`; option.selected=profile.name===state.name;
    return option;
  }));
  $('pairing-request').hidden=!state.pending;
  if (!connection.snapshot) { safeText($('server'),state.name || 'No controller linked'); safeText($('runtime'),state.url ? new URL(state.url).hostname : 'Local dashboard'); }
  if (state.pending) {
    safeText($('pairing-command'),`hostlier pairing approve ${state.pending.code} --socket /run/hostlier/controller.sock`);
    safeText($('pairing-expiry'),`Waiting for approval · Expires ${new Date(state.pending.expiresAt).toLocaleTimeString()}`);
  }
  if (!state.url && state.managed) $('link-panel').hidden=false;
}
async function refreshConnection() {
  try { const state=await api('/api/connection'); showConnection(state); if(state.pending && !linking){linking=true; $('link-controller').disabled=true; pollLink();} } catch(error) {safeText($('link-feedback'),error.message);}
}
$('manage-connection').addEventListener('click',()=>{ $('link-panel').hidden=!$('link-panel').hidden; if (!$('link-panel').hidden) refreshConnection(); });
$('close-link').addEventListener('click',()=>{ $('link-panel').hidden=true; });
$('select-profile').addEventListener('click',async()=>{
  if (busy || linking) return;
  busy=true; render();
  $('select-profile').disabled=true;
  try { showConnection(await api('/api/connection/select',{name:$('profile-select').value})); connection.reset();pages.reset(); render(); $('link-panel').hidden=true; }
  catch(error) {safeText($('link-feedback'),error.message);}
  finally {busy=false; render(); $('select-profile').disabled=false;}
});
$('link-form').addEventListener('submit',async event=>{
  event.preventDefault();
  if (busy || linking) return;
  linking=true; $('link-controller').disabled=true;
  try {
    const state=await api('/api/connection/link',{name:$('controller-name').value.trim(),url:$('controller-url').value.trim()});
    showConnection(state); safeText($('link-feedback'),'Pairing requested. Approve the code on the controller to finish linking.');
    pollLink();
  } catch(error) {safeText($('link-feedback'),error.message);linking=false;$('link-controller').disabled=false;}
});
async function pollLink() {
  clearTimeout(linkTimer);
  try {
    const state=await api('/api/connection/poll',{}); showConnection(state);
    if (state.pending) { linkTimer=setTimeout(pollLink,1500); return; }
    connection.reset();pages.reset();
    safeText($('link-feedback'),'Controller linked. Its profile is saved for the CLI and dashboard.');
    $('link-panel').hidden=true; linking=false; $('link-controller').disabled=false;
  } catch(error) {
    safeText($('link-feedback'),error.message); linking=false; $('link-controller').disabled=false;
  }
}
refreshConnection();
