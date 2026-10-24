"use strict";
(() => {
const icons = {
  archive: '<rect x="3" y="4" width="18" height="5" rx="1.5"/><path d="M5 9v9a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V9m-9 4h4"/>',
  now: '<path d="M3 12h4l3-7 4 14 3-7h4"/>',
  graph: '<rect x="3" y="9" width="5" height="6" rx="1.5"/><rect x="16" y="3" width="5" height="6" rx="1.5"/><rect x="16" y="15" width="5" height="6" rx="1.5"/><path d="M8 12h4V6h4m-4 6v6h4"/>',
  agents: '<circle cx="9" cy="8" r="3"/><path d="M3 20v-2a6 6 0 0 1 12 0v2M16 5a3 3 0 0 1 0 6m2 3a5 5 0 0 1 3 4v2"/>',
  agent: '<rect x="4" y="5" width="16" height="15" rx="4"/><path d="M12 2v3M8 11v1m8-1v1m-7 4h6"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"/>',
  moon: '<path d="M20.5 14A8.8 8.8 0 0 1 10 3.5 9 9 0 1 0 20.5 14Z"/>',
  search: '<circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 4.5 4.5"/>',
  attention: '<path d="m10.3 4-8 14a2 2 0 0 0 1.7 3h16a2 2 0 0 0 1.7-3l-8-14a2 2 0 0 0-3.4 0Z"/><path d="M12 9v4m0 3v.2"/>',
  working: '<circle cx="12" cy="12" r="9"/><path d="M12 3a9 9 0 0 1 9 9" stroke-width="4"/><circle cx="12" cy="12" r="2" fill="currentColor" stroke="none"/>',
  finished: '<circle cx="12" cy="12" r="9"/><path d="m8 12 3 3 5-6"/>',
  waiting: '<circle cx="12" cy="12" r="9"/><path d="M10 9v6m4-6v6"/>',
  idle: '<circle cx="12" cy="12" r="8"/><path d="M9 12h6"/>',
  minus: '<path d="M5 12h14"/>', plus: '<path d="M5 12h14M12 5v14"/>',
  fit: '<path d="M8 3H3v5m13-5h5v5M3 16v5h5m13-5v5h-5"/>',
  close: '<path d="m6 6 12 12M6 18 18 6"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6m0-10v.2"/>'
};
const icon = name => `<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${icons[name] || icons.info}</svg>`;
document.querySelectorAll('[data-icon]').forEach(el => el.replaceWith(Object.assign(document.createElement('span'), { innerHTML: icon(el.dataset.icon) })));

const $ = id => document.getElementById(id);
const graph = $('graph-world'), panel = $('graph-panel'), agentsPanel = $('agents-panel'), nowPanel = $('now-panel');
const svgNS = 'http://www.w3.org/2000/svg';
const views = ['now','graph','agents'];
// focus is the Task highlighted in the graph with its direct neighbours; it stays after the details close.
const state = {view:'now',theme:'light',query:'',filter:'all',zoom:1,selected:null,focus:null,archived:false,allFinished:false};
let graphTasks = [], graphEdges = [], hiddenEdges = [], focusIds = null;
let model = null, etag = '', instance = document.querySelector('meta[name="a-stack-instance"]').content, version = document.querySelector('meta[name="a-stack-version"]').content;
let timer, controller, failures = 0, busy = false, lastSuccess = 0, stale = false, detailKey = '', detailEpoch = 0, returnFocus;
let geometryKey = '', width = 1400, height = 670;
const cards = new Map(), rows = new Map(), positions = new Map();
const path = location.pathname.split('/');
const context = {workspace_id:path[2],session_id:path[3]};
const storageKey = 'a-stack-read:' + location.pathname;
let token = '';
function readToken() {
  const fragment = location.hash.slice(1);
  history.replaceState(null,'',location.pathname);
  if (fragment) { sessionStorage.removeItem(storageKey); if (/^[A-Za-z0-9_-]{43}$/.test(fragment)) sessionStorage.setItem(storageKey,fragment); }
  return sessionStorage.getItem(storageKey) || '';
}
// The theme survives browser restarts; until one is picked, follow the system.
function savedTheme() {
  try { const t=localStorage.getItem('a-stack-theme'); if (t==='dark'||t==='light') return t; } catch {}
  return matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light';
}
state.theme = savedTheme();
// The last tab and the Archived toggle are remembered per browser, like the theme.
function load(key) { try { return localStorage.getItem(key); } catch { return null; } }
function save(key,value) { try { localStorage.setItem(key,value); } catch {} }
if (views.includes(load('a-stack-view'))) state.view = load('a-stack-view');
state.archived = load('a-stack-archived') === '1';
try {
  token = readToken();
} catch { history.replaceState(null,'',location.pathname); }
window.addEventListener('hashchange',()=>{
  try { token=readToken(); } catch { token=''; history.replaceState(null,'',location.pathname); }
  clearTimeout(timer);controller?.abort();failures=0;
  if(token)refresh();else{stale=true;updateSource('Read credential missing · run a-stack open');}
});
const labels = {waiting:'Waiting',working:'Working',finished:'Finished',cancelled:'Cancelled'};
function el(tag, cls, text) { const n = document.createElement(tag); if (cls) n.className=cls; if (text !== undefined) n.textContent=text; return n; }
function setText(n,text) { if (n.textContent !== String(text)) n.textContent=text; }
function iconNode(name) { const n=el('span'); n.innerHTML=icon(name); return n; } // only constant artwork
// ponytail: linear ownership scans measured at 1,000 Tasks; index by Task if larger Sessions require it.
function owners(t) { return model.assignments.filter(a=>a.task_id===t.id).map(a=>a.agent_name).sort(); }
function matches(t) {
  return (state.filter==='all' || (state.filter==='blocked' ? t.needs_you : t.progress===state.filter)) &&
    [t.title,t.ticket_ref,t.report_preview,...owners(t)].join(' ').toLowerCase().includes(state.query.trim().toLowerCase());
}
function status(n,t) {
  const key=t.progress+':'+t.needs_you;
  if (n.dataset.key===key) return;
  n.dataset.key=key;n.className='status '+t.progress;
  n.replaceChildren(iconNode(t.progress==='cancelled'?'idle':t.progress), document.createTextNode(labels[t.progress] || t.progress));
  if (t.needs_you) { const a=el('span','status blocked','Needs you');n.append(' · ',a); }
}
function makeCard(t) {
  const article=el('article','task-unit');article.dataset.task=t.id;
  const own=el('div','owners'), button=el('button','task-card');button.dataset.openTask=t.id;
  const st=el('span','status'),title=el('span','task-title'),note=el('span','task-note'),foot=el('span','task-caption');
  button.append(st,title,note,foot);article.append(own,button);
  button.addEventListener('click',()=>openTask(t.id,button));
  return {article,own,button,st,title,note,foot};
}
function patchCard(c,t,graphCard=false) {
  const names=owners(t), ownKey=names.join('\n');
  if(c.own.dataset.key!==ownKey) {
    c.own.dataset.key=ownKey;c.own.replaceChildren(...(names.length?names:['Unassigned']).map(name=>{
      const n=el('span','owner');n.append(iconNode('agent'),el('span','owner-name',name));return n;
    }));
  }
  status(c.st,t);setText(c.title,t.title);setText(c.note,t.report_preview || 'No report yet.');
  const caption=[t.reference_count ? `${t.reference_count} file reference${t.reference_count===1?'':'s'}`:'',t.cancelled_prerequisite_count?`${t.cancelled_prerequisite_count} cancelled prerequisite${t.cancelled_prerequisite_count===1?'':'s'}`:''].filter(Boolean).join(' · ');
  setText(c.foot,caption);
  c.button.classList.toggle('selected',state.selected===t.id);
  c.button.setAttribute('aria-label',`${t.title}. ${labels[t.progress]}.${t.needs_you?' Needs you.':''} Assigned to ${names.join(' and ') || 'Unassigned'}. ${caption}`);
  const visible=matches(t);
  c.article.classList.toggle('archived',graphCard&&done(t));
  c.article.classList.toggle('unfocused',graphCard&&!!focusIds&&!focusIds.has(t.id));
  c.article.classList.toggle('dimmed',graphCard&&!visible);
  c.article.inert=graphCard&&!visible;
  if (graphCard) c.article.setAttribute('aria-hidden',String(!visible));
  else c.article.hidden=!visible;
}
const edgesSVG = document.createElementNS(svgNS,'svg');edgesSVG.classList.add('connections');edgesSVG.setAttribute('aria-hidden','true');
edgesSVG.innerHTML='<defs><marker id="arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="5" markerHeight="5" orient="auto"><path d="M1 1l5 3-5 3" fill="none" stroke="var(--edge)" stroke-width="1.5"/></marker></defs><g></g>';
graph.append(edgesSVG);
const done = t => t.progress==='finished'||t.progress==='cancelled';
// The focused Task and its direct prerequisites and dependents, from every dependency, also the lines the graph hides.
function focusSet() {
  const s=new Set([state.focus]);
  for(const e of model.dependencies){if(e.prerequisite_id===state.focus)s.add(e.dependent_id);if(e.dependent_id===state.focus)s.add(e.prerequisite_id);}
  return s;
}
// Transitive reduction: a line u -> v is hidden when v is reachable from u through another path. Reachability bitsets in
// reverse topological order cost about O(V·E/32). Edges on a cycle (which the service rejects) are kept.
function reduceEdges(tasks,deps) {
  const index=new Map(tasks.map((t,i)=>[t.id,i])), n=tasks.length, words=Math.ceil(n/32)||1, out=tasks.map(()=>[]), indegree=new Uint32Array(n);
  for(const e of deps){const a=index.get(e.prerequisite_id),b=index.get(e.dependent_id);if(a===undefined||b===undefined)continue;out[a].push([b,e]);indegree[b]++;}
  const order=[];for(let i=0;i<n;i++)if(!indegree[i])order.push(i);
  for(let i=0;i<order.length;i++)for(const [b] of out[order[i]])if(!--indegree[b])order.push(b);
  const reach=[], kept=[], hidden=[], inOrder=new Set(order);
  for(let i=order.length-1;i>=0;i--) {
    const u=order[i], r=new Uint32Array(words);
    for(const [v] of out[u])if(reach[v])for(let w=0;w<words;w++)r[w]|=reach[v][w];
    for(const [v,e] of out[u])((r[v>>>5]>>>(v&31))&1?hidden:kept).push(e);
    for(const [v] of out[u])r[v>>>5]|=1<<(v&31);
    reach[u]=r;
  }
  for(let u=0;u<n;u++)if(!inOrder.has(u))for(const [,e] of out[u])kept.push(e);
  return {kept,hidden};
}
// One column per dependency rank with the cards stacked and centered, and bracket lines through the gap between
// columns, as in v0.1.0. Tasks without a drawn line sit in rows below the connected graph, so no line runs past them.
function layout() {
  const wasHidden=panel.hidden; if(wasHidden){panel.hidden=false;panel.style.visibility='hidden';}
  const tasks=graphTasks, byID=new Map(tasks.map(t=>[t.id,t])), rank=new Map(tasks.map(t=>[t.id,0]));
  const degree=new Map(tasks.map(t=>[t.id,0])), next=new Map(tasks.map(t=>[t.id,[]])), linked=new Set();
  for(const e of graphEdges) if(byID.has(e.prerequisite_id)&&byID.has(e.dependent_id)) {degree.set(e.dependent_id,degree.get(e.dependent_id)+1);next.get(e.prerequisite_id).push(e.dependent_id);linked.add(e.prerequisite_id).add(e.dependent_id);}
  const queue=tasks.filter(t=>!degree.get(t.id)).map(t=>t.id).sort();
  for(let i=0;i<queue.length;i++) for(const id of next.get(queue[i])) {
    rank.set(id,Math.max(rank.get(id),rank.get(queue[i])+1));degree.set(id,degree.get(id)-1);if(!degree.get(id))queue.push(id);
  }
  const layers=new Map();
  for(const t of tasks) if(linked.has(t.id)) {const r=rank.get(t.id);if(!layers.has(r))layers.set(r,[]);layers.get(r).push(t.id);}
  // Barycenter sweeps place each Task near the Tasks it connects to, so fewer lines cross; ties keep the stable Task order.
  const prev=new Map(tasks.map(t=>[t.id,[]]));for(const [id,ns] of next)for(const n of ns)prev.get(n).push(id);
  const cols=[...layers.keys()].sort((a,b)=>a-b).map(r=>layers.get(r)), order=new Map(), place=l=>l.forEach((id,i)=>order.set(id,(i+.5)/l.length));cols.forEach(place);
  const sweep=(l,near)=>{const c=new Map(l.map(id=>{const n=near.get(id);return [id,n.length?n.reduce((s,x)=>s+order.get(x),0)/n.length:order.get(id)];}));l.sort((a,b)=>c.get(a)-c.get(b));place(l);};
  for(let i=1;i<cols.length;i++)sweep(cols[i],prev);for(let i=cols.length-2;i>=0;i--)sweep(cols[i],next);for(let i=1;i<cols.length;i++)sweep(cols[i],prev);
  const sizes=new Map(tasks.map(t=>[t.id,cards.get(t.id).article.offsetHeight]));
  const totals=[...layers.values()].map(ids=>ids.reduce((h,id)=>h+sizes.get(id)+32,0)-32), top=Math.max(0,...totals);
  let w=layers.size?Math.max(...layers.keys())*342+274:0, y=24;
  positions.clear();
  const put=(id,x,py)=>{positions.set(id,{x,y:py});const n=cards.get(id).article;n.style.left=x+'px';n.style.top=py+'px';};
  for(const [r,ids] of layers) {
    const total=ids.reduce((h,id)=>h+sizes.get(id)+32,0)-32;let py=24+(top-total)/2;
    for(const id of ids){put(id,28+r*342,py);py+=sizes.get(id)+32;}
  }
  if(layers.size)y+=top+64;
  const loose=tasks.filter(t=>!linked.has(t.id)).map(t=>t.id), perRow=Math.max(1,Math.floor((Math.max(w,(panel.clientWidth||1400)-88)+68)/342));
  for(let i=0;i<loose.length;i+=perRow) {
    const row=loose.slice(i,i+perRow);row.forEach((id,j)=>put(id,28+j*342,y));
    y+=Math.max(...row.map(id=>sizes.get(id)))+32;w=Math.max(w,row.length*342-68);
  }
  width=w+56;height=y-32+24; // tight around the Task units, so Fit fits the cards
  graph.style.width=width+'px';graph.style.height=height+'px';applyZoom();drawEdges();
  if(wasHidden){panel.hidden=true;panel.style.visibility='';}
}
function roundedPath(points, radius = 11) {
  let path = `M${points[0].join(',')}`;
  for (let i = 1; i < points.length - 1; i++) {
    const [x,y] = points[i], [px,py] = points[i-1], [nx,ny] = points[i+1];
    const before = Math.hypot(x-px,y-py), after = Math.hypot(nx-x,ny-y);
    if (!before || !after) continue;
    const r = Math.min(radius,before/2,after/2);
    const a = [x + (px-x)*r/before,y + (py-y)*r/before];
    const b = [x + (nx-x)*r/after,y + (ny-y)*r/after];
    path += `L${a.join(',')}Q${x},${y} ${b.join(',')}`;
  }
  return path + `L${points.at(-1).join(',')}`;
}

function drawEdges() {
  const group=edgesSVG.querySelector('g'), frag=document.createDocumentFragment();
  const ends=e=>{
    const a=cards.get(e.prerequisite_id),b=cards.get(e.dependent_id),ap=positions.get(e.prerequisite_id),bp=positions.get(e.dependent_id);if(!a||!b||!ap||!bp)return null;
    return [ap.x+274,ap.y+a.button.offsetTop+a.button.offsetHeight/2,bp.x,bp.y+b.button.offsetTop+b.button.offsetHeight/2];
  };
  for(const e of graphEdges) {
    const p=ends(e);if(!p)continue;const [x1,y1,x2,y2]=p,m=(x1+x2)/2;
    frag.append(connection(e,roundedPath([[x1,y1],[m,y1],[m,y2],[x2-3,y2]])));
  }
  // The focused Task also shows its direct lines that the reduction hides, dashed.
  if(state.focus)for(const e of hiddenEdges) {
    if(e.prerequisite_id!==state.focus&&e.dependent_id!==state.focus)continue;
    const p=ends(e);if(!p)continue;const [x1,y1,x2,y2]=p,dx=Math.max(48,Math.abs(x2-x1)/2);
    const c=connection(e,`M${x1},${y1}C${x1+dx},${y1} ${x2-3-dx},${y2} ${x2-3},${y2}`);c.classList.add('implied');frag.append(c);
  }
  group.replaceChildren(frag);
}
function connection(e,d) {
  const p=document.createElementNS(svgNS,'path');p.setAttribute('d',d);p.setAttribute('marker-end','url(#arrow)');p.classList.add('connection');
  const near=e.prerequisite_id===state.focus||e.dependent_id===state.focus||e.prerequisite_id===state.selected||e.dependent_id===state.selected;
  if(near)p.classList.add('active');else if(state.focus)p.classList.add('unfocused');
  if(!matches(model.tasks.find(t=>t.id===e.prerequisite_id))&&!matches(model.tasks.find(t=>t.id===e.dependent_id)))p.classList.add('faded');
  return p;
}
function measure(name,run) {performance.clearMarks(name+'-start');performance.clearMarks(name+'-end');performance.clearMeasures(name);performance.mark(name+'-start');run();performance.mark(name+'-end');performance.measure(name,name+'-start',name+'-end');}
function patchGraph() {
  // Finished and cancelled Tasks are archived: hidden from the graph unless the Archived toggle is on.
  graphTasks=state.archived?model.tasks:model.tasks.filter(t=>!done(t));
  if(state.focus&&!graphTasks.some(t=>t.id===state.focus))state.focus=null;
  focusIds=state.focus?focusSet():null;
  const ids=new Set(graphTasks.map(t=>t.id));
  for(const [id,c] of cards)if(!ids.has(id)){c.article.remove();cards.delete(id);}
  for(const t of graphTasks) {let c=cards.get(t.id);if(!c){c=makeCard(t);cards.set(t.id,c);graph.append(c.article);}patchCard(c,t,true);}
  const key=JSON.stringify([graphTasks.map(t=>[t.id,t.title,owners(t),t.needs_you,t.reference_count,t.cancelled_prerequisite_count]),model.dependencies.map(e=>[e.prerequisite_id,e.dependent_id])]);
  if(geometryKey!==key){
    geometryKey=key;
    measure('a-stack-layout',()=>{measure('a-stack-reduce',()=>{({kept:graphEdges,hidden:hiddenEdges}=reduceEdges(graphTasks,model.dependencies));});layout();});
  }else drawEdges();
  const archived=model.tasks.filter(done).length;setText($('archived-label'),`Archived (${archived})`);$('archived').setAttribute('aria-pressed',String(state.archived));
  $('archived').setAttribute('aria-label',`${state.archived?'Hide':'Show'} ${plural(archived,'archived Task')} in the graph`);
}
function patchAgents() {
  const all=[...model.agents,{id:'unassigned',name:'Unassigned'}], live=new Set();
  for(const a of all) {
    const assigned=a.id==='unassigned'?model.tasks.filter(t=>t.unassigned):model.tasks.filter(t=>model.assignments.some(x=>x.task_id===t.id&&x.agent_id===a.id));
    if(a.id==='unassigned'&&!assigned.length)continue;
    live.add(a.id);let r=rows.get(a.id);
    if(!r){
      const section=el('section','agent-row'),heading=el('div','agent-heading'),identity=el('div','agent-identity'),avatar=el('span','agent-avatar'),names=el('div'),name=el('h2','agent-name'),meta=el('div','agent-meta'),runtime=el('div','agent-state'),tasks=el('div','agent-tasks'),empty=el('p','agent-meta','No assigned Tasks.');
      identity.append(avatar,names);names.append(name,meta);heading.append(identity,runtime);section.append(heading,tasks);tasks.append(empty);$('agent-list').append(section);r={section,name,avatar,meta,runtime,tasks,empty,cards:new Map()};rows.set(a.id,r);
    }
    setText(r.name,a.name);setText(r.avatar,a.name.slice(0,2));setText(r.meta,a.id==='unassigned'?'No assignee':a.id===model.snapshot.session.orchestrator_id?'Orchestrator':'Registered Agent');
    setText(r.runtime,a.id==='unassigned'?'':runtimeText(a));
    const assignedIDs=new Set(assigned.map(t=>t.id));for(const [id,c]of r.cards)if(!assignedIDs.has(id)){c.article.remove();r.cards.delete(id);}
    for(const t of assigned){let c=r.cards.get(t.id);if(!c){c=makeCard(t);r.cards.set(t.id,c);r.tasks.append(c.article);}patchCard(c,t);}
    const count=assigned.filter(matches).length;r.empty.hidden=assigned.length>0;r.section.hidden=(state.query||state.filter!=='all')&&!count;
  }
  for(const [id,r] of rows)if(!live.has(id)){r.section.remove();rows.delete(id);}
}
// The Now board: one card per Task, moved between columns in place, so focus and scroll positions survive polls.
const columns=[
  {key:'attention',title:'Needs you',empty:'Nothing needs you.'},
  {key:'working',title:'Working',empty:'Nothing is working.'},
  {key:'waiting',title:'Waiting',empty:'Nothing is waiting.'},
  {key:'done',title:'Recently finished',empty:'Nothing has finished yet.'}
].map(c=>{
  const section=el('section','now-column'),head=el('header','now-head'),h=el('h2','',c.title),count=el('span','now-count'),list=el('div','now-list'),empty=el('p','now-empty',c.empty);
  section.dataset.column=c.key;h.id='now-'+c.key;section.setAttribute('aria-labelledby',h.id);head.append(h,count);section.append(head,list,empty);$('now-board').append(section);
  return {...c,section,count,list,emptyNode:empty};
});
const doneColumn=columns[3], more=el('button','plain-button now-more');doneColumn.section.append(more);
more.onclick=()=>{state.allFinished=!state.allFinished;render();};
const nowCards=new Map();
const column=t=>t.needs_you?'attention':t.progress==='working'?'working':t.progress==='waiting'?'waiting':'done';
const firstLine=text=>(text || '').split('\n').find(l=>l.trim())?.trim() || '';
function stamp(n,at) { if(n.dataset.at!==at){n.dataset.at=at;n.dateTime=at;} setText(n,at?age(at):''); }
function makeNowCard(t) {
  const button=el('button','task-card now-card'),top=el('span','now-top'),st=el('span','status'),when=el('time','now-age'),title=el('span','task-title'),own=el('span','now-owners'),question=el('span','now-question'),waits=el('span','now-waits'),note=el('span','task-note');
  top.append(st,when);button.append(top,title,own,question,waits,note);button.dataset.openTask=t.id;
  button.addEventListener('click',()=>openTask(t.id,button));
  return {button,st,when,title,own,question,waits,note};
}
function patchNowCard(c,t,byID) {
  const names=owners(t), blockers=model.dependencies.filter(e=>e.dependent_id===t.id).map(e=>byID.get(e.prerequisite_id)).filter(p=>p&&!done(p)).map(p=>p.title);
  status(c.st,t);stamp(c.when,t.updated_at);setText(c.title,t.title);
  const ownKey=names.join('\n');if(c.own.dataset.key!==ownKey){c.own.dataset.key=ownKey;c.own.replaceChildren(iconNode('agent'),el('span','owner-name',names.join(', ') || 'Unassigned'));}
  setText(c.question,t.question_preview || '');c.question.hidden=!t.needs_you||!t.question_preview;
  setText(c.waits,blockers.length?'Waits for: '+blockers.join(', '):'');c.waits.hidden=t.progress!=='waiting'||!blockers.length;
  const report=firstLine(t.report_preview);setText(c.note,report || 'No report yet.');
  c.button.classList.toggle('selected',state.selected===t.id);
  c.button.setAttribute('aria-label',`${t.title}. ${labels[t.progress]}.${t.needs_you?' Needs you: '+(t.question_preview || '')+'.':''} Assigned to ${names.join(' and ') || 'Unassigned'}.${c.waits.hidden?'':' '+c.waits.textContent+'.'} Changed ${age(t.updated_at)}.`);
}
// Moving a node keeps its focus where the browser supports moveBefore.
function place(list,node,ref) { if(node.nextSibling===ref&&node.parentNode===list)return; try{if(list.moveBefore&&node.isConnected&&node.ownerDocument===list.ownerDocument){list.moveBefore(node,ref);return;}}catch{} list.insertBefore(node,ref); }
function placeAll(list,nodes) { nodes.forEach((n,i)=>place(list,n,list.children[i] || null)); while(list.children.length>nodes.length)list.lastChild.remove(); }
function patchNow() {
  const focused=document.activeElement, byID=new Map(model.tasks.map(t=>[t.id,t])), newest=(a,b)=>a.updated_at<b.updated_at?1:a.updated_at>b.updated_at?-1:a.id<b.id?-1:1;
  for(const [id] of nowCards)if(!byID.has(id))nowCards.delete(id);
  for(const col of columns) {
    const all=model.tasks.filter(t=>column(t)===col.key).sort(newest), shown=all.filter(matches), capped=col===doneColumn&&!state.allFinished?shown.slice(0,10):shown;
    placeAll(col.list,capped.map(t=>{let c=nowCards.get(t.id);if(!c){c=makeNowCard(t);nowCards.set(t.id,c);}patchNowCard(c,t,byID);return c.button;}));
    setText(col.count,shown.length===all.length?String(all.length):`${shown.length} of ${all.length}`);
    col.section.classList.toggle('has-items',all.length>0);
    setText(col.emptyNode,all.length?'No matching Tasks.':col.empty);col.emptyNode.hidden=capped.length>0;
    if(col===doneColumn){more.hidden=shown.length<=10;setText(more,state.allFinished?'Show the last 10':`Show archive (${shown.length})`);more.setAttribute('aria-expanded',String(state.allFinished));}
  }
  patchFeed(byID);
  if(focused&&focused!==document.activeElement&&focused.isConnected)focused.focus({preventScroll:true});
}
// The feed keeps the changes this page has seen: each Task's last action at its updated_at, its report time,
// and differences between polls. A change that is not a Task's latest one shows only while the page watched it.
const events=new Map();let feedModel=null;const feedRows=new Map();
const actionKinds={'task.create':'create','task.status':'status','task.reopen':'status','task.report':'report','task.attention.set':'ask','task.attention.resolve':'resolve','task.assign':'assign','task.unassign':'assign','task.reference.add':'refs','task.reference.remove':'refs'};
function addEvent(t,kind,at) {
  if(!at)return;const key=`${t.id}|${kind}|${at}`;if(events.has(key))return;
  events.set(key,{key,task:t.id,kind,at,progress:t.progress,text:kind==='report'?firstLine(t.report_preview):kind==='ask'?t.question_preview || '':''});
}
function collectEvents() {
  const before=feedModel?new Map(feedModel.tasks.map(t=>[t.id,t])):null;feedModel=model;
  for(const t of model.tasks) {
    const kind=actionKinds[t.last_action] || 'edit';
    if(kind!=='report')addEvent(t,kind,t.updated_at); // a report is keyed by report_at, a moment before updated_at
    addEvent(t,'report',t.report_at);
    const p=before?.get(t.id);if(!p)continue;
    if(p.progress!==t.progress)addEvent(t,'status',t.updated_at);
    if(p.needs_you!==t.needs_you)addEvent(t,t.needs_you?'ask':'resolve',t.updated_at);
  }
  const ids=new Set(model.tasks.map(t=>t.id));for(const [k,e] of events)if(!ids.has(e.task))events.delete(k);
  if(events.size>300)[...events.values()].sort((a,b)=>a.at<b.at?-1:1).slice(0,events.size-300).forEach(e=>events.delete(e.key));
}
function eventText(e) {
  return {status:`changed to ${labels[e.progress] || e.progress}`,create:'was added',report:e.text?'reported: '+e.text:'reported',ask:e.text?'needs you: '+e.text:'needs you',resolve:'Needs you resolved',assign:'Agents changed',refs:'file references changed',edit:'was edited'}[e.kind];
}
function patchFeed(byID) {
  if(feedModel!==model)collectEvents();
  const list=$('feed-list'), shown=[...events.values()].filter(e=>byID.has(e.task)&&matches(byID.get(e.task))).sort((a,b)=>a.at<b.at?1:a.at>b.at?-1:a.key<b.key?-1:1).slice(0,12);
  for(const k of feedRows.keys())if(!events.has(k))feedRows.delete(k);
  placeAll(list,shown.map(e=>{
    let r=feedRows.get(e.key),t=byID.get(e.task);
    if(!r){const li=el('li'),b=el('button','feed-item'),mark=el('span','feed-mark'),text=el('span','feed-text'),title=el('span','feed-title'),what=el('span'),when=el('time','now-age');
      mark.append(iconNode(e.kind==='status'?(e.progress==='cancelled'?'idle':e.progress):e.kind==='ask'?'attention':e.kind==='resolve'?'finished':'info'));mark.className='feed-mark '+(e.kind==='status'?e.progress:e.kind);
      text.append(title,' ',what);b.append(mark,text,when);li.append(b);b.onclick=()=>openTask(e.task,b);r={li,b,title,what,when};feedRows.set(e.key,r);}
    setText(r.title,t.title);setText(r.what,eventText(e));stamp(r.when,e.at);r.b.setAttribute('aria-label',`${t.title} ${eventText(e)}, ${age(e.at)}`);
    return r.li;
  }));
  $('feed-empty').hidden=shown.length>0;
}
function updateNowAges() { for(const n of nowPanel.querySelectorAll('time[data-at]'))setText(n,age(n.dataset.at)); }
function render() {
  document.documentElement.dataset.theme=state.theme;
  document.querySelectorAll('.theme-button').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.theme===state.theme)));
  document.querySelectorAll('[data-view]').forEach(b=>{const active=b.dataset.view===state.view;b.setAttribute('aria-selected',String(active));b.tabIndex=active?0:-1;});
  panel.hidden=state.view!=='graph';agentsPanel.hidden=state.view!=='agents';nowPanel.hidden=state.view!=='now';
  for(const id of ['zoom-controls','archived'])$(id).hidden=state.view!=='graph';
  if(!model)return;
  setText(document.querySelector('.workspace-title h1'),model.snapshot.session.name);
  setText($('project-name'),model.snapshot.workspace_name || '');$('project-name').hidden=!model.snapshot.workspace_name; // the Workspace folder name, never its full path
  setText(document.querySelector('.workspace-subtitle'),`${plural(model.agents.length,'agent')} · ${plural(model.tasks.length,'task')}${model.snapshot.session.closed?' · Closed Session':''}`);
  const count=model.tasks.filter(matches).length, attention=model.tasks.filter(t=>t.needs_you);
  $('attention').replaceChildren(iconNode('attention'),document.createTextNode(`${attention.length} needs you`));$('attention').disabled=!attention.length;
  setText($('result-count'),state.query||state.filter!=='all'?`${count} of ${model.tasks.length} tasks`:'');
  patchGraph();patchAgents();patchNow();
  // In the graph, all matching Tasks can be archived and hidden.
  const onlyHidden=state.view==='graph'&&count>0&&!graphTasks.some(matches);
  $('empty').hidden=(count>0&&!onlyHidden) || (state.view==='agents'&&!model.tasks.length&&model.agents.length>0);
  setText($('empty').querySelector('h2'),onlyHidden?'Only archived Tasks match':model.tasks.length?'No matching work':'No Tasks yet');
  setText($('empty').querySelector('p'),onlyHidden?'Turn on Archived to see them in the graph.':model.tasks.length?'Try another agent name, task, or status.':'Tasks will appear here when the Orchestrator adds them.');
  $('clear-filters').hidden=onlyHidden&&!state.query&&state.filter==='all';
  graph.parentElement.style.visibility=count&&!onlyHidden?'visible':'hidden';
  nowPanel.firstElementChild.style.visibility=count?'visible':'hidden';
  updateHint();
  if(state.selected){if(model.tasks.some(t=>t.id===state.selected))loadDetail();else $('task-dialog').close();}
  if($('attention-dialog').open)renderAttention();
  updateSource();
}
function age(timestamp) {const seconds=Math.max(0,Math.floor((Date.now()-new Date(timestamp).getTime())/1000));return seconds<60?`${seconds}s ago`:seconds<3600?`${Math.floor(seconds/60)}m ago`:seconds<86400?`${Math.floor(seconds/3600)}h ago`:`${Math.floor(seconds/86400)}d ago`;}
function runtimeText(a) { return `Runtime: ${a.runtime_attached?(a.runtime_state || 'unknown'):'unbound'} · unverified${a.observed_at?' · '+age(a.observed_at):''}`; }
function updateRuntimeAges() { if(model)for(const a of model.agents){const r=rows.get(a.id);if(r)setText(r.runtime,runtimeText(a));} }
function plural(n,word) { return `${n} ${word}${n===1?'':'s'}`; }
// After a restart on a new port, polling cannot recover; say what to do.
function lost() { return stale&&lastSuccess>0&&Date.now()-lastSuccess>30000; }
function updateHint() {
  const hint=$('footer-hint');hint.classList.toggle('lost',lost());
  setText(hint,lost()?'Service unreachable. If you restarted it, run a-stack again.':state.view==='graph'?'Drag to pan. Select a task to see its neighbours.':'Select a task for its report and dependencies.');
}
function updateSource(message) {
  const service=version?'Local service '+version:'Local service';
  const text=message || (lastSuccess?`${stale?'Saved · stale':service} · checked ${age(new Date(lastSuccess).toISOString())}`:service+' · connecting');
  updateHint();
  setText(document.querySelector('.preview-label'),text);setText(document.querySelector('.preview-short'),stale?'Saved · stale':'Read-only');
  setText($('source-info'),`${text}${model?'. Snapshot captured '+model.snapshot.captured_at+' · revision '+model.snapshot.session.read_revision+'. Runtime observations have their own age and are unverified.':''}`);
  $('about-button').setAttribute('aria-label',text+'. About this read-only view');
}
function applyZoom() {graph.style.transform=`scale(${state.zoom})`;graph.parentElement.style.width=width*state.zoom+'px';graph.parentElement.style.height=height*state.zoom+'px';setText($('zoom-reset'),(state.zoom<.1?(state.zoom*100).toFixed(1):Math.round(state.zoom*100))+'%');}
function setZoom(value) {
  const old=state.zoom;state.zoom=Math.max(.0001,Math.min(1.5,value));
  const x=(panel.scrollLeft+panel.clientWidth/2)/old,y=(panel.scrollTop+panel.clientHeight/2)/old;applyZoom();panel.scrollLeft=x*state.zoom-panel.clientWidth/2;panel.scrollTop=y*state.zoom-panel.clientHeight/2;
}
for(const b of document.querySelectorAll('[data-view]')) {
  b.addEventListener('click',()=>{state.view=b.dataset.view;save('a-stack-view',state.view);render();});
  b.addEventListener('keydown',e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();const i=views.indexOf(state.view),v=e.key==='Home'?views[0]:e.key==='End'?views.at(-1):views[(i+(e.key==='ArrowRight'?1:views.length-1))%views.length];$(v+'-tab').click();$(v+'-tab').focus();});
}
for(const b of document.querySelectorAll('.theme-button'))b.addEventListener('click',()=>{state.theme=b.dataset.theme;save('a-stack-theme',state.theme);render();});
$('search').addEventListener('input',e=>{state.query=e.target.value;render();});
$('search').addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();const root={now:nowPanel,graph,agents:agentsPanel}[state.view];const c=[...root.querySelectorAll('.task-card')].find(b=>!b.closest('[hidden]')&&!b.closest('[inert]'));if(c)c.focus();}});
$('status-filter').addEventListener('change',e=>{state.filter=e.target.value;render();});
$('clear-filters').addEventListener('click',()=>{state.query='';state.filter='all';$('search').value='';$('status-filter').value='all';render();$('search').focus();});
$('zoom-out').onclick=()=>setZoom(state.zoom/1.2);$('zoom-in').onclick=()=>setZoom(state.zoom*1.2);$('zoom-reset').onclick=()=>setZoom(1);
$('zoom-fit').onclick=()=>{setZoom(Math.min((panel.clientWidth-40)/width,(panel.clientHeight-40)/height,1));panel.scrollTo((panel.scrollWidth-panel.clientWidth)/2,(panel.scrollHeight-panel.clientHeight)/2);};
// A drag pans from anywhere, also from a card. It starts after 4px, so a plain click still opens the Task.
let drag, dragged=false;
panel.addEventListener('pointerdown',e=>{if(e.button!==0||e.target.closest('button:not(.task-card)'))return;dragged=false;drag={x:e.clientX,y:e.clientY,left:panel.scrollLeft,top:panel.scrollTop,id:e.pointerId,moving:false};});
panel.addEventListener('pointermove',e=>{if(!drag)return;
  if(!drag.moving){if(Math.hypot(e.clientX-drag.x,e.clientY-drag.y)<4)return;drag.moving=true;panel.setPointerCapture(drag.id);panel.classList.add('panning');}
  panel.scrollLeft=drag.left+drag.x-e.clientX;panel.scrollTop=drag.top+drag.y-e.clientY;});
