import {text, bytes, MetricCard, EmptyState, DeploymentTable, OperationList, Button, StatusBadge} from './components.js';

import {element,routeLink,Panel,Facts,JsonDetails,CommandCard,commandLine,InventoryTable,LinkList} from './primitives.js';
import {DomainView} from './domains.js';
import {Navigation,destination,appDestinations,appPath} from './navigation.js';
import {HistoryView} from './history.js';
import {renderDeploymentDetails} from './deployment-details.js';
const time=value=>value && new Date(value).getUTCFullYear()>2000 ? new Date(value).toLocaleString() : 'Not received';
const labelOf=record=>[record.identity?.application||record.identity?.node||'Infrastructure',record.identity?.component,record.identity?.instance].filter(Boolean).join(' / ');

// History charts preserve distinct metric series, ordering and actual observation timestamps.
export function MetricSeries(records) {
  const groups=new Map();
  for(const record of records) {
    const key=JSON.stringify([record.name,record.unit,record.kind,record.temporality,record.collector,record.identity,Object.entries(record.attributes||{}).sort()]);
    const group=groups.get(key)||[];group.push(record);groups.set(key,group);
  }
  const grid=element('div','series-grid');
  for(const [key,values] of groups) {
    values.sort((a,b)=>new Date(a.observedAt)-new Date(b.observedAt));const last=values.at(-1);
    const card=element('article','series-card');card.observationKey=key;card.append(element('h3','',last.name),element('p','series-identity',labelOf(last)));
    const value=last.intValue??last.value;card.append(element('strong','series-value',`${value??'—'} ${last.unit||''}`));
    const points=values.map(record=>({t:new Date(record.observedAt).getTime(),v:Number(record.intValue??record.value)})).filter(p=>Number.isFinite(p.t)&&Number.isFinite(p.v));
    card.append(sparkline(points));
    card.append(element('p','series-caption',`${values.length} samples · ${last.kind||'metric'}${last.temporality ? last.temporality===2?' · cumulative':' · delta':''}`));
    card.append(element('p','series-caption',`Latest ${time(last.observedAt)}`));
    card.append(JsonDetails('Labels',last.attributes||{}));grid.append(card);
  }
  return grid;
}
function sparkline(points) {
  if(points.length<2)return element('p','chart-placeholder','Waiting for more samples');
  const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox','0 0 320 76');svg.classList.add('sparkline');svg.setAttribute('role','img');
  svg.setAttribute('aria-label',`${points.length} observations from ${points[0].v} to ${points.at(-1).v}`);
  const min=Math.min(...points.map(p=>p.v)),max=Math.max(...points.map(p=>p.v));const begin=points[0].t,end=points.at(-1).t;
  const line=document.createElementNS(svg.namespaceURI,'polyline');
  line.setAttribute('points',points.map(p=>`${8+(p.t-begin)/(end-begin||1)*304},${68-(p.v-min)/(max-min||1)*56}`).join(' '));
  line.setAttribute('fill','none');line.setAttribute('stroke','currentColor');line.setAttribute('stroke-width','1.6');svg.append(line);return svg;
}
function LogRecords(records) {
  const list=element('div','log-records');
  for(const record of records){
    const row=element('article','log-record');row.observationKey=String(record.id??JSON.stringify([record.observedAt,record.identity,record.body]));const head=element('div','log-meta');
    head.append(element('time','',time(record.observedAt)),element('span','',labelOf(record)));
    row.append(head,element('pre','log-body',typeof record.body==='string'?record.body:record.body?.stringValue??JSON.stringify(record.body??{})),JsonDetails('Attributes',record.attributes));list.append(row);
  }
  return list;
}

