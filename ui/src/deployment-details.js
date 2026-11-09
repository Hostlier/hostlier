import {StatusBadge,OperationList} from './components.js';
import {element,routeLink,Panel,Facts,LinkList,CommandCard,InventoryTable,JsonDetails} from './primitives.js';
import {appPath} from './navigation.js';

// Each app has its own dashboard. Read and render only the selected section.
export function renderDeploymentDetails(page,data){
 const spec=data.spec||{};const view=page.appView||'overview';
 if(view==='logs')return;
 if(view==='resources'){
  const resources=Panel('Connected resources','Databases and caches used by this application.');
  resources.content.append(LinkList(Object.entries(data.resourceRefs||{}).map(([name,resource])=>({label:`${name} · ${resource}`,path:'/resources/'+encodeURIComponent(resource)+'?app='+encodeURIComponent(data.name)})),'This app has no connected databases or caches.'));page.detailBody.append(resources.node);return;
 }
 if(view==='activity'){
  const activity=Panel('Recent activity','Changes to this application.');const list=element('div','operation-list');page.activityError=element('p','notice');page.activityError.hidden=true;activity.content.append(page.activityError,list);page.appOperations=OperationList(list);page.detailBody.append(activity.node);
  const workflow=Panel('Deployment steps');page.workflowBody=workflow.content;page.workflowBody.append(element('p','muted','Loading workflow steps…'));page.detailBody.append(workflow.node);return;
 }
 if(view==='overview'){
  const controls=element('div','detail-actions');const badge=StatusBadge();badge.update(data.status);controls.append(badge.node,routeLink('View logs →',appPath(data.name,'logs')));page.detailBody.append(controls);
  const grid=element('div','detail-grid');const health=Panel('How it’s running');page.observedFacts=health.content;grid.append(health.node);
  const summary=Panel('Current deployment');summary.content.append(Facts([['Address',data.url||'Private application'],['Image',spec.image],['Revision',data.revision],['Components',1+Object.keys(spec.sidecars||{}).length]]));grid.append(summary.node);page.detailBody.append(grid);
  return;
 }
 const endpoints=Panel('Endpoints','Named listeners, public addresses and upstream transports.');
 const listeners=Object.entries(spec.endpoints||{}).map(([name,value])=>({name,...value}));
 if(!listeners.length&&spec.endpoint)listeners.push({name:'primary',...spec.endpoint});
 endpoints.content.append(InventoryTable([['Endpoint',row=>row.name+(row.name===spec.primaryEndpoint?' · primary':'')],['Exposure',row=>row.exposure],['Address',row=>row.route||'Internal'],['Port',row=>row.port||'None'],['Upstream',row=>row.protocol||'http'],['HTTP versions',row=>(row.httpVersions||['h1','h2']).join(', ')]],listeners,{emptyTitle:'No inbound endpoints'}));page.detailBody.append(endpoints.node);
 const permissions=trafficPolicyRows(listeners);
 if(permissions.length){const policies=Panel('Traffic policies','Only these paths and methods are admitted on policy endpoints. Other requests in their reserved paths are denied.');policies.content.append(InventoryTable([['Endpoint',row=>row.endpoint],['Protocol',row=>row.protocol],['Path',row=>row.path],['Methods',row=>row.methods],['Client HTTP',row=>row.versions],['Idle timeout',row=>row.timeout]],permissions));page.detailBody.append(policies.node);}
 const components=Panel('Components','Primary workload and declared sidecars.');const observed=data.runtimeStatus?.components||[];
 const rows=[{name:'app',spec},...Object.entries(spec.sidecars||{}).map(([name,spec])=>({name,spec}))].map(row=>({...row,observation:observed.find(value=>value.name===row.name)}));
 components.content.append(InventoryTable([['Component',row=>row.name],['Image',row=>row.spec.image],['Process',row=>row.observation?(row.observation.running?'Running':'Stopped'):'Not observed'],['Ready',row=>row.observation?String(row.observation.ready):'Unknown'],['Restarts',row=>row.observation?.restartCount??'—']],rows));page.detailBody.append(components.node);
 const configuration=Panel('Configuration','Health checks, storage, environment variable names and secret references.');
 configuration.content.append(Facts([['Health check',spec.health?.path||spec.health?.exec?.join(' ')||'Not configured'],['Timeout',spec.health?.timeoutSeconds?`${spec.health.timeoutSeconds}s`:'Default'],['Environment names',Object.keys(spec.env||{}).join(', ')||'None'],['Secret references',Object.keys(data.secretRefs||{}).join(', ')||'None'],['Autoscaling',spec.autoscaling?'Configured':'Not configured']]));
 configuration.content.append(InventoryTable([['Volume',row=>row.name],['Mount',row=>row.mount],['Size',row=>row.size]],Object.entries(spec.volumes||{}).map(([name,value])=>({name,...value})),{emptyTitle:'No persistent volumes'}));
 const files=Object.entries(spec.configs||{}).map(([name,value])=>({name,...value}));
 configuration.content.append(InventoryTable([['Managed file',row=>row.name],['Mount',row=>row.mount],['Source',row=>row.secret?'Encrypted secret reference':'Reviewed configuration'],['Access',()=> 'Read-only']],files,{emptyTitle:'No managed configuration files'}));
 configuration.content.append(JsonDetails('Advanced project configuration',spec));page.detailBody.append(configuration.node);
 const revisions=Panel('Revision history','Retained revisions available for rollback inspection.');revisions.content.append(InventoryTable([['Revision',row=>row.id],['Image',row=>row.spec?.image||'—']],data.revisions||[],{emptyTitle:'No older revisions'}));page.detailBody.append(revisions.node);
 const command=args=>page.command(args);const handoffs=Panel('Continue in the CLI','These commands target the selected controller. Review plans and hardware approvals in your terminal.');
 for(const [title,args]of [['Inspect application',['status',data.name]],['Update from your project',['update','--file','PROJECT_FILE']],['Change allocation',['scale',data.name,'--cpu','CPU_CORES','--memory','MEMORY_SIZE']],['Rollback to a revision',['rollback',data.name,'--revision','REVISION_ID']],['Manage components',['sidecar','attach',data.name,'--name','COMPONENT','--file','MEMBER_FILE']],['Restart application',['restart',data.name]]])handoffs.content.append(CommandCard(title,args,command));
 const advanced=element('details','json-details');advanced.append(element('summary','','Manage from the CLI'),handoffs.node);page.detailBody.append(advanced);
}

// Traffic permissions show the same method, service and endpoint inheritance applied by the edge.
export function trafficPolicyRows(listeners){
 const rows=[];
 for(const endpoint of listeners){
  for(const [service,policy]of Object.entries(endpoint.grpc?.services||{}))for(const [method,override]of Object.entries(policy.methods||{}))rows.push({endpoint:endpoint.name,protocol:'gRPC',path:`/${service}/${method}`,methods:'POST',versions:'h2',timeout:idleBudget(override.idleTimeoutSeconds||policy.idleTimeoutSeconds||endpoint.idleTimeoutSeconds)});
  for(const policy of Object.values(endpoint.http?.routes||{}))rows.push({endpoint:endpoint.name,protocol:policy.protocol,path:policy.path,methods:(policy.methods||[]).join(', '),versions:policy.httpVersions?.join(', ')||(policy.protocol==='websocket'?'h1':'Any enabled'),timeout:idleBudget(policy.idleTimeoutSeconds||endpoint.idleTimeoutSeconds)});
 }
 return rows;
}

// A zero budget inherits the proxy's default instead of claiming an unlimited stream lifetime.
function idleBudget(seconds){return seconds?`${seconds}s`:'Proxy default';}