const endDrag=()=>{drag=null;panel.classList.remove('panning');};
// A click on empty space, without a drag, clears the focus.
panel.addEventListener('pointerup',e=>{if(drag){dragged=drag.moving;if(!dragged&&!e.target.closest('.task-card')&&state.focus)setFocus(null);}endDrag();});panel.addEventListener('pointercancel',endDrag);
panel.addEventListener('click',e=>{if(dragged){e.stopPropagation();e.preventDefault();}dragged=false;},true);
$('about-button').onclick=()=>{$('about-dialog').showModal();};
document.querySelectorAll('[data-close]').forEach(b=>b.onclick=()=>$(b.dataset.close).close());
const attentionChoices = new Map();
function renderAttention() {
  const list=$('attention-list'), tasks=model.tasks.filter(t=>t.needs_you), ids=new Set(tasks.map(t=>t.id));
  const focused=document.activeElement, oldOrder=[...list.children], oldIndex=oldOrder.indexOf(focused);
  for(const [id,b] of attentionChoices)if(!ids.has(id)){b.remove();attentionChoices.delete(id);}
  for(let i=0;i<tasks.length;i++) {
    const t=tasks[i];let b=attentionChoices.get(t.id);
    if(!b){b=el('button','dependency');b.dataset.attentionTask=t.id;b.onclick=()=>{$('attention-dialog').close();openTask(t.id,$('attention'));};attentionChoices.set(t.id,b);list.insertBefore(b,list.children[i] || null);}
    setText(b,t.title);
  }
  if(!tasks.length && $('attention-dialog').open){$('attention-dialog').close();$(state.view+'-tab').focus();}
  else if(oldIndex>=0&&!focused.isConnected)list.children[Math.min(oldIndex,tasks.length-1)]?.focus();
}
$('attention').onclick=()=>{const tasks=model.tasks.filter(t=>t.needs_you);if(tasks.length===1)openTask(tasks[0].id,$('attention'));else{renderAttention();$('attention-dialog').showModal();}};
const dialog=$('task-dialog');
const top=el('div','detail-top'),kicker=el('span','detail-kicker','TASK DETAILS'),close=el('button','icon-button');close.setAttribute('aria-label','Close task details');close.append(iconNode('close'));close.onclick=()=>dialog.close();top.append(kicker,close);
const body=el('div','detail-body'),detailTitle=el('h2'),detailStatus=el('div','status'),detailLoading=el('p','detail-footnote'),detailFields={};detailTitle.id='detail-title';body.append(detailTitle,detailStatus,detailLoading);
for(const [key,label]of Object.entries({ticket:'Ticket reference',description:'Description',report:'Latest report',reason:'Reason',result:'Result',question:'Needs you',owners:'Assigned agents',dependencies:'Dependencies',references:'File references',source:'Source'})){
  const section=el('section','detail-section'),h=el('h3','',label),content=el('div','detail-text');section.append(h,content);body.append(section);detailFields[key]=content;
}
dialog.append(top,body);
dialog.addEventListener('close',()=>{detailEpoch++;state.selected=null;detailKey='';render();if(returnFocus?.isConnected&&!returnFocus.closest('[inert]')&&!returnFocus.closest('[hidden]'))returnFocus.focus();else $(state.view+'-tab').focus();});
function setFocus(id) {state.focus=id;render();}
// Escape clears the focus once no dialog is open; an open dialog takes the first Escape.
document.addEventListener('keydown',e=>{if(e.key==='Escape'&&state.focus&&state.view==='graph'&&!document.querySelector('dialog[open]')){e.preventDefault();setFocus(null);}});
$('archived').addEventListener('click',()=>{state.archived=!state.archived;save('a-stack-archived',state.archived?'1':'0');render();});
function openTask(id,focus) {if(state.view==='graph')state.focus=id;if(state.selected!==id){for(const name of ['description','report','reason','result','question','references','source'])detailFields[name].replaceChildren();delete detailFields.references.dataset.key;}returnFocus=focus || returnFocus;state.selected=id;detailKey='';detailEpoch++;render();if(!dialog.open){dialog.showModal();close.focus();}}
function patchDetailMetadata(t) {
  setText(detailTitle,t.title);status(detailStatus,t);setText(detailFields.ticket,t.ticket_ref || 'None.');setText(detailFields.owners,owners(t).join(', ') || 'Unassigned');
  const dep=detailFields.dependencies;
  const signature=JSON.stringify(model.dependencies.filter(e=>e.dependent_id===t.id||e.prerequisite_id===t.id).map(e=>{const other=model.tasks.find(x=>x.id===(e.dependent_id===t.id?e.prerequisite_id:e.dependent_id));return [e.id,other?.title,other?.progress];}));
  if(dep.dataset.key===t.id+signature)return;dep.dataset.key=t.id+signature;dep.replaceChildren();
  for(const e of model.dependencies)if(e.dependent_id===t.id||e.prerequisite_id===t.id){const prior=e.dependent_id===t.id,other=model.tasks.find(x=>x.id===(prior?e.prerequisite_id:e.dependent_id));if(!other)continue;const b=el('button','dependency',`${prior?'Requires':'Required by'}: ${other.title} · ${labels[other.progress]}${done(other)?' · archived':''}`);b.onclick=()=>openTask(other.id,returnFocus);dep.append(b);}
  if(!dep.childNodes.length)setText(dep,'No dependencies.');
}
async function loadDetail() {
  const t=model.tasks.find(t=>t.id===state.selected);if(!t)return;
  patchDetailMetadata(t);
  const key=t.id+':'+t.revision+':'+t.reference_count;
  if(detailKey===key)return;detailKey=key;const epoch=++detailEpoch,revision=model.snapshot.session.read_revision;
  setText(detailLoading,'Loading current details…');
  try{
    const [detail,refs]=await Promise.all([request('/v1/task/show',{task_id:t.id,read_revision:revision}),pages('references',revision,t.id)]);
    if(epoch!==detailEpoch||state.selected!==t.id)return;
    for(const name of ['description','report','reason','result'])setText(detailFields[name],detail.data[name] || 'None.');
    const q=detail.data.question;setText(detailFields.question,q?`${q.question}\nAsked by ${model.agents.find(a=>a.id===q.asker_id)?.name || q.asker_id} at ${q.asked_at}${q.note?'\n'+q.note:''}`:'No open request.');
    const referencesKey=JSON.stringify(refs);
    if(detailFields.references.dataset.key!==t.id+referencesKey){detailFields.references.dataset.key=t.id+referencesKey;detailFields.references.replaceChildren(...refs.map(ref=>{const row=el('div','reference'),text=el('code','',ref.location+(ref.revision?' @ '+ref.revision:'')),copy=el('button','plain-button','Copy');copy.setAttribute('aria-label','Copy reference '+ref.location);copy.onclick=async()=>{try{await navigator.clipboard.writeText(ref.location);copy.textContent='Copied';}catch{copy.textContent='Select text to copy';}};row.append(text,copy);return row;}));
    if(!refs.length)setText(detailFields.references,'No file references.');}
    setText(detailFields.source,`Local service · read revision ${revision}. Report received ${t.report_at || 'never'}.`);setText(detailLoading,'Read-only · viewing does not resolve requests.');
  }catch{if(epoch===detailEpoch){detailKey='';setText(detailLoading,'Details unavailable · saved content may be stale. Retrying with the next refresh.');}}
}
async function request(endpoint,payload,headers={},signal) {
  const response=await fetch(endpoint,{method:'POST',headers:{'Content-Type':'application/json','Authorization':'Bearer '+token,'X-A-Stack-Instance':instance,...headers},body:JSON.stringify({...context,...payload}),credentials:'omit',cache:'no-store',redirect:'error',signal});
  if(response.status===304)return {unchanged:true,etag:response.headers.get('ETag')};
  if(!response.ok){const error=new Error('Read unavailable');error.status=response.status;throw error;}
  const result=await response.json();if(!result.data)throw new Error('Invalid read response');return {...result,etag:response.headers.get('ETag')};
}
async function pages(kind,revision,task_id,signal) {
  const items=[];let after='';
  do{const r=await request('/v1/session/page',{kind,read_revision:revision,limit:100,after,...(task_id?{task_id}:{})},{},signal);if(r.data.read_revision!==revision)throw new Error('Read revision changed');items.push(...r.data.items);after=r.data.next || '';}while(after);
  return items;
}
async function refresh() {
  if(document.hidden||busy||!token)return;busy=true;controller=new AbortController();
  try{
    for(let attempt=0;attempt<3;attempt++) {
      try{
        const manifest=await request('/v1/session/snapshot',{known_revision:model?.snapshot.session.read_revision || 0},etag?{'If-None-Match':etag}:{},controller.signal);
        if(!manifest.unchanged&&!manifest.data.unchanged){
          const revision=manifest.data.session.read_revision;
          const [tasks,agents,assignments,dependencies]=await Promise.all(['tasks','agents','assignments','dependencies'].map(k=>pages(k,revision,null,controller.signal)));
          for(const [key,items]of Object.entries({tasks,agents,assignments,dependencies}))if(items.length!==manifest.data.counts[key])throw new Error('Incomplete collection');
          model={snapshot:manifest.data,tasks:tasks.sort((a,b)=>a.id<b.id?-1:a.id>b.id?1:0),agents:agents.sort((a,b)=>a.name<b.name?-1:a.name>b.name?1:0),assignments,dependencies:dependencies.sort((a,b)=>a.id<b.id?-1:a.id>b.id?1:0)};etag=manifest.etag || '';render();
        }
        lastSuccess=Date.now();stale=false;failures=0;updateSource();if(state.selected)loadDetail();break;
      }catch(error){
        if(error.status===409&&attempt<2){const shell=await fetch(location.pathname,{cache:'no-store',credentials:'omit',redirect:'error',signal:controller.signal});if(shell.ok){instance=shell.headers.get('X-A-Stack-Instance') || instance;version=shell.headers.get('X-A-Stack-Version') || version;}continue;}throw error;
      }
    }
  }catch(error){if(error.name!=='AbortError'){stale=true;failures++;updateSource(model?undefined:error.status===401||error.status===403?'Read credential unavailable · run a-stack open again':'Local service unavailable · retrying');}}
  finally{busy=false;controller=null;schedule();}
}
function schedule(){clearTimeout(timer);if(!document.hidden&&token)timer=setTimeout(refresh,Math.min(30000,2000*2**Math.min(failures,4)));}
document.addEventListener('visibilitychange',()=>{clearTimeout(timer);if(document.hidden)controller?.abort();else refresh();});
setInterval(()=>{if(!document.hidden&&model){updateSource();updateRuntimeAges();updateNowAges();}},1000);
render();
if(token)refresh();else{updateSource('Read credential missing · run a-stack open');$('attention').disabled=true;}

})();
