import {text} from './components.js';
// All reusable page primitives assign controller-provided values as text, including diagnostic JSON.
export function element(tag, className='', value) {
  const node=document.createElement(tag); node.className=className;
  if(value!==undefined) text(node,value);
  return node;
}
export function routeLink(label,path,className='text-link') {
  const node=element('a',className,label); node.href=path; node.dataset.route=''; return node;
}
export function Panel(title,caption) {
  const node=element('section','detail-panel'); const head=element('div','panel-heading');
  head.append(element('h2','',title)); if(caption) head.append(element('p','',caption));
  const content=element('div','panel-body'); node.append(head,content); return {node,content};
}
export function Facts(values) {
  const node=element('dl','facts');
  for(const [label,value] of values) {const row=element('div');row.append(element('dt','',label),element('dd','',value??'—'));node.append(row);}
  return node;
}
export function JsonDetails(label,value,open=false) {
  const node=element('details','json-details');node.open=open;
  node.append(element('summary','',label),element('pre','code-block',JSON.stringify(value??{},null,2)));return node;
}

// Quote every dynamic CLI argument as data; the browser never executes a shell command.
export function shellArgument(value) {
  const text=String(value??'');return /^[a-zA-Z0-9_./:@+=,-]+$/.test(text)?text:"'"+text.replace(/'/g,"'\"'\"'")+"'";
}
export function commandLine(args,connection={},host) {
  const suffix=connection.name?['--server',connection.name]:connection.url?['--server','SAVED_PROFILE']:['--server','PROFILE'];
  const context=connection.cli||{};const prefix=[];if(context.home)prefix.push('HOSTLIER_HOME='+shellArgument(context.home));if(context.caFile)prefix.push('HOSTLIER_CA_FILE='+shellArgument(context.caFile));
  return [...prefix,[context.binary||'hostlier',...args,...suffix,...(host?['--on',host]:[])].map(shellArgument).join(' ')].join(' ');
}
export function CommandCard(title,args,command,description='Run in your terminal. Replace uppercase placeholders and review changes before approving.',host) {
  const node=element('section','command-card');const header=element('div','command-heading');
  header.append(element('h3','',title));const button=element('button','button','Copy command');button.type='button';header.append(button);
  const code=element('code','cli-command',command(args));code.dataset.cliArgs=JSON.stringify(args);if(host)code.dataset.cliHost=host;
  button.addEventListener('click',async()=>{try{await navigator.clipboard.writeText(code.textContent);text(button,'Copied');}catch{text(button,'Select and copy below');}});
  node.append(header,element('p','',description),code);return node;
}
export function LinkList(items,empty='None recorded') {
  const node=element('div','related-links');if(!items.length)node.append(element('span','muted',empty));
  for(const item of items)node.append(routeLink(item.label,item.path));return node;
}
export function InventoryTable(columns,rows,{emptyTitle='Nothing here yet',emptyDescription='Records will appear when configured.'}={}) {
  const wrapper=element('div','table-container');
  if(!rows.length){const empty=element('div','empty-state');empty.append(element('strong','',emptyTitle),element('p','',emptyDescription));wrapper.append(empty);return wrapper;}
  const table=element('table','inventory-table');const head=element('thead');const line=element('tr');
  for(const [title]of columns){const th=element('th','',title);th.scope='col';line.append(th);}head.append(line);table.append(head);
  const body=element('tbody');
  for(const row of rows){const tr=element('tr');for(const [,render]of columns){const td=element('td');const value=render(row);if(value?.nodeType)td.append(value);else text(td,value);tr.append(td);}body.append(tr);}
  table.append(body);wrapper.append(table);return wrapper;
}
export function humanTime(value) {
  return value&&new Date(value).getUTCFullYear()>2000?new Date(value).toLocaleString():'—';
}
export function values(object) {return Array.isArray(object)?object:Object.entries(object||{}).map(([id,value])=>({...value,id:value?.id||id,name:value?.name||id}));}
