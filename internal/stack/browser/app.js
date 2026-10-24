"use strict";
(() => {
const icons = {
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
const graph = $('graph-world'), panel = $('graph-panel'), agentsPanel = $('agents-panel');
const svgNS = 'http://www.w3.org/2000/svg';
const state = {view:'graph',theme:'light',query:'',filter:'all',zoom:1,selected:null};
let model = null, etag = '', instance = document.querySelector('meta[name="a-stack-instance"]').content, version = document.querySelector('meta[name="a-stack-version"]').content;
let timer, controller, failures = 0, busy = false, lastSuccess = 0, stale = false, detailKey = '', detailEpoch = 0, returnFocus;
let geometryKey = '', width = 1400, height = 670, laneKey = p => p;
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
try {
  token = readToken();
  state.theme = sessionStorage.getItem('a-stack-theme') === 'dark' ? 'dark' : 'light';
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
  c.article.classList.toggle('dimmed',graphCard&&!visible);
  c.article.inert=graphCard&&!visible;
  if (graphCard) c.article.setAttribute('aria-hidden',String(!visible));
  else c.article.hidden=!visible;
}
const edgesSVG = document.createElementNS(svgNS,'svg');edgesSVG.classList.add('connections');edgesSVG.setAttribute('aria-hidden','true');
edgesSVG.innerHTML='<defs><marker id="arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="5" markerHeight="5" orient="auto"><path d="M1 1l5 3-5 3" fill="none" stroke="var(--edge)" stroke-width="1.5"/></marker></defs><g></g>';
graph.append(edgesSVG);
function layout() {
  const wasHidden=panel.hidden; if(wasHidden){panel.hidden=false;panel.style.visibility='hidden';}
  const tasks=model.tasks, byID=new Map(tasks.map(t=>[t.id,t])), rank=new Map(tasks.map(t=>[t.id,0]));
  const degree=new Map(tasks.map(t=>[t.id,0])), next=new Map(tasks.map(t=>[t.id,[]])), prev=new Map(tasks.map(t=>[t.id,[]]));
  for(const e of model.dependencies) if(byID.has(e.prerequisite_id)&&byID.has(e.dependent_id)) {degree.set(e.dependent_id,degree.get(e.dependent_id)+1);next.get(e.prerequisite_id).push(e.dependent_id);prev.get(e.dependent_id).push(e.prerequisite_id);}
  // Lines into a Task with several prerequisites share a lane and track, so they merge; other lines share them per prerequisite, so they branch.
  laneKey=(p,d)=>prev.get(d).length>1?d:p;
  const queue=tasks.filter(t=>!degree.get(t.id)).map(t=>t.id).sort();
  for(let i=0;i<queue.length;i++) for(const id of next.get(queue[i])) {
    rank.set(id,Math.max(rank.get(id),rank.get(queue[i])+1));degree.set(id,degree.get(id)-1);if(!degree.get(id))queue.push(id);
  }
  // Unconnected groups of Tasks are laid out side by side, largest first; ties keep the stable Task order.
  const root=new Map(tasks.map(t=>[t.id,t.id])), find=id=>{let r=id;while(root.get(r)!==r)r=root.get(r);for(let n=id;n!==r;){const up=root.get(n);root.set(n,r);n=up;}return r;};
  for(const [id,ns] of next) for(const n of ns) root.set(find(n),find(id));
  const groups=new Map();for(const t of tasks){const r=find(t.id);if(!groups.has(r))groups.set(r,[]);groups.get(r).push(t.id);}
  const sizes=new Map(tasks.map(t=>[t.id,cards.get(t.id).article.offsetHeight]));
  // Columns wrap above the panel height, or above the height that gives all cards the panel's aspect ratio.
  const aspect=(panel.clientWidth||1400)/(panel.clientHeight||670), limit=Math.max((panel.clientHeight||670)-88,Math.sqrt(tasks.reduce((s,t)=>s+342*(sizes.get(t.id)+32),0)/aspect));
  const blocks=[...groups.values()].sort((a,b)=>b.length-a.length).map(ids=>{
    const layers=[], order=new Map(), place=l=>l.forEach((id,i)=>order.set(id,(i+.5)/l.length));
    for(const id of ids)(layers[rank.get(id)]??=[]).push(id);
    const cols=layers.filter(Boolean);cols.forEach(place);
    // Barycenter sweeps keep each Task near the Tasks it connects to, so lines pass fewer unrelated cards.
    const sweep=(l,near)=>{const c=new Map(l.map(id=>{const n=near.get(id);return [id,n.length?n.reduce((s,x)=>s+order.get(x),0)/n.length:order.get(id)];}));l.sort((a,b)=>c.get(a)-c.get(b));place(l);};
    for(let i=1;i<cols.length;i++)sweep(cols[i],prev);for(let i=cols.length-2;i>=0;i--)sweep(cols[i],next);for(let i=1;i<cols.length;i++)sweep(cols[i],prev);
    let x=0,h=0,extra=0;
    const block={cols:cols.map((ids,i)=>{
      // A column much taller than the limit wraps into closer sub-columns with shared row heights, so lines can run in the gaps between rows.
      // Lines out of a row run in the upper part of the gap below it and lines into a row in the lower part of the gap above it,
      // each lane key on its own track 12px apart; a gap grows only as far as its tracks need.
      const total=ids.reduce((s,id)=>s+sizes.get(id)+32,0)-32, n=Math.ceil(ids.length/Math.min(ids.length,total>limit*1.5?Math.ceil(total/limit):1)), heights=[];
      ids.forEach((id,j)=>heights[j%n]=Math.max(heights[j%n]||0,sizes.get(id)));
      const k=Math.ceil(ids.length/n), two=k>1&&ids.some(id=>prev.get(id).length)&&ids.some(id=>next.get(id).length);
      const outs=heights.map(()=>[]), ins=heights.map(()=>[]), add=(l,v)=>{if(!l.includes(v))l.push(v);};
      ids.forEach((id,j)=>{if(j<n*(k-1))for(const d of next.get(id))add(outs[j%n],laneKey(id,d));if(j>=n)for(const p of prev.get(id))add(ins[j%n],laneKey(p,id));});
      const gaps=heights.slice(1).map((_,r)=>{const o=outs[r].length,q=ins[r+1].length;return Math.max(32,(o&&q?16:8)+12*(o+q));});
      extra=Math.max(extra,12*(ins[0].length-1),12*(outs[n-1].length-1));
      const col={i,ids,x,k,two,outs,ins,gaps,w:k*314-40,heights,h:heights.reduce((s,r)=>s+r,0)+gaps.reduce((s,g)=>s+g,0)};x+=col.w+68;h=Math.max(h,col.h);return col;
    })};
    // Lines that skip a column run over or under the group, each source on its own track, clear of the row tracks.
    const skips=ids.filter(id=>next.get(id).some(n=>rank.get(n)>rank.get(id)+1)).length;
    block.extra=extra;block.pad=(skips?46+8*skips:0)+extra;block.w=x-68;block.h=h+2*block.pad;return block;
  });
  const maxWidth=Math.max(limit*aspect,(panel.clientWidth||1400)-88,...blocks.map(b=>b.w));let bx=28,by=24,shelf=0;width=56;
  positions.clear();
  for(const b of blocks) {
    if(bx>28&&bx+b.w>28+maxWidth){bx=28;by+=shelf+56;shelf=0;}
    b.x=bx;b.y=by;bx+=b.w+180;shelf=Math.max(shelf,b.h);width=Math.max(width,b.x+b.w+28); // groups sit well apart from columns, so they do not read as next steps
    for(const col of b.cols) {
      col.x+=b.x;col.block=b;col.prev=b.cols[col.i-1];let y=b.y+(b.h-col.h)/2;const tops=col.heights.map((r,i)=>{const t=y;y+=r+(col.gaps[i]||0);return t;}), n=tops.length;
      col.ids.forEach((id,j)=>{const r=j%n,p={x:col.x+Math.floor(j/n)*314,y:tops[r],r,c:Math.floor(j/n),top:tops[r],bottom:tops[r]+col.heights[r],col};positions.set(id,p);const a=cards.get(id).article;a.style.left=p.x+'px';a.style.top=p.y+'px';});
    }
  }
  height=by+shelf+24; // tight around the Task units, so Fit fits the cards
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
  const group=edgesSVG.querySelector('g'), frag=document.createDocumentFragment(), edges=[], lanes=new Map();
  // Each gutter gives every lane key its own vertical lane.
  const use=(col,id)=>{const l=lanes.get(col) || [];lanes.set(col,l);if(!l.includes(id))l.push(id);};
  const lane=(col,id)=>{const l=lanes.get(col);return col.x+col.w+68*(l.indexOf(id)+1)/(l.length+1);}, tracks=new Map();
  for(const e of model.dependencies) {
    const ap=positions.get(e.prerequisite_id),bp=positions.get(e.dependent_id);
    if(!ap||!bp||!cards.has(e.prerequisite_id)||!cards.has(e.dependent_id)||ap.col.block!==bp.col.block||bp.col.i<=ap.col.i)continue;
    edges.push([e,ap,bp,laneKey(e.prerequisite_id,e.dependent_id)]);
  }
  // Lines that skip a column take the lanes nearest their source column, so they turn away before passing other arrows.
  for(const [,ap,bp,k] of edges)if(bp.col.i!==ap.col.i+1)use(ap.col,k);
  for(const [,ap,bp,k] of edges){use(ap.col,k);use(bp.col.prev,k);}
  for(const [e,ap,bp,k] of edges) {
    const a=cards.get(e.prerequisite_id),b=cards.get(e.dependent_id),g=ap.col.block;
    // Lines leave a wrapped column on their key's track below the row and enter one on their key's track above it. Where a column
    // has lines on both sides, they leave low on a card and enter high on its neighbour, in separate lanes, so they never meet.
    const out=ap.c<ap.col.k-1, inn=bp.c>0, x1=ap.x+274, x2=bp.x, from=lane(ap.col,k), to=lane(bp.col.prev,k);
    const y1=ap.y+a.button.offsetTop+(out&&ap.col.two?a.button.offsetHeight-14:a.button.offsetHeight/2), y2=bp.y+b.button.offsetTop+(inn&&bp.col.two?14:b.button.offsetHeight/2), points=[[x1,y1]];
    let y=y1;
    if(out){y=ap.bottom+10+12*ap.col.outs[ap.r].indexOf(k);points.push([x1+12,y1],[x1+12,y]);}
    points.push([from,y]);
    if(bp.col.i!==ap.col.i+1){ // over or under the group, not through other columns
      if(!tracks.has(e.prerequisite_id))tracks.set(e.prerequisite_id,[...tracks.keys()].filter(id=>positions.get(id).col.block===g).length);
      const t=46+g.extra+8*tracks.get(e.prerequisite_id), c=(y1+y2)/2<g.y+g.h/2?g.y+g.pad-t:g.y+g.h-g.pad+t;points.push([from,c],[to,c]);
    }
    if(inn){const t=bp.top-10-12*bp.col.ins[bp.r].indexOf(k);points.push([to,t],[x2-12,t],[x2-12,y2]);}else points.push([to,y2]);
    points.push([x2-3,y2]);
    const p=document.createElementNS(svgNS,'path');p.setAttribute('d',roundedPath(points));p.setAttribute('marker-end','url(#arrow)');p.classList.add('connection');
    if(e.prerequisite_id===state.selected||e.dependent_id===state.selected)p.classList.add('active');
    if(!matches(model.tasks.find(t=>t.id===e.prerequisite_id))&&!matches(model.tasks.find(t=>t.id===e.dependent_id)))p.classList.add('faded');
    frag.append(p);
  }
  group.replaceChildren(frag);
}
function patchGraph() {
  const ids=new Set(model.tasks.map(t=>t.id));
  for(const [id,c] of cards)if(!ids.has(id)){c.article.remove();cards.delete(id);}
  for(const t of model.tasks) {let c=cards.get(t.id);if(!c){c=makeCard(t);cards.set(t.id,c);graph.append(c.article);}patchCard(c,t,true);}
  const key=JSON.stringify([model.tasks.map(t=>[t.id,t.title,owners(t),t.needs_you,t.reference_count,t.cancelled_prerequisite_count]),model.dependencies.map(e=>[e.prerequisite_id,e.dependent_id])]);
  if(geometryKey!==key){geometryKey=key;performance.clearMarks('a-stack-layout-start');performance.clearMarks('a-stack-layout-end');performance.clearMeasures('a-stack-layout');performance.mark('a-stack-layout-start');layout();performance.mark('a-stack-layout-end');performance.measure('a-stack-layout','a-stack-layout-start','a-stack-layout-end');}else drawEdges();
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
function render() {
  document.documentElement.dataset.theme=state.theme;
  document.querySelectorAll('.theme-button').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.theme===state.theme)));
  document.querySelectorAll('[data-view]').forEach(b=>{const active=b.dataset.view===state.view;b.setAttribute('aria-selected',String(active));b.tabIndex=active?0:-1;});
  panel.hidden=state.view!=='graph';agentsPanel.hidden=state.view!=='agents';$('zoom-controls').hidden=state.view!=='graph';
  if(!model)return;
  setText(document.querySelector('.workspace-title h1'),model.snapshot.session.name);
  setText($('project-name'),model.snapshot.workspace_name || '');$('project-name').hidden=!model.snapshot.workspace_name; // the Workspace folder name, never its full path
  setText(document.querySelector('.workspace-subtitle'),`${plural(model.agents.length,'agent')} · ${plural(model.tasks.length,'task')}${model.snapshot.session.closed?' · Closed Session':''}`);
  const count=model.tasks.filter(matches).length, attention=model.tasks.filter(t=>t.needs_you);
  $('attention').replaceChildren(iconNode('attention'),document.createTextNode(`${attention.length} needs you`));$('attention').disabled=!attention.length;
  $('empty').hidden=count>0 || (state.view==='agents'&&!model.tasks.length&&model.agents.length>0);
  $('empty').querySelector('h2').textContent=model.tasks.length?'No matching work':'No Tasks yet';
  setText($('empty').querySelector('p'),model.tasks.length?'Try another agent name, task, or status.':'Tasks will appear here when the Orchestrator adds them.');
  setText($('result-count'),state.query||state.filter!=='all'?`${count} of ${model.tasks.length} tasks`:'');
  patchGraph();patchAgents();
  graph.parentElement.style.visibility=count?'visible':'hidden';
  updateHint();
  if(state.selected){if(model.tasks.some(t=>t.id===state.selected))loadDetail();else $('task-dialog').close();}
  if($('attention-dialog').open)renderAttention();
  updateSource();
}
function age(timestamp) {const seconds=Math.max(0,Math.floor((Date.now()-new Date(timestamp).getTime())/1000));return seconds<60?`${seconds}s ago`:`${Math.floor(seconds/60)}m ago`;}
function runtimeText(a) { return `Runtime: ${a.runtime_attached?(a.runtime_state || 'unknown'):'unbound'} · unverified${a.observed_at?' · '+age(a.observed_at):''}`; }
function updateRuntimeAges() { if(model)for(const a of model.agents){const r=rows.get(a.id);if(r)setText(r.runtime,runtimeText(a));} }
function plural(n,word) { return `${n} ${word}${n===1?'':'s'}`; }
// After a restart on a new port, polling cannot recover; say what to do.
function lost() { return stale&&lastSuccess>0&&Date.now()-lastSuccess>30000; }
function updateHint() {
  const hint=$('footer-hint');hint.classList.toggle('lost',lost());
  setText(hint,lost()?'Service unreachable. If you restarted it, run a-stack open again.':state.view==='graph'?'Arrows show what comes next. Drag empty space to pan.':'Select a task for its report and dependencies.');
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
  b.addEventListener('click',()=>{state.view=b.dataset.view;render();});
  b.addEventListener('keydown',e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();const v=e.key==='Home'?'graph':e.key==='End'?'agents':state.view==='graph'?'agents':'graph';$(v+'-tab').click();$(v+'-tab').focus();});
}
for(const b of document.querySelectorAll('.theme-button'))b.addEventListener('click',()=>{state.theme=b.dataset.theme;try{sessionStorage.setItem('a-stack-theme',state.theme);}catch{}render();});
$('search').addEventListener('input',e=>{state.query=e.target.value;render();});
$('search').addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();const root=state.view==='graph'?graph:agentsPanel;const c=[...root.querySelectorAll('.task-card')].find(b=>!b.closest('[hidden]')&&!b.closest('[inert]'));if(c)c.focus();}});
$('status-filter').addEventListener('change',e=>{state.filter=e.target.value;render();});
$('clear-filters').addEventListener('click',()=>{state.query='';state.filter='all';$('search').value='';$('status-filter').value='all';render();$('search').focus();});
$('zoom-out').onclick=()=>setZoom(state.zoom/1.2);$('zoom-in').onclick=()=>setZoom(state.zoom*1.2);$('zoom-reset').onclick=()=>setZoom(1);
$('zoom-fit').onclick=()=>{setZoom(Math.min((panel.clientWidth-40)/width,(panel.clientHeight-40)/height,1));panel.scrollTo((panel.scrollWidth-panel.clientWidth)/2,(panel.scrollHeight-panel.clientHeight)/2);};
let drag;
panel.addEventListener('pointerdown',e=>{if(e.button!==0||e.target.closest('button'))return;drag={x:e.clientX,y:e.clientY,left:panel.scrollLeft,top:panel.scrollTop};panel.setPointerCapture(e.pointerId);panel.classList.add('panning');});
panel.addEventListener('pointermove',e=>{if(drag){panel.scrollLeft=drag.left+drag.x-e.clientX;panel.scrollTop=drag.top+drag.y-e.clientY;}});
const endDrag=()=>{drag=null;panel.classList.remove('panning');};panel.addEventListener('pointerup',endDrag);panel.addEventListener('pointercancel',endDrag);
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
function openTask(id,focus) {if(state.selected!==id){for(const name of ['description','report','reason','result','question','references','source'])detailFields[name].replaceChildren();delete detailFields.references.dataset.key;}returnFocus=focus || returnFocus;state.selected=id;detailKey='';detailEpoch++;render();if(!dialog.open){dialog.showModal();close.focus();}}
function patchDetailMetadata(t) {
  setText(detailTitle,t.title);status(detailStatus,t);setText(detailFields.ticket,t.ticket_ref || 'None.');setText(detailFields.owners,owners(t).join(', ') || 'Unassigned');
  const dep=detailFields.dependencies;
  const signature=JSON.stringify(model.dependencies.filter(e=>e.dependent_id===t.id||e.prerequisite_id===t.id).map(e=>{const other=model.tasks.find(x=>x.id===(e.dependent_id===t.id?e.prerequisite_id:e.dependent_id));return [e.id,other?.title,other?.progress];}));
  if(dep.dataset.key===t.id+signature)return;dep.dataset.key=t.id+signature;dep.replaceChildren();
  for(const e of model.dependencies)if(e.dependent_id===t.id||e.prerequisite_id===t.id){const prior=e.dependent_id===t.id,other=model.tasks.find(x=>x.id===(prior?e.prerequisite_id:e.dependent_id));if(!other)continue;const b=el('button','dependency',`${prior?'Requires':'Required by'}: ${other.title} · ${labels[other.progress]}`);b.onclick=()=>openTask(other.id,returnFocus);dep.append(b);}
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
setInterval(()=>{if(!document.hidden&&model){updateSource();updateRuntimeAges();}},1000);
render();
if(token)refresh();else{updateSource('Read credential missing · run a-stack open');$('attention').disabled=true;}

})();
