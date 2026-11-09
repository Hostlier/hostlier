import {EmptyState,StatusBadge,text} from './components.js';
import {element,routeLink,Panel,Facts,CommandCard,LinkList,InventoryTable,humanTime,values} from './primitives.js';

const state=value=>{const badge=StatusBadge();badge.update(value||'Unknown',['failed','interrupted','revoked','expired','unavailable'].includes(value)?'attention':['running','ready','succeeded','active','verified'].includes(value)?'active':'default');return badge.node;};
const resourceLink=name=>name?routeLink(name,'/resources/'+encodeURIComponent(name)):'—';
const appLink=name=>name?routeLink(name,'/deployments/'+encodeURIComponent(name)):'—';
const duration=seconds=>seconds>=86400?`${seconds/86400} days`:seconds>=3600?`${seconds/3600} hours`:`${seconds||0} seconds`;

// Domain views read independent sections so an unavailable feature never looks like an empty inventory.
export class DomainView {
 constructor(root,page,identity,context){this.root=root;this.page=page;this.identity=identity;this.context=context;this.nextRead=0;this.results={};this.signature=null;root.append(EmptyState('Loading '+(page==='recovery'?'recovery':page),'Reading the controller’s inventory.'));}
 get definitions(){
  if(this.page==='overview')return {resources:'/api/resources',hosts:'/api/hosts',jobs:'/api/recovery/jobs'};
  if(this.page==='resources')return this.identity?{resource:'/api/resources/'+encodeURIComponent(this.identity),logs:'/api/resources/'+encodeURIComponent(this.identity)+'/logs'}:{resources:'/api/resources'};
  if(this.page==='hosts')return this.identity?{hosts:'/api/hosts',host:'/api/hosts/'+encodeURIComponent(this.identity)}:{hosts:'/api/hosts'};
  if(this.page==='recovery')return this.identity?{job:'/api/recovery/'+encodeURIComponent(this.identity)}:{policies:'/api/recovery/policies',backups:'/api/recovery/backups',jobs:'/api/recovery/jobs',destinations:'/api/recovery/destinations'};
  if(this.page==='access')return {clients:'/api/access/clients',grants:'/api/access/authorizations',approval:'/api/access/approval'};
  return {domains:'/api/settings/domains',secrets:'/api/settings/secrets',doctor:'/api/settings/doctor'};
 }
 async refresh(){
  if(Date.now()<this.nextRead)return;this.nextRead=Date.now()+15000;
  const definitions=Object.entries(this.definitions);const results=await Promise.allSettled(definitions.map(([,path])=>this.context.api(path)));
  if(!this.context.current())return;
  const retrying=[];
  definitions.forEach(([key],index)=>{const result=results[index];if(result.status==='rejected'){retrying.push(key);if(this.results[key]?.status==='fulfilled')return;}this.results[key]=result;});
  const signature=JSON.stringify(Object.entries(this.results).map(([key,result])=>[key,result.status,result.status==='fulfilled'?result.value:result.reason?.message]));
  if(signature!==this.signature){this.signature=signature;this.render();}
  const freshness=this.root.querySelector('.inventory-freshness');
  if(freshness)text(freshness,retrying.length?'Some updates delayed · Keeping last received data':`Checked ${new Date().toLocaleTimeString()} · Updates every 15 seconds`);
 }
 section(key,title,description,render){
  const panel=Panel(title,description);const result=this.results[key];
  if(result?.status==='fulfilled')render(panel.content,result.value);
  else panel.content.append(EmptyState('Unavailable',result?.reason?.message||'This section could not be read from the controller.'));
  this.root.append(panel.node);return panel;
 }
 command(title,args,description,host){return CommandCard(title,args,args=>this.context.command(args,host),description,host);}
 render(){
  this.root.replaceChildren();const page=this.page;
  if(page==='settings'){const links=element('div','settings-destinations');for(const [name,path,caption] of [['Access & devices','/access','Who can connect'],['Servers','/hosts','Controller and fleet'],['Databases & caches','/resources','Shared resources'],['Backups & recovery','/recovery','Protect your data'],['Controller monitoring','/observability','All monitoring signals']]){const link=routeLink('',path,'settings-destination');link.append(element('strong','',name),element('span','',caption+' →'));links.append(link);}this.root.append(links);}
  if(page==='overview')this.overview();
  else if(page==='resources')this.identity?this.resource():this.resources();
  else if(page==='hosts')this.hosts();
  else if(page==='recovery')this.identity?this.job():this.recovery();
  else if(page==='access')this.access();else this.settings();
  if(page==='settings')for(const panel of [...this.root.querySelectorAll('.detail-panel')]){const details=element('details','settings-disclosure');details.append(element('summary','',panel.querySelector('h2').textContent));panel.replaceWith(details);details.append(panel);}
  this.root.append(element('p','inventory-freshness',`Inventory checked ${new Date().toLocaleTimeString()} · Updates every 15 seconds while visible`));
 }
 overview(){
  const grid=element('div','summary-grid');
  for(const [key,label,field,path]of [['resources','Resources','resources','/resources'],['hosts','Hosts','hosts','/hosts'],['jobs','Recovery jobs','jobs','/recovery']]){
   const result=this.results[key];const card=routeLink('',path,'summary-card');const rows=result?.status==='fulfilled'?values(result.value[field]):null;
   card.append(element('span','',label),element('strong','',rows?rows.length:'—'),element('small','',rows?'Open inventory →':'Inventory unavailable'));grid.append(card);
  }
  this.root.append(grid);
  const jobs=this.results.jobs?.value?.jobs;const attention=values(jobs).filter(job=>['failed','interrupted','awaiting_cutover'].includes(job.status));
  if(attention.length){const panel=Panel('Recovery needs attention','Inspect the retained job before resuming or cutting over.');panel.content.append(LinkList(attention.map(job=>({label:`${job.source} · ${job.status}`,path:'/recovery/'+encodeURIComponent(job.id)}))));this.root.append(panel.node);}
 }
 resources(){
  this.section('resources','Private resources','Last recorded state and application dependencies. Open a resource for a fresh runtime inspection.',(body,data)=>body.append(InventoryTable([
   ['Resource',row=>resourceLink(row.name)],['Engine',row=>row.type],['Recorded state',row=>state(row.status)],['Allocation',row=>[row.spec?.cpu&&`${row.spec.cpu} CPU`,row.spec?.memory,row.spec?.storage&&`${row.spec.storage} storage`].filter(Boolean).join(' · ')||'Default'],['Used by',row=>LinkList((row.dependents||[]).map(name=>({label:name,path:'/deployments/'+encodeURIComponent(name)})))],
  ],data.resources||[],{emptyTitle:'No private resources',emptyDescription:'Create a database or cache from the CLI and it will appear here.'})));
  this.root.append(this.command('Create a resource',['resource','create','TYPE','RESOURCE','--storage','SIZE'],'Choose the engine, name and storage size in your terminal; review the resource plan before approval.'));
 }
 resource(){
  this.section('resource',this.identity,'Fresh inspection of this resource and its dependencies.',(body,data)=>{
   body.append(Facts([['Engine',data.type],['Runtime state',data.runtimeStatus==='unavailable'?'Inspection unavailable':data.status],['CPU allocation',data.spec?.cpu||'Default'],['Memory allocation',data.spec?.memory||'Default'],['Storage',data.spec?.storage],['Image',data.spec?.image],['Runtime ID',data.runtimeId]]));
   body.append(element('h3','subheading','Connected applications'),LinkList((data.dependents||[]).map(name=>({label:name,path:'/deployments/'+encodeURIComponent(name)}))));
   body.append(routeLink('View backups and recovery →','/recovery?resource='+encodeURIComponent(data.name)+(new URLSearchParams(location.search).get('app')?'&app='+encodeURIComponent(new URLSearchParams(location.search).get('app')):'')));
  });
  this.section('logs','Resource logs','A bounded, redacted tail from the runtime.',(body,data)=>body.append(element('pre','code-block log-tail',data.text||'No log output.')));
  this.root.append(this.command('Inspect resource',['resource','status',this.identity]),this.command('Change allocation',['resource','scale',this.identity,'--cpu','CPU_CORES','--memory','MEMORY_SIZE']),this.command('Create a backup',['resource','backup',this.identity,'--output','BACKUP_FILE']),this.command('Restart resource',['resource','restart',this.identity]));
 }
 hosts(){
  this.section('hosts','Enrolled hosts','Pinned server identities known to this controller. Open a host to check reachability.',(body,data)=>{
   const hosts=values(data.hosts);body.append(InventoryTable([['Host',row=>routeLink(row.name,'/hosts/'+encodeURIComponent(row.name))],['Controller address',row=>row.url],['Pinned fingerprint',row=>row.fingerprint]],this.identity?hosts.filter(host=>host.name===this.identity):hosts,{emptyTitle:'No enrolled hosts',emptyDescription:'This controller currently manages its local host. Enroll additional servers through the CLI.'}));
  });
  if(!this.identity){const local=Panel('Local controller','This server also runs workloads locally. Fleet enrollment adds other servers.');const snapshot=this.context.snapshot?.();local.content.append(Facts([['Hostname',snapshot?.hostname||'Waiting for shared capture'],['Runtime',snapshot?.runtime||'Unknown'],['Observed',humanTime(snapshot?.observedAt)]]));this.root.append(local.node,this.command('Inspect local workloads',['list']),this.command('Inspect local runtime',['doctor']));}
  if(this.identity){
   this.section('host','Host connection','Authenticated state retrieved through the controller’s fleet connection.',(body,data)=>body.append(Facts([['Reachability','Connected'],['Hostname',data.hostname],['Generation',data.generation]])));
   this.root.append(this.command('Inspect workloads on this host',['list'],'Commands target this enrolled host through the selected controller.',this.identity),this.command('Inspect host runtime',['doctor'],'Read runtime health for this specific host.',this.identity));
  }else this.root.append(this.command('Enroll a server',['hosts','enroll','HOST_NAME','--url','https://HOST:PORT','--fingerprint','SHA256_FINGERPRINT'],'Verify the server fingerprint in your trusted terminal before enrolling it.'));
 }
 recovery(){
  const resource=new URLSearchParams(location.search).get('resource');
  if(resource)this.root.append(element('p','notice',`Showing backup and job records for ${resource}.`));
  this.section('policies','Backup policies','Schedule, retention and the next run for each configured resource.',(body,data)=>{
   const policies=values(data.policies).filter(row=>!resource||row.resource===resource);
   body.append(InventoryTable([['Policy',row=>row.name],['Resource',row=>resourceLink(row.resource)],['State',row=>state(row.enabled?'enabled':'disabled')],['Interval',row=>duration(row.everySeconds)],['Retention',row=>`${row.retain} copies`],['Next run',row=>row.enabled?humanTime(row.nextRun):'Disabled']],policies,{emptyTitle:'No matching backup policies',emptyDescription:'Schedules are configured and approved through the CLI.'}));
   const points=values(data.points).filter(row=>!resource||row.source===resource);
   body.append(element('h3','subheading','Verified offsite recovery points'),InventoryTable([['Source',row=>resourceLink(row.source)],['Destination',row=>row.destination],['Verification',row=>state(row.verified?'verified':'unverified')],['Created',row=>humanTime(row.createdAt)]],points,{emptyTitle:'No verified recovery points',emptyDescription:'Verified points appear after a backup and native restore test complete.'}));
  });
  this.section('jobs','Recovery and upgrade jobs','Inspect retained phases and failures before retrying any mutation.',(body,data)=>body.append(InventoryTable([
   ['Job',row=>routeLink(row.id,'/recovery/'+encodeURIComponent(row.id))],['Kind',row=>row.kind],['Source',row=>resourceLink(row.source)],['Target',row=>row.target?.name||'—'],['Status',row=>state(row.status)],['Phase',row=>row.phase],
  ],values(data.jobs).filter(row=>!resource||row.source===resource),{emptyTitle:'No matching recovery jobs',emptyDescription:'Backup runs, imports and upgrades appear here once requested.'})));
  this.section('backups','Native backup receipts','Metadata only; backup downloads and restores require explicit CLI approval.',(body,data)=>body.append(InventoryTable([['Backup ID',row=>row.id],['Resource',row=>resourceLink(row.name)],['Engine',row=>row.type],['Created',row=>humanTime(row.createdAt)]],values(data.backups).filter(row=>!resource||row.name===resource),{emptyTitle:'No matching backup receipts',emptyDescription:'Completed native resource backups appear here.'})));
  this.section('destinations','Backup destinations','Configured storage identities; credential values are never displayed.',(body,data)=>body.append(InventoryTable([['Destination',row=>row.id],['Bucket',row=>row.bucket],['Region',row=>row.region],['Endpoint',row=>row.endpoint]],data.destinations||[],{emptyTitle:'No backup destination configured',emptyDescription:'Configure recovery storage on the controller before creating a policy.'})));
  this.root.append(this.command('Configure recurring backups',['backup','policy','POLICY_NAME','--resource',resource||'RESOURCE','--destination','DESTINATION','--every','24h','--retain','7']),this.command('Inspect backup policies',['backup','list']));
 }
 job(){
  this.section('job','Recovery job','A retained execution receipt, including its current phase and any failure.',(body,data)=>{
   body.append(Facts([['Job ID',data.id],['Kind',data.kind],['Status',data.status],['Phase',data.phase],['Source',data.source],['Target',data.target?.name],['Destination',data.destination],['Policy',data.policy],['Created',humanTime(data.createdAt)],['Failure',data.error||'None reported']]));
   body.append(LinkList([{label:'Source: '+data.source,path:'/resources/'+encodeURIComponent(data.source)}]));
   if(data.evidence)body.append(element('h3','subheading','Verification evidence'),Facts([['Engine',data.evidence.engine],['Version',data.evidence.version],['Content digest',data.evidence.digest]]));
  });
  this.root.append(this.command('Follow this job',['backup','job',this.identity,'--watch']),...(this.results.job?.value?.kind==='upgrade'?[this.command('Inspect upgrade state',['resource','upgrade-status',this.identity])]:[]));
 }
 access(){
  this.section('approval','Human approval','Enrollment provides a device identity. Protected actions require independent hardware approval.',(body,data)=>body.append(Facts([['Required',data.humanApprovalRequired?'Yes':'Not reported'],['Approver enrolled',data.enrolled?'Yes':'No'],['Method',data.method]])));
  this.section('clients','Devices','Enrolled CLI identities and their revocation state.',(body,data)=>{
   body.append(InventoryTable([['Device',row=>row.name],['Client ID',row=>row.id],['Authentication',row=>row.publicKey?'Device signature':'Legacy bearer'],['State',row=>state(row.revoked?'revoked':'active')]],data.clients||[],{emptyTitle:'No enrolled devices'}));
   for(const row of (data.clients||[]).filter(row=>!row.revoked))body.append(this.command('Revoke '+row.name,['clients','revoke',row.id],'Inspect the device identity before revoking it in your terminal.'));
  });
  this.section('grants','Authorization windows','Finite deployment scopes, expiration and usage budgets.',(body,data)=>body.append(InventoryTable([
   ['Grant',row=>row.id],['Device',row=>row.device],['Host',row=>row.host],['Applications',row=>(row.apps||[]).join(', ')],['Scope',row=>row.scope],['Usage',row=>`${row.uses} / ${row.maxUses}`],['Expires',row=>humanTime(row.expiresAt)],['State',row=>state(row.revoked?'revoked':row.expiresAt<Date.now()?'expired':row.uses>=row.maxUses?'exhausted':'active')],
  ],values(data.authorizations),{emptyTitle:'No authorization windows',emptyDescription:'Grant a bounded deployment window from a trusted terminal when needed.'})));
  this.root.append(this.command('Inspect authorization windows',['authorizations','list']));
 }
 settings(){
  this.section('domains','Allowed domains','Public route domains configured on this controller.',(body,data)=>body.append(InventoryTable([['Domain',row=>row]],data.domains||[],{emptyTitle:'No domains configured'})));
  this.section('secrets','Secret references','Names and active revisions only. Secret values stay outside the dashboard.',(body,data)=>body.append(InventoryTable([['Secret',row=>row.name],['Revision',row=>row.revision]],data.secrets||[],{emptyTitle:'No secret references',emptyDescription:'Manage encrypted secret values through the CLI.'})));
  this.section('doctor','Controller diagnostics','Current runtime checks. Use the CLI to inspect full diagnostics.',(body,data)=>{
   const rows=(data.checks||[]).map(check=>[check.name,check.ok?'Available':'Unavailable']);function collect(object,prefix=''){for(const[key,value]of Object.entries(object||{})){if(typeof value==='boolean')rows.push([prefix+key,value?'Available':'Unavailable']);else if(value&&typeof value==='object'&&!Array.isArray(value))collect(value,prefix+key+' / ');}}collect(data);
   body.append(rows.length?Facts(rows):element('p','muted','Diagnostics returned. Run the command below for detailed runtime output.'));
  });
  this.root.append(this.command('Inspect controller',['doctor']),this.command('Add a route domain',['domains','add','DOMAIN']),this.command('Store or rotate a secret',['secret','set','SECRET_NAME','--stdin'],'Read the value securely in your terminal. Never put secret values in command arguments.'));
 }
}