// Reuse metric/log row identities so recovery preserves expanded labels and browser scroll anchors.
function reconcileObservations(container,next){
  const previous=container.firstElementChild;
  if(!previous||previous.className!==next.className||!['series-grid','log-records'].includes(next.className)){container.replaceChildren(next);return;}
  const rows=new Map([...previous.children].map(row=>[row.observationKey,row]));const wanted=new Set();let cursor=previous.firstElementChild;
  for(const incoming of [...next.children]){
    const key=incoming.observationKey;wanted.add(key);const current=rows.get(key)||incoming;
    if(rows.has(key)&&next.className==='series-grid')updateSeries(current,incoming);
    if(current!==cursor)previous.insertBefore(current,cursor);cursor=current.nextElementSibling;
  }
  for(const row of [...previous.children])if(!wanted.has(row.observationKey))row.remove();
}
function updateSeries(current,next){
  for(const selector of ['h3','.series-identity','.series-value'])text(current.querySelector(selector),next.querySelector(selector).textContent);
  const captions=current.querySelectorAll('.series-caption');next.querySelectorAll('.series-caption').forEach((node,index)=>text(captions[index],node.textContent));
  const chart=current.querySelector('.sparkline,.chart-placeholder'),replacement=next.querySelector('.sparkline,.chart-placeholder');
  if(chart?.tagName!==replacement?.tagName){chart?.replaceWith(replacement);return;}
  if(chart?.classList.contains('sparkline')){chart.setAttribute('aria-label',replacement.getAttribute('aria-label'));chart.querySelector('polyline').setAttribute('points',replacement.querySelector('polyline').getAttribute('points'));}
}

