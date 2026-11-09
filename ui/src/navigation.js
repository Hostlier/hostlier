import {Icon,text} from './components.js';
import {element,routeLink} from './primitives.js';

export const destinations=[
 {key:'overview',label:'Apps & tunnels',group:'Controller',icon:'box',description:'Your apps and tunnels, in one place.'},
 {key:'deployments',label:'Apps & tunnels',group:'Controller',icon:'box',description:'Your apps and tunnels, in one place.'},
 {key:'tunnels',label:'Tunnel',group:'Controller',icon:'arrow',description:'Connection status and access details.'},
 {key:'logs',label:'Logs',group:'Controller',icon:'history',description:'Search application logs collected by this controller.'},
 {key:'operations',label:'Activity',group:'Controller',icon:'history',description:'Recent changes across your controller.'},
 {key:'settings',label:'Settings',group:'Controller',icon:'settings',description:'Connections, access, and controller configuration.'},
 {key:'observability',label:'Metrics & logs',group:'Controller settings',icon:'activity',description:'Controller-wide monitoring signals.'},
 {key:'resources',label:'Databases & caches',group:'Controller settings',icon:'database',description:'Shared databases and caches.'},
 {key:'hosts',label:'Servers',group:'Controller settings',icon:'server',description:'Your controller and enrolled servers.'},
 {key:'recovery',label:'Backups & recovery',group:'Controller settings',icon:'restart',description:'Backup schedules and recovery history.'},
 {key:'access',label:'Access & devices',group:'Controller settings',icon:'shield',description:'Connected devices and permissions.'},
];
export const appDestinations=[
 {key:'overview',label:'Overview',icon:'grid',description:'Health and deployment details for this app.'},
 {key:'metrics',label:'Metrics',icon:'activity',description:'Live measurements for this app.'},
 {key:'logs',label:'Logs',icon:'history',description:'Runtime output from this app and its components.'},
 {key:'activity',label:'Activity',icon:'history',description:'Deployments and changes for this app.'},
 {key:'resources',label:'Resources',icon:'database',description:'The databases and caches this app uses.'},
 {key:'settings',label:'Settings',icon:'settings',description:'Configuration, endpoints, and revision history.'},
];
export const appPath=(name,view='overview')=>'/deployments/'+encodeURIComponent(name)+(view==='overview'?'':'?view='+view);
export const destination=key=>destinations.find(item=>item.key===key);
export function Navigation(root){
 const toggle=document.getElementById('navigation-toggle');let context;
 toggle?.addEventListener('click',()=>{const open=toggle.getAttribute('aria-expanded')!=='true';toggle.setAttribute('aria-expanded',String(open));root.classList.toggle('is-open',open);});
 root.addEventListener('keydown',event=>{if(event.key==='Escape'){toggle?.setAttribute('aria-expanded','false');root.classList.remove('is-open');toggle?.focus();}});
 return {select(key,app=null,view='overview'){
  const next=app||'';
  if(context!==next){
   context=next;root.replaceChildren();
   if(app)root.append(routeLink('← All applications','/overview','back-to-controller'));
   const section=element('div','nav-group');section.append(element('h2','nav-group-label',app||'Controller'));
   const items=app?appDestinations:destinations.filter(item=>['overview','logs','operations','settings'].includes(item.key));
   for(const item of items){const link=routeLink('',app?appPath(app,item.key):'/'+item.key,'nav-link');link.dataset.key=item.key;link.append(Icon(item.icon),element('span','nav-title',item.label));section.append(link);}
   root.append(section);
   const note=document.querySelector('.sidebar-note');if(note)text(note,app?'Monitoring this application':'Open an app to see its metrics and logs.');
  }
  const selected=app?view:['overview','deployments','tunnels'].includes(key)?'overview':key==='operations'?'operations':key==='logs'?'logs':'settings';
  for(const link of root.querySelectorAll('.nav-link')){const current=link.dataset.key===selected;link.classList.toggle('selected',current);if(current)link.setAttribute('aria-current','page');else link.removeAttribute('aria-current');}
  const label=document.getElementById('current-section');if(label)text(label,app?app+' / '+(appDestinations.find(item=>item.key===view)?.label||'Overview'):destination(selected)?.label||'Navigation');
  toggle?.setAttribute('aria-expanded','false');root.classList.remove('is-open');
 }};
}
