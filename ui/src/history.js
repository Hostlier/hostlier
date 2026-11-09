import {element,routeLink} from './primitives.js';
import {text,Button,EmptyState} from './components.js';

const hour=3600000, retention=30*24*hour;
const localInput=value=>{const d=new Date(value);return new Date(value-d.getTimezoneOffset()*60000).toISOString().slice(0,16);};
const bodyText=record=>{const body=record.body;return typeof body==='string'?body:body?.stringValue??(body==null?'':JSON.stringify(body));};
const severity=record=>record.level||record.attributes?.['otel.severity_text']?.stringValue||'info';

// The same time-window viewer serves controller and app Logs and Activity.
// API filtering is authoritative; filters are captured before asynchronous reads.
export class HistoryView {
 constructor(root,{api,current,kind='activity',app=null,now=()=>Date.now()}){
  Object.assign(this,{root,api,current,kind,app,now});this.version=0;this.busy=false;this.records=[];this.next=null;this.relative=2*hour;this.filter={search:'',level:'',component:''};
  const q=new URLSearchParams(location.search);this.filter.search=q.get('q')||'';this.filter.level=q.get('level')||'';this.filter.component=q.get('component')||'';
  if(q.has('window')){const hours=Number(q.get('window'));if(hours>0&&hours<=720)this.relative=hours*hour;}
  const from=Date.parse(q.get('from')),to=Date.parse(q.get('to'));if(Number.isFinite(from)&&Number.isFinite(to)&&from<to&&to-from<=retention){this.relative=0;this.from=from;this.to=to;}else this.setWindow();
  this.render();
 }
 setWindow(){if(this.relative){this.to=this.now();this.from=this.to-this.relative;}}
 render(){
  const toolbar=element('form','history-toolbar');this.root.append(toolbar);
  const searchLabel=element('label','field-label','Search');this.search=element('input','filter-input');this.search.placeholder=this.kind==='activity'?'Search activity…':'Search log messages…';this.search.maxLength=256;this.search.value=this.filter.search;searchLabel.append(this.search);toolbar.append(searchLabel);
  const rangeLabel=element('label','field-label','Time range');this.range=element('select','filter-input');
  for(const [value,label]of [['1','Past hour'],['2','Past 2 hours'],['6','Past 6 hours'],['24','Past 24 hours'],['168','Past 7 days'],['720','Past 30 days'],['custom','Custom range']]){const option=element('option','',label);option.value=value;option.selected=value===(this.relative?String(this.relative/hour):'custom');this.range.append(option);}rangeLabel.append(this.range);toolbar.append(rangeLabel);
  const levelLabel=element('label','field-label','Level');this.level=element('select','filter-input');for(const value of ['','debug','info','warn','error','fatal']){const option=element('option','',value||'All levels');option.value=value;option.selected=value===this.filter.level;this.level.append(option);}levelLabel.append(this.level);toolbar.append(levelLabel);
  if(this.kind==='logs'&&this.app){const label=element('label','field-label','Component');this.component=element('input','filter-input');this.component.placeholder='All components';this.component.value=this.filter.component;label.append(this.component);toolbar.append(label);}
  const apply=Button({label:'Search'});apply.type='submit';toolbar.append(apply);
  this.custom=element('div','history-custom');this.custom.hidden=!!this.relative;
  for(const [key,label]of [['from','From'],['to','To']]){const field=element('label','field-label',label);const input=element('input','filter-input');input.type='datetime-local';input.value=localInput(this[key]);input.required=true;this[key+'Input']=input;field.append(input);this.custom.append(field);}toolbar.append(this.custom);
  this.range.addEventListener('change',()=>{this.editingRange=true;this.custom.hidden=this.range.value!=='custom';if(!this.custom.hidden){this.fromInput.value=localInput(this.from);this.toInput.value=localInput(this.to);}});
  toolbar.addEventListener('submit',event=>{event.preventDefault();const relative=this.range.value==='custom'?0:Number(this.range.value)*hour;const current=this.now(),from=relative?current-relative:this.fromInput.value===localInput(this.from)?this.from:Date.parse(this.fromInput.value),to=relative?current:this.toInput.value===localInput(this.to)?this.to:Date.parse(this.toInput.value);if(!Number.isFinite(from)||!Number.isFinite(to)||from>=to||to-from>retention){this.fail('Choose a start before the end, within a 30-day range.');return;}this.relative=relative;this.from=from;this.to=to;this.filter={search:this.search.value.trim(),level:this.level.value,component:this.component?.value.trim()||''};this.change();});
  const controls=element('div','history-controls');this.olderWindow=Button({label:'← Older hour',onClick:()=>this.shift(-hour)});this.newerWindow=Button({label:'Newer hour →',onClick:()=>this.shift(hour)});this.expand=Button({label:'Expand range',onClick:()=>{this.relative=0;this.from=Math.max(this.to-retention,this.from-(this.to-this.from));this.change();}});this.live=Button({label:'Back to live',onClick:()=>{this.relative=2*hour;this.setWindow();this.change();}});controls.append(this.olderWindow,this.newerWindow,this.expand,this.live);this.root.append(controls);
  this.caption=element('p','history-status','Loading entries…');this.caption.setAttribute('role','status');this.root.append(this.caption);
  this.error=element('p','notice');this.error.hidden=true;this.error.setAttribute('role','alert');this.root.append(this.error);
  this.list=element('div','history-entries');this.list.tabIndex=0;this.list.setAttribute('role','region');this.list.setAttribute('aria-label',this.kind==='activity'?'Activity entries':'Log entries');this.root.append(this.list);
  this.more=Button({label:'Load more in this range',onClick:()=>{if(!this.next||this.busy)return;this.relative=0;this.editingRange=false;this.syncControls();this.writeURL();this.refresh(true);}});this.more.hidden=true;this.root.append(this.more);
  this.retentionNote=element('p','history-retention','History is kept for up to 30 days, subject to storage limits.');this.root.append(this.retentionNote);
  this.syncControls();
 }
 syncControls(){
  const value=this.relative?String(this.relative/hour):'custom';for(const option of this.range.children)option.selected=option.value===value;
  this.custom.hidden=!!this.relative;this.fromInput.value=localInput(this.from);this.toInput.value=localInput(this.to);
  this.newerWindow.disabled=this.to>=this.now();this.expand.disabled=this.to-this.from>=retention;this.live.hidden=!!this.relative;
 }
 shift(delta){const advance=delta>0?Math.min(delta,this.now()-this.to):delta;this.relative=0;this.from+=advance;this.to+=advance;this.change();}
 change(){
  this.version++;this.editingRange=false;this.busy=false;this.records=[];this.next=null;this.list.replaceChildren();this.list.scrollTop=0;this.more.hidden=true;this.error.hidden=true;text(this.caption,'Loading entries…');this.syncControls();
  this.writeURL();this.refresh();
 }
 writeURL(){
  const q=new URLSearchParams(location.search);for(const key of ['from','to','q','level','component','window'])q.delete(key);
  if(this.relative)q.set('window',String(this.relative/hour));
  if(!this.relative){q.set('from',new Date(this.from).toISOString());q.set('to',new Date(this.to).toISOString());}
  if(this.filter.search)q.set('q',this.filter.search);if(this.filter.level)q.set('level',this.filter.level);if(this.filter.component)q.set('component',this.filter.component);
  history.replaceState(null,'',location.pathname+(q.size?'?'+q:''));
 }
 fail(message){this.error.hidden=false;text(this.error,message);}
 async refresh(more=false){
  if(this.busy||(!more&&!this.relative&&this.records.length))return;
  this.setWindow();const version=this.version;this.busy=true;this.more.disabled=true;const q=new URLSearchParams({from:new Date(this.from).toISOString(),to:new Date(this.to).toISOString(),limit:'100'});
  if(this.app)q.set('app',this.app);if(this.filter.search)q.set('q',this.filter.search);if(this.filter.level)q.set('level',this.filter.level);if(this.filter.component)q.set('component',this.filter.component);
  if(more&&this.next){q.set('before',String(this.next.id));q.set('beforeTime',this.next.time);}
  if(this.kind==='logs')q.set('signal','logs');
  try{
   const result=await this.api((this.kind==='activity'?'/api/activity':'/api/observability')+'?'+q);
   if(!this.current()||version!==this.version)return;
   const incoming=this.kind==='activity'?result.entries||[]:result.records||[];
   const seen=new Set(more?this.records.map(row=>row.sequence??row.id):[]);const rows=incoming.filter(row=>!seen.has(row.sequence??row.id));this.records=more?[...this.records,...rows]:incoming;
   this.next=result.nextBefore?{id:result.nextBefore,time:result.nextBeforeTime}:null;this.error.hidden=true;this.renderEntries();
   this.more.hidden=!this.next||this.records.length>=5000;
   const timezone=Intl.DateTimeFormat().resolvedOptions().timeZone;
   text(this.caption,`${new Date(this.from).toLocaleString()} – ${new Date(this.to).toLocaleString()} · ${timezone} · ${this.records.length} entries${this.relative?' · Live':''}`);
   text(this.retentionNote,`Up to ${result.retentionDays||30} days retained, subject to storage limits.${result.retainedFrom?' Oldest stored entry: '+new Date(result.retainedFrom).toLocaleString()+'.':''}${this.records.length>=5000?' Narrow your search to see more results.':''}`);
   if(!this.editingRange)this.syncControls();
  }catch(error){if(this.current()&&version===this.version)this.fail((this.records.length?'Refresh delayed. Showing the last received entries. ':'')+error.message);}
  finally{if(this.current()&&version===this.version){this.busy=false;this.more.disabled=false;}}
 }
 renderEntries(){
  const existing=new Map([...this.list.querySelectorAll('.history-entry')].map(node=>[node.dataset.id,node]));
  for(const node of [...this.list.children])if(!node.classList.contains('history-entry'))node.remove();
  let cursor=this.list.firstElementChild;const retained=new Set();
  if(!this.records.length){this.list.replaceChildren();this.list.append(EmptyState('No entries in this range',this.kind==='logs'?'Try an earlier hour or a wider range. Application history appears after the workload collector delivers logs.':'Try an earlier hour, a wider range, or a different search.'));return;}
  for(const record of this.records){
   const id=String(record.sequence??record.id),signature=JSON.stringify(record),previous=existing.get(id);retained.add(id);
   if(previous?.recordSignature===signature){if(previous!==cursor)this.list.insertBefore(previous,cursor);cursor=previous.nextElementSibling;continue;}
   const row=element('details','history-entry');row.dataset.id=id;row.recordSignature=signature;row.open=previous?.open||false;
   const summary=element('summary');const stamp=record.timestamp||record.observedAt;const message=this.kind==='activity'?record.message:bodyText(record);
   summary.append(element('time','history-time',new Date(stamp).toLocaleString()),element('span','history-level',severity(record)),element('span','history-message',message));row.append(summary);
   const detail=element('div','history-entry-detail');if(!this.app)detail.append(element('p','muted',record.application||record.identity?.application||'Controller'));
   detail.append(element('pre','code-block',message));if(record.actor)detail.append(element('p','muted','Requested by '+record.actor));
   if(record.operationId)detail.append(routeLink('View operation →','/operations/'+encodeURIComponent(record.operationId)+(this.app?'?app='+encodeURIComponent(this.app):'')));
   if(record.attributes&&Object.keys(record.attributes).length)detail.append(element('pre','code-block',JSON.stringify(record.attributes,null,2)));
   row.append(detail);if(previous){if(cursor===previous)cursor=previous.nextElementSibling;previous.remove();}this.list.insertBefore(row,cursor);
  }
  for(const [id,node]of existing)if(!retained.has(id))node.remove();
 }
}