export class Pages {
  constructor(root,{api,propose}) {
    this.navigation=Navigation(document.querySelector('.navigation'));this.root=root;this.api=api;this.propose=propose;this.state={};this.sequence=0;this.requesting=false;this.snapshot=null;
    document.addEventListener('click',event=>{
      const link=event.target.closest('a[data-route]');if(!link||event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;
      event.preventDefault();const target=new URL(link.href,location);if(target.href===location.href){this.navigation.select(this.page,this.appName,this.appView);return;}history.pushState(null,'',link.href);this.navigate();
    });
    addEventListener('popstate',()=>this.navigate());
    // Accept links from the previous anchor-based dashboard without losing local authorization.
    if(['/overview','/deployments','/operations','/observability'].includes(location.hash.replace('#','/')))history.replaceState(null,'',location.hash.replace('#','/'));
    this.navigate();
    setInterval(()=>{if(!document.hidden&&!this.paused)this.refresh();},5000);
    document.addEventListener('visibilitychange',()=>{if(!document.hidden&&!this.paused)this.refresh();});
  }
  command(args,host) {return commandLine(args,this.connection||{},host);}
  setConnection(state) {this.connection=state;for(const code of this.root.querySelectorAll(".cli-command"))text(code,this.command(JSON.parse(code.dataset.cliArgs),code.dataset.cliHost));}
  domainContext() {const sequence=this.sequence;return {api:this.api,snapshot:()=>this.snapshot,command:(args,host)=>this.command(args,host),current:()=>this.sequence===sequence};}
  reset() {this.sequence++;this.requesting=false;this.data=null;this.snapshot=null;this.navigate();}
  heading(title,description,parent) {
    document.title=`Hostlier · ${title}`;text(document.getElementById('page-title'),title);text(document.getElementById('page-description'),description);
    const crumbs=document.getElementById('breadcrumbs');crumbs.replaceChildren();crumbs.hidden=!parent;
    if(parent)crumbs.append(routeLink(`← ${parent.label}`,parent.path));
  }
  navigate() {
    this.sequence++;this.requesting=false;this.paused=false;this.domain=null;this.data=null;this.detailSignature=null;this.diagnosticError=null;this.component=null;this.actionButtons=null;this.observedFacts=null;this.readFailures={};this.diagnosticLoaded=false;this.workflowBody=null;this.appName=null;this.appView=null;this.appOperations=null;this.appGridSignature=null;this.tunnels=[];this.historyView=null;this.runtimePanel=null;this.runtimeBusy=false;this.runtimeVersion=0;this.runtimeMetadata=false;this.root.replaceChildren();this.error=element('p','notice');this.error.hidden=true;this.root.append(this.error);
    const parts=location.pathname.split('/').filter(Boolean);this.page=parts[0]||'overview';
    try{this.identity=parts[1]?decodeURIComponent(parts[1]):null;}catch{this.identity=null;this.page='missing';}
    const query=new URLSearchParams(location.search);
    if(this.page==='deployments'&&this.identity){
      this.appName=this.identity;this.appView=query.get('view')||'overview';
      if(!appDestinations.some(item=>item.key===this.appView))this.appView='overview';
    }
    if(this.identity&&query.get('app')&&['operations','resources','recovery'].includes(this.page)){this.appName=query.get('app');this.appView=this.page==='operations'?'activity':'resources';}
    this.navigation.select(this.page,this.appName,this.appView);
    const info=destination(this.page);const title=info?.label||'Page not found';
    const appInfo=appDestinations.find(item=>item.key===this.appView);
    text(document.getElementById('page-section'),this.appName?'Application / '+appInfo.label:info?.group||'Hostlier');
    this.heading(this.identity||this.appName||title,appInfo?.description||info?.description||'Use the navigation to return to your dashboard.',this.appName?(this.page==='deployments'?{label:'All applications',path:'/overview'}:{label:this.appName+' / '+appInfo.label,path:appPath(this.appName,this.appView)}):this.identity?{label:this.page==='tunnels'?'Apps & tunnels':title,path:this.page==='tunnels'?'/overview':'/'+this.page}:info?.group==='Controller settings'?{label:'Settings',path:'/settings'}:null);
    if(this.page==='deployments'&&['logs','activity'].includes(this.appView))this.history(this.appView==='logs'?'logs':'activity',this.appName);
    else if(this.appName&&this.appView==='metrics')this.observability();
    else if(this.page==='overview'||this.page==='deployments'&&!this.identity)this.overview();
    else if(this.page==='operations'&&!this.identity)this.history('activity',new URLSearchParams(location.search).get('app'));
    else if(this.page==='logs')this.history('logs');
    else if(this.page==='observability')this.observability();
    else if(this.identity&&['deployments','operations','tunnels'].includes(this.page))this.detail();
    else if(['resources','hosts','recovery','access','settings'].includes(this.page)){const body=element('div');this.root.append(body);this.domain=new DomainView(body,this.page,this.identity,this.domainContext());}
    else this.root.append(EmptyState('Page not found','Use the navigation to open your dashboard.'));
    this.update(this.snapshot,this.state);this.refresh();document.querySelector('main').focus({preventScroll:true});window.scrollTo(0,0);
  }
  overview() {
    this.appSummary=element('p','application-summary','Loading applications…');
    this.appGrid=element('div','application-grid');this.tunnelNotice=element('p','inventory-freshness');this.tunnelNotice.hidden=true;this.tunnelNotice.setAttribute('role','status');this.root.append(this.appSummary,this.tunnelNotice,this.appGrid);
  }
  renderApplications(apps) {
    const entries=[...apps.map(app=>({...app,kind:'app'})),...(this.tunnels||[]).map(tunnel=>({...tunnel,kind:'tunnel'}))];
    const observations=new Map((this.snapshot?.applications||[]).map(app=>[app.name,app]));
    const signature=JSON.stringify([entries, [...observations.values()].map(({name,available,running})=>({name,available,running})),this.state.stale]);if(signature===this.appGridSignature)return;this.appGridSignature=signature;
    const existing=new Map([...this.appGrid.querySelectorAll('.application-card')].map(card=>[card.dataset.key,card]));
    for(const node of [...this.appGrid.children])if(!node.classList.contains('application-card'))node.remove();
    text(this.appSummary,`${apps.length} app${apps.length===1?'':'s'} · ${this.tunnels.length} tunnel${this.tunnels.length===1?'':'s'}${this.state.stale?' · Showing last received status':''}`);
    if(!entries.length){this.appGrid.replaceChildren();this.appGrid.append(EmptyState('No apps or tunnels yet','Deploy an app or connect a tunnel from the CLI to get started.'));return;}
    let cursor=this.appGrid.firstElementChild;const retained=new Set();
    for(const app of entries){
      const tunnel=app.kind==='tunnel', key=app.kind+':'+app.name;
      const observed=observations.get(app.name);const card=existing.get(key)||routeLink('',tunnel?'/tunnels/'+encodeURIComponent(app.name):appPath(app.name),'application-card');card.dataset.name=app.name;card.dataset.key=key;retained.add(key);card.replaceChildren();
      const top=element('div','application-card-top');const badge=StatusBadge();badge.update(tunnel?app.status:observed?.available?(observed.running?'running':'stopped'):'unavailable');
      top.append(element('span','application-symbol',app.name.slice(0,1).toUpperCase()),badge.node);
      const name=element('div','application-name-row');name.append(element('h2','app-name',app.name),element('span','workload-type',tunnel?'Tunnel':'App'));
      card.append(top,name,element('p','application-address',app.url||app.spec?.route||'Private application'));
      const bottom=element('div','application-card-bottom');bottom.append(element('span','',tunnel?'Bridge · '+app.bridgeId:app.spec?.image?.split('/').at(-1)||'Application'),element('span','',tunnel?'View tunnel →':'Open dashboard →'));card.append(bottom);if(card!==cursor)this.appGrid.insertBefore(card,cursor);cursor=card.nextElementSibling;
    }
    for(const [name,card] of existing)if(!retained.has(name))card.remove();
  }
  history(kind,app=null) {
    const sequence=this.sequence;const body=element('div');this.root.append(body);
    this.historyView=new HistoryView(body,{api:this.api,current:()=>sequence===this.sequence,kind,app});
    if(kind==='logs'&&app){
      this.runtimePanel=element('details','json-details');this.runtimePanel.append(element('summary','','Current runtime output'));
      this.runtimePanel.append(element('p','muted','Read directly from the running workload. Availability depends on the pod or container still being present.'));
      this.component=element('select','filter-input');this.component.setAttribute('aria-label','Log component');const main=element('option','','main');main.value='';this.component.append(main);
      this.diagnosticError=element('p','notice');this.diagnosticError.hidden=true;this.diagnosticBody=element('pre','code-block log-tail','Open to read runtime output.');
      this.component.addEventListener('change',()=>{this.runtimeVersion++;this.runtimeBusy=false;this.diagnosticError.hidden=true;text(this.diagnosticBody,'Loading runtime output…');this.refreshRuntime();});
      this.runtimePanel.append(this.component,this.diagnosticError,this.diagnosticBody);this.root.append(this.runtimePanel);
      this.runtimePanel.addEventListener('toggle',()=>{if(this.runtimePanel.open)this.refreshRuntime();});
    }
  }
  async refreshRuntime(){
    if(this.runtimeBusy||!this.appName)return;this.runtimeBusy=true;const sequence=this.sequence,version=this.runtimeVersion;
    const endpoint='/api/deployments/'+encodeURIComponent(this.appName);
    try{
      if(!this.runtimeMetadata){const data=await this.api(endpoint);if(sequence!==this.sequence||version!==this.runtimeVersion)return;this.runtimeMetadata=true;for(const name of Object.keys(data.spec?.sidecars||{})){const option=element('option','',name);option.value=name;this.component.append(option);}}
      const tail=await this.api(endpoint+'/logs'+(this.component.value?'?component='+encodeURIComponent(this.component.value):''));
      if(sequence!==this.sequence||version!==this.runtimeVersion)return;this.diagnosticError.hidden=true;text(this.diagnosticBody,tail.text||'No log output.');
    }catch(error){if(sequence===this.sequence&&version===this.runtimeVersion){text(this.diagnosticError,error.message);this.diagnosticError.hidden=false;}}
    finally{if(sequence===this.sequence&&version===this.runtimeVersion)this.runtimeBusy=false;}
  }
  operations() {const list=element('div','operation-list');this.root.append(list);this.operationsList=OperationList(list);}
  detail() {
    this.detailBody=element('div');this.root.append(this.detailBody);this.detailBody.append(EmptyState('Loading details','Reading the controller’s latest retained state.'));
  }
  update(snapshot,state) {
    this.snapshot=snapshot;this.state=state;
    const apps=snapshot?.applications||[];
    if(this.page==='overview'||this.page==='deployments'&&!this.identity)this.renderApplications(this.data?.applications||apps);
    if(this.page==='operations'&&!this.identity&&!this.historyView)this.operationsList((this.data?.operations||snapshot?.operations||[]).filter(op=>!new URLSearchParams(location.search).get('app')||op.name===new URLSearchParams(location.search).get('app')));
    if(this.page==='deployments'&&this.identity&&this.data){
      const observed=apps.find(a=>a.name===this.identity);
      if(this.observedFacts){const open=this.observedFacts.querySelector('details')?.open;this.observedFacts.replaceChildren(this.healthFacts(observed));if(open)this.observedFacts.querySelector('details').open=true;}
      this.actionButtons?.forEach(button=>button.disabled=!state.live||state.busy||this.data.status==='deleted');
    }
    if(this.page==='observability'||this.appView==='metrics')this.updateTelemetryHealth();
  }
  healthFacts(observed) {
    const available=observed?.available;
    const root=element('div');root.append(Facts([['Status',available?(observed.running?'Running':'Stopped'):'Unavailable'],['Ready replicas',available?`${observed.readyReplicas??'—'} / ${observed.desiredReplicas}`:'—'],['CPU',observed?.usage?.cpuCores!=null?`${observed.usage.cpuCores.toFixed(3)} cores`:'Unavailable'],['Memory',observed?.usage?.memoryBytes!=null?bytes(observed.usage.memoryBytes):'Unavailable']]));
    const details=element('details','json-details');details.append(element('summary','','Observation details'),Facts([['Restarts',available?observed.restartCount:'—'],['Out of memory',observed?.oomKilled==null?'Unknown':String(observed.oomKilled)],['Observed',time(observed?.observedAt)],['Measurements',observed?.usage?.reason||(!observed?.usage?'Waiting for measurements':observed.usage.available?'Available':'Unavailable')],['Reason',observed?.reason||'—']]));root.append(details);return root;
  }
  async refresh() {
    if(this.requesting||!this.api||this.page==='missing')return;
    const sequence=this.sequence;this.requesting=true;let diagnostic=false;
    try {
      if(this.historyView){await this.historyView.refresh();if(this.runtimePanel?.open)await this.refreshRuntime();return;}
      if(this.page==='overview'||this.page==='deployments'&&!this.identity){await this.refreshOverview(sequence);return;}
      if(this.domain){await this.domain.refresh();return;}
      if(this.page==='observability'||this.appView==='metrics'){await this.refreshTelemetry(sequence);return;}
      const endpoint=`/api/${this.page==='overview'?'deployments':this.page}${this.identity?'/'+encodeURIComponent(this.identity):''}`;
      const result=await this.api(endpoint);
      if(sequence!==this.sequence)return;
      if(this.page==='operations'&&this.appName&&result.name!==this.appName)throw new Error('This operation does not belong to the selected app.');
      this.data=result;this.readFailures.metadata=null;this.error.hidden=true;
      if(this.identity){
        // Rebuild metadata only when it changes, preserving expanded sections and keyboard focus on steady captures.
        const signature=JSON.stringify(result);if(this.detailSignature!==signature){this.detailSignature=signature;this.renderDetail(result);this.update(this.snapshot,this.state);}
        if(this.page==='deployments'&&this.workflowBody){
          try{const workflows=await this.api(endpoint+'/workflows');if(sequence!==this.sequence)return;this.workflowBody.replaceChildren(InventoryTable([['Step',row=>row.step],['Status',row=>row.status]],Array.isArray(workflows)?workflows:workflows.steps||[],{emptyTitle:'No retained workflow steps',emptyDescription:'Package provisioning steps appear here when this deployment uses them.'}));}
          catch(error){if(sequence===this.sequence)this.workflowBody.replaceChildren(element('p','muted',error.message));}
        }
        if(this.page==='deployments'&&this.appView==='activity'){
          try{const activity=await this.api('/api/operations');if(sequence!==this.sequence)return;
          this.appOperations((activity.operations||[]).filter(operation=>operation.name===this.appName));
          for(const link of this.detailBody.querySelectorAll('a[href^="/operations/"]'))link.href=link.pathname+'?app='+encodeURIComponent(this.appName);
          this.activityError.hidden=true;}catch(error){if(sequence!==this.sequence)return;text(this.activityError,error.message);this.activityError.hidden=false;}
        }
        if(this.page==='tunnels'){this.update(this.snapshot,this.state);return;}
        if(this.page==='deployments'&&this.appView!=='logs'){this.update(this.snapshot,this.state);return;}
        diagnostic=true;
        const tail=await this.api(endpoint+(this.page==='deployments'?'/logs'+(this.component?.value?'?component='+encodeURIComponent(this.component.value):''):'/events'));
        if(sequence!==this.sequence)return;
        this.diagnosticLoaded=true;this.readFailures.diagnostic=null;this.diagnosticError.hidden=true;text(this.diagnosticBody,this.page==='deployments'?tail.text||'No log output.':(tail.entries||[]).map(e=>`${e.timestamp||''} ${e.level||''} ${e.message||''}`.trim()).join('\n')||'No retained execution events.');
      }
      this.update(this.snapshot,this.state);
    }catch(error){if(sequence===this.sequence){
      const node=diagnostic&&this.diagnosticError?this.diagnosticError:this.error;
      const cached=diagnostic?this.diagnosticLoaded:!!this.data;const kind=diagnostic?'diagnostic':'metadata';
      this.readFailures[kind]??=Date.now();node.className=cached?'read-warning':'notice';
      text(node,cached?'Refresh delayed · Keeping last received data':error.message);node.title=error.message;
      node.hidden=cached&&Date.now()-this.readFailures[kind]<10000;
    }}
    finally{if(sequence===this.sequence)this.requesting=false;}
  }
  async refreshOverview(sequence) {
    // The two inventories can fail independently; retain successful data from either.
    const [apps,tunnels]=await Promise.allSettled([this.api('/api/deployments'),this.api('/api/tunnels')]);
    if(sequence!==this.sequence)return;
    if(apps.status==='fulfilled'){this.data=apps.value;this.error.hidden=true;}
    else {text(this.error,this.data?'App inventory refresh delayed · Showing last received data':apps.reason.message);this.error.hidden=false;}
    if(tunnels.status==='fulfilled'){
      if(!Array.isArray(tunnels.value))throw new Error('Unexpected tunnel inventory response.');
      this.tunnels=tunnels.value;this.tunnelNotice.hidden=true;
    }else{
      const unconfigured=tunnels.reason.code==='INVALID_CONFIG'&&tunnels.reason.message==='Tunnel gateway is not configured';
      text(this.tunnelNotice,unconfigured?'Tunnels are not configured on this controller.':this.tunnels.length?'Tunnel status refresh delayed · Showing last received data':'Tunnel inventory unavailable');
      this.tunnelNotice.hidden=false;
    }
    this.update(this.snapshot,this.state);
  }
  renderTunnel(data) {
    this.heading(data.name,'Tunnel connection status.',{label:'Apps & tunnels',path:'/overview'});
    const panel=Panel('Tunnel');const badge=StatusBadge();badge.update(data.status);panel.content.append(badge.node);
    panel.content.append(Facts([['Public URL',data.url],['Bridge',data.bridgeId],['Connection limit',data.connections],['Expires',time(data.expiresAt)]]));
    const details=element('details','json-details');details.append(element('summary','','Connection details'),Facts([['Connector URL',data.connectUrl]]));panel.content.append(details);this.detailBody.append(panel.node);
  }
  renderDetail(data) {
    const component=this.component?.value||'';
    const expanded=new Set([...this.detailBody.querySelectorAll('details[open]')].map(node=>node.querySelector('summary')?.textContent));
    this.detailBody.replaceChildren();this.detailSignature=JSON.stringify(data);
    if(this.page==='tunnels'){this.renderTunnel(data);return;}
    if(this.page==='deployments')this.renderDeployment(data);else this.renderOperation(data);
    if(this.page==='deployments'&&this.appView!=='logs'){for(const details of this.detailBody.querySelectorAll('details'))if(expanded.has(details.querySelector('summary')?.textContent))details.open=true;return;}
    const diagnostics=Panel(this.page==='deployments'?'Application logs':'Execution events','Refreshes every 5 seconds while this page is visible.');
    this.diagnosticLoaded=false;this.diagnosticError=element('p','notice');this.diagnosticError.hidden=true;this.diagnosticBody=element('pre','code-block log-tail','Loading…');
    if(this.page==='deployments'){
      this.component=element('select','filter-input');this.component.setAttribute('aria-label','Log component');
      for(const name of ['main',...Object.keys(data.spec?.sidecars||{})]){const option=element('option','',name);option.value=name==='main'?'':name;option.selected=option.value===component;this.component.append(option);}
      this.component.addEventListener('change',()=>{this.sequence++;this.requesting=false;this.refresh();});diagnostics.content.append(this.component);
    }
    diagnostics.content.append(this.diagnosticError,this.diagnosticBody);this.detailBody.append(diagnostics.node);
    for(const details of this.detailBody.querySelectorAll('details'))if(expanded.has(details.querySelector('summary')?.textContent))details.open=true;
  }
  renderDeployment(data) {renderDeploymentDetails(this,data);}
  renderOperation(data) {
    const summary=Panel('Operation','The controller owns execution, including when this dashboard is closed.');
    const badge=StatusBadge();badge.update(data.status,['failed','interrupted'].includes(data.status)?'attention':'default');summary.content.append(badge.node);
    summary.content.append(Facts([['Operation ID',data.id],['Kind',data.kind],['Action',data.action||'—'],['Status',data.status],[data.kind?.startsWith('resource')?'Resource':'Application',data.name],['Requested by',data.actor],['Plan ID',data.planId],['Superseded by',data.supersededBy||'—']]));
    if(data.name){const resource=data.kind?.startsWith('resource');summary.content.append(routeLink(resource?'Open resource →':'Open deployment →',(resource?'/resources/':'/deployments/')+encodeURIComponent(data.name)));}
    if(data.supersededBy)summary.content.append(routeLink('Open replacement operation →','/operations/'+encodeURIComponent(data.supersededBy)));
    this.detailBody.append(summary.node);
    if(data.error){const error=Panel('Failure details','Reported by the controller.');error.content.append(element('pre','code-block',`${data.error.code||'Failure'}: ${data.error.message||'No further detail reported'}`));this.detailBody.append(error.node);}
    const request=Panel('Request details','Retained inputs and reviewed workflow metadata.');request.content.append(InventoryTable([['Step',row=>row.id||row.key],['Summary',row=>(row.summary||[]).join(' · ')],['Reused',row=>row.reuse?'Yes':'No']],data.workflow||[],{emptyTitle:'No package workflow steps'}),JsonDetails('Advanced operation receipt',data));
    request.content.append(CommandCard('Follow this operation',['operation',data.id,'--watch'],args=>this.command(args)));this.detailBody.append(request.node);
  }
  observability() {
    this.telemetryLoaded=[false,false];this.telemetrySignatures=[];this.telemetryScope=null;this.paused=false;this.before=0;this.appliedFilters=new URLSearchParams(location.search);this.appliedFilters.delete('view');if(this.appName)this.appliedFilters.set('app',this.appName);
    const filters=element('form','observation-filters');this.filters={};const initial=new URLSearchParams(this.appliedFilters);
    for(const [key,label,placeholder]of [['app','Application','All applications'],['component','Component','All components'],['revision','Revision','All revisions']]){
      if(this.appName&&key==='app')continue;
      const field=element('label','field-label',label);const input=element('input','filter-input');input.value=initial.get(key)||'';input.placeholder=placeholder;input.maxLength=200;input.name=key;this.filters[key]=input;field.append(input);filters.append(field);
    }
    filters.append(Button({label:'Apply filters',onClick:()=>{}}));filters.lastChild.type='submit';
    filters.addEventListener('submit',event=>{event.preventDefault();const q=new URLSearchParams();if(this.appName)q.set('app',this.appName);for(const[key,input]of Object.entries(this.filters))if(input.value.trim())q.set(key,input.value.trim());const address=new URLSearchParams(q);if(this.appName){address.delete('app');address.set('view','metrics');}history.replaceState(null,'',(this.appName?appPath(this.appName):'/observability')+(address.size?'?'+address:''));this.appliedFilters=q;this.sequence++;this.requesting=false;this.before=0;this.refresh();});
    this.pause=Button({label:'Pause updates',onClick:()=>{this.paused=!this.paused;text(this.pause,this.paused?'Resume live updates':'Pause updates');if(!this.paused){this.before=0;this.sequence++;this.requesting=false;this.refresh();}}});filters.append(this.pause);this.root.append(filters);
    const health=Panel('Agent delivery','Delivery freshness is separate from workload readiness.');this.telemetryFacts=health.content;if(!this.appName)this.root.append(health.node);
    this.historyStatus=element('p','history-status','Loading observations…');this.root.append(this.historyStatus);
    const metricPanel=Panel('Metrics','Latest 100 matching measurements. Charts use the actual agent sample timestamps.');this.metricBody=metricPanel.content;this.root.append(metricPanel.node);
    const logs=Panel('Logs','Latest 100 matching records, with workload identity verified by the controller.');this.logsBody=element('div');logs.content.append(this.logsBody);
    this.older=Button({label:'Older logs',onClick:()=>{if(this.nextBefore){this.before=this.nextBefore;this.paused=true;text(this.pause,'Resume live updates');this.refresh();}}});this.older.hidden=true;logs.content.append(this.older);if(!this.appName)this.root.append(logs.node);
  }
  updateTelemetryHealth() {
    const telemetry=this.snapshot?.telemetry;
    this.telemetryFacts.replaceChildren(Facts([['Collectors configured',telemetry?.collectors??0],['Delivery',this.state.stale?'Stale · last known delivery':telemetry?.fresh?'Receiving':telemetry?'No recent delivery':'No telemetry received'],['Managed collector',telemetry?.managed?.state||'Not managed'],['Collector detail',telemetry?.managed?.reason||'—'],['Last batch',time(telemetry?.lastReceivedAt)],['Accepted samples',telemetry?.accepted??0],['Rejected samples',telemetry?.rejected??0]]));
  }
  async refreshTelemetry(sequence) {
    const query=new URLSearchParams(this.appliedFilters);query.set('limit','100');
    const logs=new URLSearchParams(query);if(this.before)logs.set('before',this.before);
    const scope=this.appliedFilters.toString();if(scope!==this.telemetryScope){this.telemetryScope=scope;this.telemetryLoaded=[false,false];this.telemetrySignatures=[];this.metricBody.replaceChildren();this.logsBody.replaceChildren();}
    const requests=[this.api('/api/observability?'+query+'&signal=metrics')];if(!this.appName)requests.push(this.api('/api/observability?'+logs+'&signal=logs'));const results=await Promise.allSettled(requests);
    if(sequence!==this.sequence)return;
    const containers=[this.metricBody,this.logsBody];const retrying=[];
    results.forEach((result,index)=>{
      if(result.status==='rejected'){retrying.push(index?'logs':'metrics');if(!this.telemetryLoaded[index]){if(index)this.older.hidden=true;containers[index].replaceChildren(EmptyState(index?'Logs unavailable':'Metrics unavailable',result.reason.message,'activity'));}return;}
      const records=result.value.records||[];
      const signature=JSON.stringify(records);if(signature!==this.telemetrySignatures[index]){this.telemetrySignatures[index]=signature;reconcileObservations(containers[index],records.length?index?LogRecords(records):MetricSeries(records):EmptyState(index?'No matching log records':'No matching metric samples','Check agent delivery and your application, component, and revision filters.','activity'));}this.telemetryLoaded[index]=true;
      if(index){this.nextBefore=result.value.nextBefore;this.older.hidden=!this.nextBefore;}
    });
    text(this.historyStatus,retrying.length?`Retrying ${retrying.join(' and ')} · Showing last received data`:`${this.paused?'Paused history':'Live · Refreshes every 5 seconds'} · Last checked ${new Date().toLocaleTimeString()}`);
    this.error.hidden=true;
  }
}

export {element,routeLink,Panel,Facts,JsonDetails} from "./primitives.js";
