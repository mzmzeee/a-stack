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
  chevron: '<path d="m9 6 6 6-6 6"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6m0-10v.2"/>',
  report: '<path d="M7 3h7l5 5v13H7Z"/><path d="M14 3v5h5M10 13h6m-6 4h6"/>',
  file: '<path d="m20 11-8.5 8.5a5 5 0 0 1-7-7L13 4a3.5 3.5 0 0 1 5 5l-8.5 8.5a2 2 0 0 1-3-3L14 7"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16Z"/>',
  system: '<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8m-4-4v4"/>',
  bell: '<path d="M6 16v-5a6 6 0 0 1 12 0v5l2 2H4Z"/><path d="M10 21a2 2 0 0 0 4 0"/>',
  mail: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/>',
  map: '<rect x="3" y="5" width="18" height="14" rx="2"/><rect x="12" y="11" width="6" height="5" rx="1"/>'
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
// The version and the asset files this page came with; after a restart the service may run others.
const pageVersion = version, pageAssets = document.querySelector('meta[name="a-stack-assets"]').content;
let timer, controller, failures = 0, busy = false, lastSuccess = 0, stale = false, answered = false, detailKey = '', detailEpoch = 0, returnFocus;
let geometryKey = '', width = 1400, height = 670;
const cards = new Map(), rows = new Map(), positions = new Map();
const path = location.pathname.split('/');
const context = {workspace_id:path[2],session_id:path[3]};
const storageKey = 'a-stack-view:' + location.pathname;
let token = '', notice = '';
const linkUsed = 'This link has expired or was already used. Run a-stack, or a-stack open, to open a new one.';
const accessEnded = 'This view lost access: its view token expired, was revoked, or is not in a restored database. Run a-stack to open the view again.';
// A view link carries a one-time code in its fragment. The page trades it for a
// view token and keeps that in the tab's session storage, so a reload works.
async function readToken() {
  const code=location.hash.slice(1);
  history.replaceState(null,'',location.pathname);
  let saved='';try{saved=sessionStorage.getItem(storageKey) || '';}catch{}
  if(!code)return saved;
  try{
    const response=await fetch('/v1/browser/redeem',{method:'POST',headers:{'Content-Type':'application/json','X-A-Stack-Instance':instance},body:JSON.stringify({...context,code}),credentials:'omit',cache:'no-store',redirect:'error'});
    if(!response.ok)throw new Error(response.status===401?linkUsed:'');
    const fresh=(await response.json()).data.token;
    try{sessionStorage.setItem(storageKey,fresh);}catch{}
    return fresh;
  }catch(error){if(!saved)notice=error.message || 'The local service did not answer. Run a-stack, or a-stack open, to open a new link.';return saved;}
}
// noAccess says on the page itself why the view cannot read.
function noAccess(text) {
  notice=text;
  if(!model)setText(document.querySelector('.workspace-title h1'),'No access');
  setText(document.querySelector('.workspace-subtitle'),text);
  render();
}
// A new link in the same tab replaces the view token and clears an old notice.
async function connect() {
  clearTimeout(timer);controller?.abort();failures=0;notice='';etag='';
  token=await readToken();
  if(token){if(model)render();refresh();}else{stale=true;$('attention').disabled=true;noAccess(notice || 'Open this view with a-stack, or a-stack open.');}
}
// The theme survives browser restarts. System, the default, follows the light or dark setting of the system.
function savedTheme() {
  try { const t=localStorage.getItem('a-stack-theme'); if (t==='dark'||t==='light') return t; } catch {}
  return 'system';
}
state.theme = savedTheme();
// The last tab and the Archived toggle are remembered per browser, like the theme.
function load(key) { try { return localStorage.getItem(key); } catch { return null; } }
function save(key,value) { try { localStorage.setItem(key,value); } catch {} }
if (views.includes(load('a-stack-view'))) state.view = load('a-stack-view');
state.archived = load('a-stack-archived') === '1';
window.addEventListener('hashchange',connect);
const labels = {waiting:'Waiting',working:'Working',finished:'Finished',cancelled:'Cancelled'};
function el(tag, cls, text) { const n = document.createElement(tag); if (cls) n.className=cls; if (text !== undefined) n.textContent=text; return n; }
function setText(n,text) { if (n.textContent !== String(text)) n.textContent=text; }
function iconNode(name) { const n=el('span'); n.innerHTML=icon(name); return n; } // only constant artwork
// Indexes of the current model, built once per snapshot: Agent names by Task, Task IDs by Agent,
// prerequisite edges by dependent Task, and Tasks by ID. A 2,000-Task Session made the linear scans quadratic.
let indexed=null, ownerNames=new Map(), agentTasks=new Map(), prerequisites=new Map(), taskByID=new Map();
function index() {
  if(indexed===model)return;indexed=model;ownerNames=new Map();agentTasks=new Map();prerequisites=new Map();taskByID=new Map(model.tasks.map(t=>[t.id,t]));
  const add=(map,key,value)=>{let list=map.get(key);if(!list)map.set(key,list=[]);list.push(value);};
  for(const a of model.assignments){add(ownerNames,a.task_id,a.agent_name);add(agentTasks,a.agent_id,a.task_id);}
  for(const names of ownerNames.values())names.sort();
  for(const e of model.dependencies)add(prerequisites,e.dependent_id,e);
}
function owners(t) { index();return ownerNames.get(t.id) || []; }
function matches(t) {
  return (state.filter==='all' || (state.filter==='blocked' ? t.needs_you : t.progress===state.filter)) &&
    [t.title,t.ticket_ref,t.report_preview,...owners(t)].join(' ').toLowerCase().includes(state.query.trim().toLowerCase());
}
function status(n,t) {
  const key=t.progress+':'+t.needs_you;
  if (n.dataset.key===key) return;
  n.dataset.key=key;n.className='status '+t.progress;
  n.replaceChildren(iconNode(t.progress==='cancelled'?'idle':t.progress), el('span','status-label',labels[t.progress] || t.progress));
  if (t.needs_you) n.append(el('span','sr-only',', '),el('span','status blocked','Needs you')); // the comma is for screen readers
}
// Every card has a left border in its status color, so status still reads at a small zoom; Needs you has a ring.
function statusMark(button,t) { const v=t.needs_you?'needs-you':t.progress;if(button.dataset.status!==v)button.dataset.status=v; }
// The number of messages in the Task's mail, as an envelope and a count; hidden without mail.
function mailCount(n,count) {
  const v=String(count || 0);n.hidden=!count;if(n.dataset.count===v)return;
  n.dataset.count=v;n.replaceChildren(...(count?[iconNode('mail'),document.createTextNode(v)]:[]));
}
const mailText=t=>t.mail_count?` ${plural(t.mail_count,'Task mail message')}.`:'';
// Screen reader labels: a sentence ends with one mark, so a question keeps its own; a list reads "A, B and C".
const sentence=s=>/[.?!…]$/.test(s)?s:s+'.';
const listing=a=>a.length<3?a.join(' and '):a.slice(0,-1).join(', ')+' and '+a.at(-1);
const asked=t=>t.needs_you?' '+sentence(t.question_preview?'Needs you: '+t.question_preview:'Needs you'):'';
function makeCard(t) {
  const article=el('article','task-unit');article.dataset.task=t.id;
  const own=el('div','owners'), button=el('button','task-card');button.dataset.openTask=t.id;
  const top=el('span','card-top'),st=el('span','status'),mail=el('span','mail-count'),title=el('span','task-title'),note=el('span','task-note'),foot=el('span','task-caption');
  top.append(st,mail);button.append(top,title,note,foot);article.append(own,button);
  button.addEventListener('click',()=>openTask(t.id,button));
  return {article,own,button,st,mail,title,note,foot};
}
// A card in the graph. Only the graph uses these cards; Now and Agents have their own.
function patchCard(c,t) {
  const names=owners(t), ownKey=names.join('\n');
  if(c.own.dataset.key!==ownKey) {
    c.own.dataset.key=ownKey;c.own.replaceChildren(...(names.length?names:['Unassigned']).map(name=>{
      const n=el('span','owner');n.append(iconNode('agent'),el('span','owner-name',name));return n;
    }));
  }
  status(c.st,t);statusMark(c.button,t);setText(c.title,t.title);setText(c.note,t.report_preview || 'No report yet.');mailCount(c.mail,t.mail_count);
  const hidden=hiddenPrerequisites.get(t.id) || 0;
  const caption=[t.reference_count ? `${t.reference_count} file reference${t.reference_count===1?'':'s'}`:'',t.cancelled_prerequisite_count?`${t.cancelled_prerequisite_count} cancelled prerequisite${t.cancelled_prerequisite_count===1?'':'s'}`:'',hidden?`${hidden} archived prerequisite${hidden===1?'':'s'} hidden`:''].filter(Boolean).join(' · ');
  // In the graph, done Tasks without an open question are one-line blocks; the details still show everything.
  const compact=compactTask(t), tip=compact?t.title:'';c.article.classList.toggle('compact',compact);if(c.button.title!==tip)c.button.title=tip; // a compact block may cut its title
  setText(c.foot,caption);
  c.button.classList.toggle('selected',state.selected===t.id||state.focus===t.id); // the focused Task keeps its outline
  c.button.setAttribute('aria-label',`${sentence(t.title)} ${labels[t.progress]}.${t.needs_you?' Needs you.':''} Assigned to ${listing(names) || 'Unassigned'}.${mailText(t)}${caption?' '+sentence(caption):''}`);
  const visible=matches(t);
  c.article.classList.toggle('archived',!!t.archived);
  c.article.classList.toggle('unfocused',!!focusIds&&!focusIds.has(t.id)&&!t.needs_you); // Needs you never fades
  c.article.classList.toggle('dimmed',!visible);
  c.article.inert=!visible;c.article.setAttribute('aria-hidden',String(!visible));
}
const edgesSVG = document.createElementNS(svgNS,'svg');edgesSVG.classList.add('connections');edgesSVG.setAttribute('aria-hidden','true');
edgesSVG.innerHTML='<defs><marker id="arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="5" markerHeight="5" orient="auto"><path d="M1 1l5 3-5 3" fill="none" stroke="var(--edge)" stroke-width="1.5"/></marker><marker id="arrow-active" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="4.5" markerHeight="4.5" orient="auto"><path d="M1 1l5 3-5 3" fill="none" stroke="var(--accent)" stroke-width="1.6"/></marker></defs><g></g>';
graph.append(edgesSVG);
const done = t => t.progress==='finished'||t.progress==='cancelled';
const compactTask = t => done(t)&&!t.needs_you;
let hiddenPrerequisites = new Map();
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
// Left to right, one column per longest-path rank over the drawn lines (the transitive reduction), ordered by three
// barycenter sweeps. Each Task is centered on the ports of its prerequisites and pushed down to keep the row gap, so a
// straight chain runs level. Tasks without a drawn line go in rows below or columns to the right, whichever Fits larger.
const cardWidth=274, columnGap=112, padX=28, padY=24, groupGap=96;
let ports=new Map(), unitSizes=new Map(); // the port height and the height of each card, measured once per layout
function layout() {
  const wasHidden=panel.hidden; if(wasHidden){panel.hidden=false;panel.style.visibility='hidden';}
  const tasks=graphTasks, byID=new Map(tasks.map(t=>[t.id,t])), rank=new Map(tasks.map(t=>[t.id,0]));
  const degree=new Map(tasks.map(t=>[t.id,0])), next=new Map(tasks.map(t=>[t.id,[]])), linked=new Set();
  for(const e of graphEdges) if(byID.has(e.prerequisite_id)&&byID.has(e.dependent_id)) {degree.set(e.dependent_id,degree.get(e.dependent_id)+1);next.get(e.prerequisite_id).push(e.dependent_id);linked.add(e.prerequisite_id).add(e.dependent_id);}
  const queue=tasks.filter(t=>!degree.get(t.id)).map(t=>t.id).sort();
  for(let i=0;i<queue.length;i++) for(const id of next.get(queue[i])) {
    rank.set(id,Math.max(rank.get(id),rank.get(queue[i])+1));degree.set(id,degree.get(id)-1);if(!degree.get(id))queue.push(id);
  }
  // A Task with no prerequisites sits one column before its earliest dependent, so its lines do not cross a column.
  const needs=new Set();for(const ns of next.values())for(const id of ns)needs.add(id);
  for(const t of tasks){const ns=next.get(t.id);if(ns.length&&!needs.has(t.id))rank.set(t.id,Math.min(...ns.map(id=>rank.get(id)))-1);}
  const cols=[];
  for(const t of tasks) if(linked.has(t.id)) (cols[rank.get(t.id)] ??= []).push(t.id);
  // Barycenter sweeps place each Task near the Tasks it connects to, so fewer lines cross; ties keep the stable Task order.
  const prev=new Map(tasks.map(t=>[t.id,[]]));for(const [id,ns] of next)for(const n of ns)prev.get(n).push(id);
  const order=new Map(), spread=l=>l.forEach((id,i)=>order.set(id,(i+.5)/l.length));cols.forEach(spread);
  const sweep=(l,near)=>{const c=new Map(l.map(id=>{const n=near.get(id);return [id,n.length?n.reduce((s,x)=>s+order.get(x),0)/n.length:order.get(id)];}));l.sort((a,b)=>c.get(a)-c.get(b));spread(l);};
  for(let i=1;i<cols.length;i++)sweep(cols[i],prev);for(let i=cols.length-2;i>=0;i--)sweep(cols[i],next);for(let i=1;i<cols.length;i++)sweep(cols[i],prev);
  // Measure the full cards: a card that content-visibility skips, or a zoomed-out block, has another height, and
  // measuring 2,000 skipped cards one by one takes seconds. The sizes are written with the positions, after all reads.
  const sizes=new Map(), bodies=new Map(), compact=new Set();ports=new Map();unitSizes=sizes;graph.classList.remove('cv','far','farther');
  for(const t of tasks){const c=cards.get(t.id);sizes.set(t.id,c.article.offsetHeight);bodies.set(t.id,c.button.offsetHeight);ports.set(t.id,c.button.offsetTop+c.button.offsetHeight/2);if(c.article.classList.contains('compact'))compact.add(t.id);}
  const gap=(a,b)=>compact.has(a)&&compact.has(b)?16:28, step=cardWidth+columnGap, pos=new Map();
  let h=0;
  cols.forEach((col,k)=>{
    let last=null;
    for(const id of col){
      const ps=prev.get(id), floor=last===null?-Infinity:pos.get(last).y+sizes.get(last)+gap(last,id);
      const want=k&&ps.length?ps.reduce((s,p)=>s+pos.get(p).y+ports.get(p),0)/ps.length-ports.get(id):last===null?0:floor;
      pos.set(id,{x:k*step,y:Math.max(want,floor)});last=id;
    }
  });
  // A column can start above 0 when its Tasks center on a taller column.
  const top=Math.min(0,...[...pos.values()].map(p=>p.y));for(const p of pos.values()){p.y-=top;h=Math.max(h,p.y);}
  for(const [id,p] of pos)h=Math.max(h,p.y+sizes.get(id));
  const w=cols.length?cols.length*step-columnGap:0;
  const loose=tasks.filter(t=>!linked.has(t.id)).map(t=>t.id), viewW=Math.max(1,(panel.clientWidth||1400)-2*padX), viewH=Math.max(1,(panel.clientHeight||670)-2*padY);
  const below=()=>{const out=[],perRow=Math.max(1,Math.floor((Math.max(w,viewW)+columnGap)/step));let y=h?h+groupGap:0;
    for(let i=0;i<loose.length;i+=perRow){const row=loose.slice(i,i+perRow);row.forEach((id,j)=>out.push([id,{x:j*step,y}]));y+=Math.max(...row.map(id=>sizes.get(id)))+28;}return out;};
  const right=()=>{const out=[],limit=Math.max(h,viewH);let x=w?w+groupGap:0,y=0,last=null;
    for(const id of loose){if(last!==null){y+=gap(last,id);if(y+sizes.get(id)>limit){x+=step;y=0;}}out.push([id,{x,y}]);y+=sizes.get(id);last=id;}return out;};
  const box=list=>{let W=w,H=h;for(const [id,p] of list){W=Math.max(W,p.x+cardWidth);H=Math.max(H,p.y+sizes.get(id));}return [W,H];};
  const fitScale=list=>{const [W,H]=box(list);return Math.min(viewW/W,viewH/H);};
  const a=loose.length?below():[], b=loose.length?right():[], extra=fitScale(a)>=fitScale(b)?a:b;
  for(const [id,p] of extra)pos.set(id,p);
  const [W,H]=box(extra);
  positions.clear();
  for(const [id,p] of pos){const x=padX+p.x,y=padY+p.y;positions.set(id,{x,y});const s=cards.get(id).article.style;s.left=x+'px';s.top=y+'px';s.setProperty('--uh',sizes.get(id)+'px');s.setProperty('--h',bodies.get(id)+'px');}
  // Tab order follows the layout: column by column, top to bottom. A focused card keeps the focus.
  const focused=document.activeElement;let ref=edgesSVG.nextSibling;
  for(const [id] of [...positions].sort((a,b)=>a[1].x-b[1].x||a[1].y-b[1].y)){const n=cards.get(id).article;if(n===ref)ref=n.nextSibling;else place(graph,n,ref);}
  if(focused&&focused!==document.activeElement&&focused.isConnected)focused.focus({preventScroll:true});
  width=W+2*padX;height=H+2*padY; // tight around the Task units, so Fit fits the cards
  graph.style.width=width+'px';graph.style.height=height+'px';
  // Now cards out of view skip layout and paint, sized by the height just measured (--uh).
  graph.classList.add('cv');mapBase=null;applyZoom();drawEdges();
  if(wasHidden){panel.hidden=true;panel.style.visibility='';}
}
// A line leaves the output port at the right edge of the card body and ends 2px before the next card, as a cubic curve
// whose control points sit half the horizontal distance out. The focused Task's lines are drawn last, on top.
function curve(e) {
  const a=positions.get(e.prerequisite_id),b=positions.get(e.dependent_id);if(!a||!b)return null;
  const x1=a.x+cardWidth,y1=a.y+(ports.get(e.prerequisite_id) ?? 0),x2=b.x-2,y2=b.y+(ports.get(e.dependent_id) ?? 0),o=Math.max(Math.abs(x2-x1)/2,24);
  return `M${x1},${y1}C${x1+o},${y1} ${x2-o},${y2} ${x2},${y2}`;
}
function drawEdges() {
  const group=edgesSVG.querySelector('g'), lines=document.createDocumentFragment(), front=document.createDocumentFragment(), outs=new Set();
  for(const e of graphEdges){const d=curve(e);if(!d)continue;outs.add(e.prerequisite_id);const c=connection(e,d);(c.classList.contains('active')?front:lines).append(c);}
  // The focused Task also shows its direct lines that the reduction hides, dashed.
  if(state.focus)for(const e of hiddenEdges) {
    if(e.prerequisite_id!==state.focus&&e.dependent_id!==state.focus)continue;
    const d=curve(e);if(!d)continue;const c=connection(e,d);c.classList.add('implied');front.append(c);
  }
  const portsFrag=document.createDocumentFragment();
  for(const id of outs){
    const p=positions.get(id),c=document.createElementNS(svgNS,'circle');c.setAttribute('cx',p.x+cardWidth);c.setAttribute('cy',p.y+(ports.get(id) ?? 0));c.setAttribute('r','4.5');c.classList.add('port');
    if(id===state.focus||id===state.selected)c.classList.add('active');else if(focusIds&&!focusIds.has(id))c.classList.add('unfocused');
    portsFrag.append(c);
  }
  group.replaceChildren(lines,front,portsFrag);
}
function connection(e,d) {
  const p=document.createElementNS(svgNS,'path');p.setAttribute('d',d);p.classList.add('connection');
  const near=e.prerequisite_id===state.focus||e.dependent_id===state.focus||e.prerequisite_id===state.selected||e.dependent_id===state.selected;
  if(near)p.classList.add('active');else if(state.focus)p.classList.add('unfocused');
  p.setAttribute('marker-end',near?'url(#arrow-active)':'url(#arrow)');
  index();if(!matches(taskByID.get(e.prerequisite_id))&&!matches(taskByID.get(e.dependent_id)))p.classList.add('faded');
  return p;
}
function measure(name,run) {performance.clearMarks(name+'-start');performance.clearMarks(name+'-end');performance.clearMeasures(name);performance.mark(name+'-start');run();performance.mark(name+'-end');performance.measure(name,name+'-start',name+'-end');}
function patchGraph() {
  if(state.focus&&!graphTasks.some(t=>t.id===state.focus))state.focus=null;
  focusIds=state.focus?focusSet():null;mapBase=null; // statuses and filters color the minimap
  const ids=new Set(graphTasks.map(t=>t.id));
  for(const [id,c] of cards)if(!ids.has(id)){c.article.remove();cards.delete(id);}
  // Archived prerequisites that the graph hides, named in the caption of the Tasks that need them.
  hiddenPrerequisites=new Map();
  for(const e of model.dependencies)if(!ids.has(e.prerequisite_id)&&ids.has(e.dependent_id))hiddenPrerequisites.set(e.dependent_id,(hiddenPrerequisites.get(e.dependent_id) || 0)+1);
  for(const t of graphTasks) {let c=cards.get(t.id);if(!c){c=makeCard(t);cards.set(t.id,c);graph.append(c.article);}patchCard(c,t);}
  const key=JSON.stringify([graphTasks.map(t=>[t.id,t.title,owners(t),compactTask(t),t.needs_you,t.reference_count,t.cancelled_prerequisite_count,hiddenPrerequisites.get(t.id) || 0]),model.dependencies.map(e=>[e.prerequisite_id,e.dependent_id])]);
  if(geometryKey!==key){
    geometryKey=key;
    measure('a-stack-layout',()=>{measure('a-stack-reduce',()=>{({kept:graphEdges,hidden:hiddenEdges}=reduceEdges(graphTasks,model.dependencies));});layout();});
  }else drawEdges();
}
// The Agents tab: one card per Agent, the most urgent first or by name, Unassigned last. Needs you and Working
// always show; Waiting shows its 4 newest, then "N more waiting", but all 5 of 5, since a fold of one row saves
// nothing; finished and cancelled Tasks fold away. While a search or a filter is set, every matching Task shows.
// Cards and rows are patched in place, so focus stays.
const rank=t=>t.needs_you?0:t.progress==='working'?1:t.progress==='waiting'?2:3;
const newest=(a,b)=>a.updated_at<b.updated_at?1:a.updated_at>b.updated_at?-1:a.id<b.id?-1:1;
const chipKinds=[['needs','attention','needs you'],['working','working','working'],['waiting','waiting','waiting'],['done','finished','done']];
const folds=new Set(); // the open folds, only in memory: 'w' or 'd' and the Agent ID
state.agentSort=load('a-stack-agent-sort')==='name'?'name':'urgency';
function agentGroups() {
  index();
  const groups=[...model.agents,{id:'unassigned',name:'Unassigned'}].map(a=>{
    const all=a.id==='unassigned'?model.tasks.filter(t=>t.unassigned):(agentTasks.get(a.id) || []).map(id=>taskByID.get(id)).filter(Boolean);
    const tasks=all.filter(t=>state.archived||!t.archived).sort((x,y)=>rank(x)-rank(y)||newest(x,y)), counts=[0,0,0,0];
    for(const t of tasks)counts[rank(t)]++;
    return {a,tasks,counts,hidden:all.length-tasks.length,ask:tasks.filter(t=>t.needs_you).map(t=>t.updated_at).sort()[0] || ''};
  }).filter(g=>g.a.id!=='unassigned'||g.tasks.length);
  // Most urgent: Needs you first, the oldest question first; then the most Working, then the most Waiting.
  const key=g=>[g.a.id==='unassigned'?1:0,...(state.agentSort==='urgency'?[g.counts[0]?0:1,g.ask,-g.counts[1],-g.counts[2]]:[])];
  return groups.sort((x,y)=>{const a=key(x),b=key(y);for(let i=0;i<a.length;i++)if(a[i]!==b[i])return a[i]<b[i]?-1:1;return x.a.name<y.a.name?-1:x.a.name>y.a.name?1:0;});
}
function makeAgentCard(id) {
  const section=el('section','agent-row'),heading=el('header','agent-heading'),identity=el('div','agent-identity'),avatar=el('span','agent-avatar'),names=el('div'),name=el('h2','agent-name'),meta=el('div','agent-meta'),chips=el('div','chips');
  const open=el('ul','task-rows'),moreWaiting=el('button','fold'),moreDone=el('button','fold'),doneList=el('ul','task-rows done'),empty=el('p','agent-empty');
  avatar.setAttribute('aria-hidden','true');identity.append(avatar,names);names.append(name,meta);heading.append(identity,chips);section.append(heading,open,moreWaiting,moreDone,doneList,empty);
  for(const [button,kind] of [[moreWaiting,'w'],[moreDone,'d']]){button.append(iconNode('chevron'),el('span'));button.onclick=()=>{const k=kind+id;if(!folds.delete(k))folds.add(k);patchAgents();};}
  return {section,name,avatar,meta,chips,open,moreWaiting,moreDone,doneList,empty,rows:new Map()};
}
function makeRow(t) {
  const li=el('li'),b=el('button','task-row'),st=el('span','row-status'),main=el('span','row-main'),title=el('span','row-title'),question=el('span','row-question'),others=el('span','row-meta'),side=el('span','row-side'),mail=el('span','mail-count'),when=el('time','now-age');
  main.append(title,question,others);side.append(mail,when);b.append(st,main,side);li.append(b);b.dataset.openTask=t.id;b.addEventListener('click',()=>openTask(t.id,b));
  return {li,b,st,title,question,others,mail,when};
}
function patchRow(r,t,agent) {
  const kind=t.needs_you?'needs-you':t.progress;
  if(r.st.dataset.key!==kind){r.st.dataset.key=kind;r.st.className='row-status '+kind;r.st.replaceChildren(iconNode(t.needs_you?'attention':t.progress==='cancelled'?'idle':t.progress));}
  const others=owners(t).filter(n=>n!==agent);
  setText(r.title,t.title);setText(r.question,t.needs_you?t.question_preview || '':'');r.question.hidden=!r.question.textContent;
  setText(r.others,others.length?'with '+others.slice(0,2).join(', ')+(others.length>2?` +${others.length-2}`:''):'');r.others.hidden=!others.length;
  mailCount(r.mail,t.mail_count);stamp(r.when,t.updated_at);
  r.b.classList.toggle('selected',state.selected===t.id);r.b.classList.toggle('archived',!!t.archived);
  r.b.setAttribute('aria-label',`${sentence(t.title)} ${labels[t.progress]}.${t.archived?' Archived.':''}${asked(t)}${others.length?' With '+listing(others)+'.':''}${mailText(t)} Changed ${age(t.updated_at)}.`);
  return r.li;
}
function patchAgents() {
  const focused=document.activeElement, filtering=!!state.query.trim()||state.filter!=='all', groups=agentGroups(), live=new Set();
  let needs=0,working=0,idle=0;
  for(const g of groups) {
    const id=g.a.id, me=id==='unassigned'?'':g.a.name, [n,w,wait]=g.counts;live.add(id);
    if(id!=='unassigned'){if(n)needs++;if(w)working++;if(!n&&!w&&!wait)idle++;}
    let r=rows.get(id);if(!r){r=makeAgentCard(id);rows.set(id,r);}
    setText(r.name,g.a.name);setText(r.avatar,g.a.name.slice(0,2));setText(r.meta,id==='unassigned'?'No assignee':agentMeta(g.a));
    r.section.classList.toggle('urgent',n>0);r.section.classList.toggle('idle',!n&&!w&&!wait);
    const chipKey=g.counts.join();
    if(r.chips.dataset.key!==chipKey){r.chips.dataset.key=chipKey;r.chips.replaceChildren(...chipKinds.flatMap(([kind,name,word],i)=>{if(!g.counts[i])return [];const c=el('span','chip '+kind);c.append(iconNode(name),document.createTextNode(`${g.counts[i]} ${word}`));return [c];}));}
    const ids=new Set(g.tasks.map(t=>t.id));for(const k of r.rows.keys())if(!ids.has(k))r.rows.delete(k);
    const row=t=>{let x=r.rows.get(t.id);if(!x){x=makeRow(t);r.rows.set(t.id,x);}return patchRow(x,t,me);};
    const shown=filtering?g.tasks.filter(matches):g.tasks, waiting=shown.filter(t=>rank(t)===2), doneTasks=shown.filter(t=>rank(t)===3);
    const allWaiting=filtering||waiting.length<=5||folds.has('w'+id), allDone=filtering||folds.has('d'+id);
    placeAll(r.open,shown.filter(t=>rank(t)<2).concat(allWaiting?waiting:waiting.slice(0,4)).map(row));
    placeAll(r.doneList,allDone?doneTasks.map(row):[]);
    r.moreWaiting.hidden=filtering||waiting.length<=5;r.moreDone.hidden=filtering||!doneTasks.length;
    setText(r.moreWaiting.lastChild,allWaiting?'Show fewer waiting':`${waiting.length-4} more waiting`);r.moreWaiting.setAttribute('aria-expanded',String(allWaiting));
    setText(r.moreDone.lastChild,`${plural(doneTasks.length,'finished or cancelled Task')}`);r.moreDone.setAttribute('aria-expanded',String(allDone));
    r.open.hidden=!r.open.children.length;r.doneList.hidden=!r.doneList.children.length;
    setText(r.empty,g.tasks.length?'':g.hidden?`${plural(g.hidden,'archived Task')} hidden.`:'No Tasks.');r.empty.hidden=!!g.tasks.length;
    r.section.hidden=filtering&&!shown.length;
  }
  for(const [id,r] of rows)if(!live.has(id)){r.section.remove();rows.delete(id);}
  placeAll($('agent-list'),groups.map(g=>rows.get(g.a.id).section));
  const agents=model.agents.length;
  setText($('agents-summary'),agents?`${plural(agents,'Agent')}: ${needs} with Needs you, ${working} working, ${idle} with nothing open`:'');
  document.querySelector('.agents-bar').hidden=!agents;
  for(const b of document.querySelectorAll('[data-sort]'))b.setAttribute('aria-pressed',String(b.dataset.sort===state.agentSort));
  if(focused&&focused!==document.activeElement&&focused.isConnected)focused.focus({preventScroll:true});
}
for(const b of document.querySelectorAll('[data-sort]'))b.addEventListener('click',()=>{state.agentSort=b.dataset.sort;save('a-stack-agent-sort',state.agentSort);if(model)patchAgents();});
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
const doneColumn=columns[3], more=el('button','plain-button now-more');more.hidden=true;doneColumn.section.append(more);
more.onclick=()=>{state.allFinished=!state.allFinished;render();};
const nowCards=new Map();
const column=t=>t.needs_you?'attention':t.progress==='working'?'working':t.progress==='waiting'?'waiting':'done';
const firstLine=text=>(text || '').split('\n').find(l=>l.trim())?.trim() || '';
function stamp(n,at) { if(n.dataset.at!==at){n.dataset.at=at;n.dateTime=at;} setText(n,at?age(at):''); }
function makeNowCard(t) {
  const button=el('button','task-card now-card'),top=el('span','now-top'),st=el('span','status'),mail=el('span','mail-count'),when=el('time','now-age'),title=el('span','task-title'),own=el('span','now-owners'),question=el('span','now-question'),waits=el('span','now-waits'),note=el('span','task-note');
  top.append(st,mail,when);button.append(top,title,own,question,waits,note);button.dataset.openTask=t.id;
  button.addEventListener('click',()=>openTask(t.id,button));
  return {button,st,mail,when,title,own,question,waits,note};
}
function patchNowCard(c,t,byID) {
  const names=owners(t), blockers=(prerequisites.get(t.id) || []).map(e=>byID.get(e.prerequisite_id)).filter(p=>p&&!done(p)).map(p=>p.title);
  status(c.st,t);statusMark(c.button,t);mailCount(c.mail,t.mail_count);stamp(c.when,t.updated_at);setText(c.title,t.title);
  const ownKey=names.join('\n');if(c.own.dataset.key!==ownKey){c.own.dataset.key=ownKey;c.own.replaceChildren(iconNode('agent'),el('span','owner-name',names.join(', ') || 'Unassigned'));}
  setText(c.question,t.question_preview || '');c.question.hidden=!t.needs_you||!t.question_preview;
  setText(c.waits,blockers.length?'Waits for: '+blockers.slice(0,3).join(', ')+(blockers.length>3?` +${blockers.length-3} more`:''):'');c.waits.hidden=t.progress!=='waiting'||!blockers.length;
  const report=firstLine(t.report_preview);setText(c.note,report || 'No report yet.');
  c.button.classList.toggle('selected',state.selected===t.id);c.button.classList.toggle('archived',!!t.archived);
  c.button.setAttribute('aria-label',`${sentence(t.title)} ${labels[t.progress]}.${t.archived?' Archived.':''}${asked(t)} Assigned to ${listing(names) || 'Unassigned'}.${c.waits.hidden?'':' '+sentence(c.waits.textContent)}${mailText(t)} Changed ${age(t.updated_at)}.`);
}
// Moving a node keeps its focus where the browser supports moveBefore.
function place(list,node,ref) { if(node.nextSibling===ref&&node.parentNode===list)return; try{if(list.moveBefore&&node.isConnected&&node.ownerDocument===list.ownerDocument){list.moveBefore(node,ref);return;}}catch{} list.insertBefore(node,ref); }
function placeAll(list,nodes) { nodes.forEach((n,i)=>place(list,n,list.children[i] || null)); while(list.children.length>nodes.length)list.lastChild.remove(); }
function patchNow() {
  const focused=document.activeElement, byID=new Map(model.tasks.map(t=>[t.id,t]));
  for(const [id] of nowCards)if(!byID.has(id))nowCards.delete(id);
  const archivedAll=model.tasks.filter(t=>t.archived).sort(newest), archivedShown=archivedAll.filter(matches);
  for(const col of columns) {
    const all=model.tasks.filter(t=>column(t)===col.key&&!t.archived).sort(newest), shown=all.filter(matches);
    const listed=col===doneColumn&&state.allFinished?shown.concat(archivedShown):shown;
    placeAll(col.list,listed.map(t=>{let c=nowCards.get(t.id);if(!c){c=makeNowCard(t);nowCards.set(t.id,c);}patchNowCard(c,t,byID);return c.button;}));
    setText(col.count,shown.length===all.length?String(all.length):`${shown.length} of ${all.length}`);
    col.section.classList.toggle('has-items',all.length>0);
    setText(col.emptyNode,all.length?'No matching Tasks.':col.empty);col.emptyNode.hidden=listed.length>0;
    if(col===doneColumn){more.hidden=!archivedAll.length;setText(more,state.allFinished?'Hide archive':`Show archive (${archivedShown.length===archivedAll.length?archivedAll.length:archivedShown.length+' of '+archivedAll.length})`);more.setAttribute('aria-expanded',String(state.allFinished));}
  }
  patchFeed(byID);
  if(focused&&focused!==document.activeElement&&focused.isConnected)focused.focus({preventScroll:true});
}
// The feed keeps the changes this page has seen: each Task's last action at its updated_at, its report time,
// and differences between polls. A change that is not a Task's latest one shows only while the page watched it.
const events=new Map();let feedModel=null;const feedRows=new Map();
const actionKinds={'task.create':'create','task.status':'status','task.reopen':'status','task.report':'report','task.attention.set':'ask','task.attention.resolve':'resolve','task.assign':'assign','task.unassign':'assign','task.reference.add':'refs','task.reference.remove':'refs','task.archive':'archive','task.unarchive':'unarchive'};
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
// The action is its own line under the title, so a long title never hides it.
function eventText(e) {
  return {status:`Changed to ${labels[e.progress] || e.progress}`,create:'Added',report:e.text?'Reported: '+e.text:'Reported',ask:e.text?'Needs you: '+e.text:'Needs you',resolve:'Needs you resolved',assign:'Agents changed',refs:'File references changed',archive:'Archived',unarchive:'Unarchived',edit:'Edited'}[e.kind];
}
const feedIcons={ask:'attention',resolve:'finished',archive:'archive',unarchive:'archive',report:'report',refs:'file',assign:'agents',create:'plus',edit:'edit'};
function patchFeed(byID) {
  if(feedModel!==model)collectEvents();
  const list=$('feed-list'), shown=[...events.values()].filter(e=>byID.has(e.task)&&matches(byID.get(e.task))).sort((a,b)=>a.at<b.at?1:a.at>b.at?-1:a.key<b.key?-1:1).slice(0,12);
  for(const k of feedRows.keys())if(!events.has(k))feedRows.delete(k);
  placeAll(list,shown.map(e=>{
    let r=feedRows.get(e.key),t=byID.get(e.task);
    if(!r){const li=el('li'),b=el('button','feed-item'),mark=el('span','feed-mark'),text=el('span','feed-text'),title=el('span','feed-title'),what=el('span','feed-what'),when=el('time','now-age');
      mark.append(iconNode(e.kind==='status'?(e.progress==='cancelled'?'idle':e.progress):feedIcons[e.kind]));mark.className='feed-mark '+(e.kind==='status'?e.progress:e.kind);
      text.append(title,what);b.append(mark,text,when);li.append(b);b.onclick=()=>openTask(e.task,b);r={li,b,title,what,when};feedRows.set(e.key,r);}
    setText(r.title,t.title);setText(r.what,eventText(e));stamp(r.when,e.at);r.b.setAttribute('aria-label',`${t.title}: ${eventText(e)}, ${age(e.at)}`);
    return r.li;
  }));
  $('feed-empty').hidden=shown.length>0;
}
// Ages tick every second only where they show: the Now and Agents tabs and the open Task mail.
function updateNowAges() { for(const root of [state.view==='now'&&nowPanel,state.view==='agents'&&agentsPanel,dialog.open&&dialog,$('attention-dialog').open&&$('attention-dialog')])if(root)for(const n of root.querySelectorAll('time[data-at]'))setText(n,age(n.dataset.at)); }
function render() {
  if(state.theme==='system')delete document.documentElement.dataset.theme;else document.documentElement.dataset.theme=state.theme;
  document.querySelectorAll('.theme-button').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.theme===state.theme)));
  document.querySelectorAll('[data-view]').forEach(b=>{const active=b.dataset.view===state.view;b.setAttribute('aria-selected',String(active));b.tabIndex=active?0:-1;});
  panel.hidden=state.view!=='graph';agentsPanel.hidden=state.view!=='agents';nowPanel.hidden=state.view!=='now';
  $('zoom-controls').hidden=$('minimap-toggle').hidden=state.view!=='graph';$('archived').hidden=state.view==='now';minimap.hidden=true;
  if(!model){
    // Nothing is known before the first snapshot, so no column may say that nothing needs you.
    const failed=!!notice||failures>0;
    $('empty').hidden=false;setText($('empty').querySelector('h2'),failed?'No Session data':'Loading the Session…');
    setText($('empty').querySelector('p'),failed?'The view shows the Session once the local service answers.':'Reading it from the local service.');
    $('clear-filters').hidden=true;graph.parentElement.style.visibility='hidden';nowPanel.firstElementChild.style.visibility='hidden';
    updateSource();return;
  }
  setText(document.querySelector('.workspace-title h1'),model.snapshot.session.name);
  setText($('project-name'),model.snapshot.workspace_name || '');$('project-name').hidden=!model.snapshot.workspace_name; // the Workspace folder name, never its full path
  setText(document.querySelector('.workspace-subtitle'),`${plural(model.agents.length,'agent')} · ${plural(model.tasks.length,'task')}${model.snapshot.session.closed?' · Closed Session':''}`);
  const count=model.tasks.filter(matches).length, attention=model.tasks.filter(t=>t.needs_you);
  const attentionText=attention.length?`Needs you (${attention.length})`:'All clear';
  if($('attention').textContent!==attentionText)$('attention').replaceChildren(iconNode(attention.length?'attention':'finished'),document.createTextNode(attentionText));$('attention').disabled=!attention.length;
  setText($('result-count'),state.query||state.filter!=='all'?`${count} of ${model.tasks.length} tasks`:'');
  // The service decides which Tasks are archived (command, chain or age); the graph hides them unless the Archived toggle is on.
  graphTasks=state.archived?model.tasks:model.tasks.filter(t=>!t.archived);
  if(feedModel!==model)collectEvents(); // the feed sees every snapshot, also while another tab shows
  if(!document.hidden)patchView(state.view);
  // In the graph, all matching Tasks can be archived and hidden.
  const onlyHidden=state.view==='graph'&&count>0&&!graphTasks.some(matches);
  // An empty Session says what comes next; Clear filters shows only when a filter is set.
  const noAgents=state.view==='agents'&&!model.agents.length, noTasks=!model.tasks.length&&state.view!=='agents', start=' To start, brief an Orchestrator with the a-stack skill.';
  $('empty').hidden=!(noAgents || noTasks || onlyHidden || model.tasks.length&&!count);
  setText($('empty').querySelector('h2'),noAgents?'No Agents yet':noTasks?'No Tasks yet':onlyHidden?'Only archived Tasks match':'No matching work');
  setText($('empty').querySelector('p'),noAgents?'Agents appear here when the Orchestrator registers them.'+start:noTasks?'Tasks appear here when the Orchestrator adds them.'+start:onlyHidden?'Turn on Archived to see them in the graph.':'Try another Agent name, Task or status.');
  $('clear-filters').hidden=!state.query.trim()&&state.filter==='all';
  graph.parentElement.style.visibility=count&&!onlyHidden?'visible':'hidden';
  nowPanel.firstElementChild.style.visibility=count?'visible':'hidden';
  updateHint();
  if(state.selected){if(model.tasks.some(t=>t.id===state.selected))loadDetail();else $('task-dialog').close();}
  if($('attention-dialog').open)renderAttention();
  // The Archived toggle works in the Graph and Agents tabs.
  const archived=model.tasks.filter(t=>t.archived).length;setText($('archived-label'),`Archived (${archived})`);$('archived').setAttribute('aria-pressed',String(state.archived));
  $('archived').setAttribute('aria-label',`${state.archived?'Hide':'Show'} ${plural(archived,'archived Task')} in the Graph and Agents tabs`);
  if(state.view==='graph')redrawMinimap();
  updateSource();
}
// Only the visible view is patched. The others are patched when they show, if their inputs changed since.
const patched={}, patchers={now:()=>patchNow(),graph:()=>patchGraph(),agents:()=>patchAgents()};
function patchView(view) {
  const inputs=[model,state.query,state.filter,state.archived,state.focus,state.selected,state.allFinished], last=patched[view];
  if(last&&inputs.every((x,i)=>x===last[i]))return;
  patched[view]=inputs;patchers[view]();
}
function age(timestamp) {const seconds=Math.max(0,Math.floor((Date.now()-new Date(timestamp).getTime())/1000));return seconds<60?`${seconds}s ago`:seconds<3600?`${Math.floor(seconds/60)}m ago`:seconds<86400?`${Math.floor(seconds/3600)}h ago`:`${Math.floor(seconds/86400)}d ago`;}
// The runtime state is what the service last observed, so it says "unverified" and its age.
function agentMeta(a) { return `${a.id===model.snapshot.session.orchestrator_id?'Orchestrator':'Agent'} · runtime ${a.runtime_attached?(a.runtime_state || 'unknown'):'unbound'} · unverified${a.observed_at?' · '+age(a.observed_at):''}`; }
function updateRuntimeAges() { if(model&&state.view==='agents')for(const a of model.agents){const r=rows.get(a.id);if(r)setText(r.meta,agentMeta(a));} }
function plural(n,word) { return `${n} ${word}${n===1?'':'s'}`; }
// Saved data older than 10 s, or no data at all, gets a banner above the view and "Stale" in the tab title.
// If the service still answers, it is busy (Task changes keep restarting the collection);
// if not, a restart on a new port needs a-stack.
function staleShown() { return stale&&(!model||Date.now()-lastSuccess>10000); }
const clock=ms=>new Date(ms).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
function updateBanner() {
  const since=lastSuccess?` since ${clock(lastSuccess)}`:'';
  const text=notice || (!staleShown()?'':!model?'The local service does not answer. Retrying. If you restarted it, run a-stack again.':answered?`The service is busy${since}: Task changes keep restarting the read. Showing the last complete view.`:`The local service has not answered${since}. Showing saved data. If you restarted it, run a-stack again.`);
  setText($('banner'),text);$('banner').hidden=!text;document.querySelector('main').classList.toggle('stale',!!text&&!!model);
}
// The service sets X-A-Stack-Low-Disk on snapshot answers, 304 included.
let lowDisk=false;
function updateHint() {
  const hint=$('footer-hint');hint.classList.toggle('lost',lowDisk);
  setText(hint,lowDisk?'Disk space is low. Run a-stack storage status.':state.view!=='graph'?'Select a task for its report and dependencies.':state.zoom<.3?'Overview: blocks in status colors. Zoom in for titles.':state.zoom<.5?'Titles only. Zoom past 50% for reports and Agents.':'Drag to pan. Select a task to see its neighbours.');
  updateBanner();
}
function updateSource(message) {
  const service=version?'Local service '+version:'Local service';
  // While polls succeed, "just now" keeps the footer still, so it causes no layout every second.
  const text=message || (notice?'No access · run a-stack':lastSuccess?`${stale?'Saved · stale':service} · checked ${!stale&&Date.now()-lastSuccess<60000?'just now':age(new Date(lastSuccess).toISOString())}`:service+' · connecting');
  updateHint();
  setText(document.querySelector('.preview-label'),text);setText(document.querySelector('.preview-short'),stale?'Saved · stale':'Read-only');
  setText($('source-info'),`${text}${model?'. Snapshot captured '+model.snapshot.captured_at+' · revision '+model.snapshot.session.read_revision+'. Runtime observations have their own age and are unverified.':''}`);
  $('about-button').setAttribute('aria-label',text+'. About this read-only view');
  updateTitle();
}
// The tab title and icon count the Needs you requests, so a background tab shows them.
const favicon=document.querySelector('link[rel="icon"]'), plainIcon=favicon.href;
const dotIcon=plainIcon.replace('%3C/svg%3E',"%3Ccircle cx='25' cy='7' r='6.5' fill='%23d93025' stroke='%23ffffff' stroke-width='2'/%3E%3C/svg%3E");
function updateTitle() {
  const n=model?model.tasks.filter(t=>t.needs_you).length:0;
  const title=(notice?'No access · ':staleShown()?'Stale · ':'')+(n?`(${n}) Needs you · `:'')+(model?model.snapshot.session.name+' – a-stack':'a-stack');
  if(document.title!==title)document.title=title;
  const href=n?dotIcon:plainIcon;if(favicon.getAttribute('href')!==href)favicon.setAttribute('href',href);
}
// Notify me is off by default and kept per browser. A notification names the Session and the number of
// requests, never a question. It is local to the browser: nothing is sent to the service.
const canNotify='Notification' in window;
let notify=canNotify&&load('a-stack-notify')==='1', needIDs=null;
function patchNotify() {
  const b=$('notify');b.hidden=!canNotify;if(!canNotify)return;
  const on=notify&&Notification.permission==='granted', blocked=Notification.permission==='denied';
  b.setAttribute('aria-pressed',String(on));b.disabled=blocked;
  b.title=blocked?'Notifications are blocked for this page in the browser settings':on?'Notifications are on: a Task that newly needs you shows one':'Show a notification when a Task newly needs you';
}
$('notify').onclick=async()=>{
  if(notify&&Notification.permission==='granted')notify=false;
  else{try{if(Notification.permission==='default')await Notification.requestPermission();}catch{}notify=Notification.permission==='granted';}
  save('a-stack-notify',notify?'1':'0');patchNotify();
};
function notifyNew() {
  const ids=new Set(model.tasks.filter(t=>t.needs_you).map(t=>t.id)), fresh=needIDs?[...ids].some(id=>!needIDs.has(id)):false;needIDs=ids;
  if(!fresh||!notify||Notification.permission!=='granted'||!document.hidden&&document.hasFocus())return;
  try{const n=new Notification('Needs you · '+model.snapshot.session.name,{body:`${ids.size===1?'1 Task needs':ids.size+' Tasks need'} you. Open the a-stack tab to answer.`,tag:'a-stack-needs-you-'+context.session_id});n.onclick=()=>{window.focus();n.close();};}catch{}
}
// Semantic zoom: below 50% (far) the cards are blocks in their status color with only the title, at a constant size on
// screen; below 30% (farther) only the blocks: a block is then too narrow for more than one cut word.
// CSS scales every size by 1/zoom (--z), so no new layout runs.
// --lines is the number of title lines that fit the smallest full card at this zoom. Both are set only while zoomed
// out: every card inherits them, so a change restyles the cards on screen.
function applyZoom() {
  const z=state.zoom;graph.style.transform=`scale(${z})`;graph.parentElement.style.width=width*z+'px';graph.parentElement.style.height=height*z+'px';setText($('zoom-reset'),(z<.1?(z*100).toFixed(1):Math.round(z*100))+'%');
  if(z<.5){graph.style.setProperty('--z',z);graph.style.setProperty('--lines',Math.max(1,Math.floor((130*z-8)/16)));}
  graph.classList.toggle('far',z<.5);graph.classList.toggle('farther',z<.3);updateHint();redrawMinimap();
}
// The minimap: one canvas at the bottom right of the graph, drawn from the positions and heights that the layout
// cached, never from the DOM. Its base picture is drawn once per graph change; a scroll redraws only the box of
// the visible part. It is hidden while the graph fits. The Minimap button in the footer, or m in the graph, turns it
// off or on, and the button shows which, also while the graph fits. The minimap itself is for the pointer only:
// screen readers skip it and it never takes the focus.
const minimap=$('minimap'), mapCanvas=minimap.firstElementChild, phone=matchMedia('(max-width: 700px)');
let mapBase=null, mapOn=load('a-stack-minimap')!=='0', mapFrame=0;
const mapButton=$('minimap-toggle');
function setMinimap(on) { mapOn=on;save('a-stack-minimap',on?'1':'0');mapButton.setAttribute('aria-pressed',String(on));drawMinimap(); }
mapButton.setAttribute('aria-pressed',String(mapOn));mapButton.onclick=()=>setMinimap(!mapOn);
function buildMinimap() {
  const [cw,ch]=phone.matches?[140,91]:[200,130], r=devicePixelRatio || 1, c=document.createElement('canvas');c.width=cw*r;c.height=ch*r;
  const x=c.getContext('2d'), s=Math.min((cw-12)/width,(ch-12)/height), ox=(cw-width*s)/2, oy=(ch-height*s)/2;
  // Canvas needs plain colors: the browser resolves each light-dark() token on the minimap itself.
  const color=name=>{minimap.style.color=`var(--${name})`;return getComputedStyle(minimap).color;};
  const col={needs:color('attention'),working:color('working'),waiting:color('waiting'),finished:color('finished'),cancelled:color('strong-line'),back:color('chrome'),view:color('accent')};minimap.style.color='';
  x.scale(r,r);x.fillStyle=col.back;x.fillRect(0,0,cw,ch);
  for(const t of graphTasks){const p=positions.get(t.id);if(!p)continue;x.globalAlpha=!matches(t)?.15:t.needs_you?1:done(t)?.45:.8;x.fillStyle=col[t.needs_you?'needs':t.progress];x.fillRect(ox+p.x*s,oy+p.y*s,Math.max(1.5,cardWidth*s),Math.max(1.5,(unitSizes.get(t.id) || 0)*s));}
  // A ring around Needs you, so one question shows among 2,000 blocks.
  x.globalAlpha=1;x.strokeStyle=col.needs;x.lineWidth=1.5;
  for(const t of graphTasks){const p=positions.get(t.id);if(p&&t.needs_you)x.strokeRect(ox+p.x*s-3,oy+p.y*s-3,cardWidth*s+6,(unitSizes.get(t.id) || 0)*s+6);}
  mapBase={c,s,ox,oy,cw,ch,view:col.view};
}
function drawMinimap() {
  const z=state.zoom, show=mapOn&&!!model&&state.view==='graph'&&graph.parentElement.style.visibility!=='hidden'&&(width*z>panel.clientWidth+1||height*z>panel.clientHeight+1);
  minimap.hidden=!show;if(!show)return;
  if(!mapBase)buildMinimap();
  const {c,s,ox,oy,cw,ch}=mapBase, r=devicePixelRatio || 1, x=mapCanvas.getContext('2d');
  if(mapCanvas.width!==c.width||mapCanvas.height!==c.height){mapCanvas.width=c.width;mapCanvas.height=c.height;}
  x.setTransform(1,0,0,1,0,0);x.drawImage(c,0,0);x.setTransform(r,0,0,r,0,0);
  // The graph is centered while it is smaller than the panel, so subtract that margin.
  const left=(panel.scrollLeft-Math.max(0,(panel.clientWidth-width*z)/2))/z, top=(panel.scrollTop-Math.max(0,(panel.clientHeight-height*z)/2))/z;
  const x1=Math.max(1,ox+left*s), y1=Math.max(1,oy+top*s), x2=Math.min(cw-1,ox+(left+panel.clientWidth/z)*s), y2=Math.min(ch-1,oy+(top+panel.clientHeight/z)*s);
  x.strokeStyle=mapBase.view;x.lineWidth=1.5;x.strokeRect(x1,y1,Math.max(2,x2-x1),Math.max(2,y2-y1));
}
const redrawMinimap=()=>{if(!mapFrame)mapFrame=requestAnimationFrame(()=>{mapFrame=0;drawMinimap();});};
const resetMinimap=()=>{mapBase=null;redrawMinimap();};
panel.addEventListener('scroll',redrawMinimap,{passive:true});window.addEventListener('resize',redrawMinimap);
phone.addEventListener('change',resetMinimap);matchMedia('(prefers-color-scheme: dark)').addEventListener('change',resetMinimap);
// A click or a drag on the minimap centers the view there.
function mapJump(e) {
  if(!mapBase)return;const b=mapCanvas.getBoundingClientRect(), gx=((e.clientX-b.left)*mapBase.cw/b.width-mapBase.ox)/mapBase.s, gy=((e.clientY-b.top)*mapBase.ch/b.height-mapBase.oy)/mapBase.s;
  panel.scrollLeft=gx*state.zoom+Math.max(0,(panel.clientWidth-width*state.zoom)/2)-panel.clientWidth/2;panel.scrollTop=gy*state.zoom+Math.max(0,(panel.clientHeight-height*state.zoom)/2)-panel.clientHeight/2;
}
let mapDrag=false;
minimap.addEventListener('pointerdown',e=>{if(e.button!==0)return;e.preventDefault();mapDrag=true;minimap.setPointerCapture(e.pointerId);mapJump(e);});
minimap.addEventListener('pointermove',e=>{if(mapDrag)mapJump(e);});
for(const type of ['pointerup','pointercancel'])minimap.addEventListener(type,()=>{mapDrag=false;});
minimap.addEventListener('mousedown',e=>e.preventDefault()); // keeps the focus where it is
panel.addEventListener('keydown',e=>{if(e.key==='m'&&!e.repeat&&!e.isComposing&&!e.ctrlKey&&!e.metaKey&&!e.altKey)setMinimap(!mapOn);});
function setZoom(value) {
  const old=state.zoom;state.zoom=Math.max(.0001,Math.min(1.5,value));
  const x=(panel.scrollLeft+panel.clientWidth/2)/old,y=(panel.scrollTop+panel.clientHeight/2)/old;applyZoom();panel.scrollLeft=x*state.zoom-panel.clientWidth/2;panel.scrollTop=y*state.zoom-panel.clientHeight/2;
}
for(const b of document.querySelectorAll('[data-view]')) {
  b.addEventListener('click',()=>{state.view=b.dataset.view;save('a-stack-view',state.view);render();});
  b.addEventListener('keydown',e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();const i=views.indexOf(state.view),v=e.key==='Home'?views[0]:e.key==='End'?views.at(-1):views[(i+(e.key==='ArrowRight'?1:views.length-1))%views.length];$(v+'-tab').click();$(v+'-tab').focus();});
}
for(const b of document.querySelectorAll('.theme-button'))b.addEventListener('click',()=>{state.theme=b.dataset.theme;save('a-stack-theme',state.theme);mapBase=null;render();});
$('search').addEventListener('input',e=>{state.query=e.target.value;render();});
$('search').addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();const root={now:nowPanel,graph,agents:agentsPanel}[state.view];const c=[...root.querySelectorAll('.task-card, .task-row')].find(b=>!b.closest('[hidden]')&&!b.closest('[inert]'));if(c)c.focus();}});
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
  // Oldest request first, with the question preview and the age of the last change.
  const list=$('attention-list'), tasks=model.tasks.filter(t=>t.needs_you).sort((a,b)=>a.updated_at<b.updated_at?-1:a.updated_at>b.updated_at?1:0), ids=new Set(tasks.map(t=>t.id));
  const focused=document.activeElement, oldOrder=[...list.children], oldIndex=oldOrder.indexOf(focused);
  for(const [id,b] of attentionChoices)if(!ids.has(id)){b.remove();attentionChoices.delete(id);}
  placeAll(list,tasks.map(t=>{
    let b=attentionChoices.get(t.id);
    if(!b){b=el('button','dependency choice');b.dataset.attentionTask=t.id;b.append(el('span','dependency-name'),el('time','now-age'),el('span','choice-question'));b.onclick=()=>{$('attention-dialog').close();openTask(t.id,$('attention'));};attentionChoices.set(t.id,b);}
    setText(b.children[0],t.title);stamp(b.children[1],t.updated_at);setText(b.children[2],t.question_preview || '');return b;
  }));
  if(!tasks.length && $('attention-dialog').open){$('attention-dialog').close();$(state.view+'-tab').focus();}
  else if(oldIndex>=0&&!focused.isConnected)list.children[Math.min(oldIndex,tasks.length-1)]?.focus();
}
$('attention').onclick=()=>{const tasks=model.tasks.filter(t=>t.needs_you);if(tasks.length===1)openTask(tasks[0].id,$('attention'));else{renderAttention();$('attention-dialog').showModal();}};
const dialog=$('task-dialog');
const top=el('div','detail-top'),kicker=el('span','detail-kicker','TASK DETAILS'),close=el('button','icon-button');close.setAttribute('aria-label','Close task details');close.append(iconNode('close'));close.onclick=()=>dialog.close();top.append(kicker,close);
// The details: a property grid (status, Agents, Ticket, both directions of dependencies), then the sections that
// have content, then Task mail. Service details such as the read revision go in the footnote.
const body=el('div','detail-body'),detailTitle=el('h2'),detailStatus=el('div','status'),detailArchive=el('p','detail-archive'),detailLoading=el('p','detail-footnote'),props=el('dl','detail-props'),detailFields={},propRows={};detailTitle.id='detail-title';body.append(detailTitle,detailArchive,props);
for(const [key,label]of Object.entries({status:'Status',owners:'Agents',ticket:'Ticket',requires:'Requires',requiredBy:'Required by'})){
  const row=el('div','detail-prop'),dd=el('dd');row.append(el('dt','',label),dd);props.append(row);propRows[key]=row;detailFields[key]=dd;
}
detailFields.status.append(detailStatus);
for(const [key,label]of Object.entries({question:'Needs you',report:'Latest report',description:'Description',reason:'Reason',result:'Result',references:'File references'})){
  const section=el('section','detail-section'),h=el('h3','',label),content=el('div','detail-text');section.append(h,content);section.hidden=true;body.append(section);detailFields[key]=content;
}
detailFields.question.parentElement.classList.add('detail-question');
function detailText(name,text) { setText(detailFields[name],text || '');detailFields[name].parentElement.hidden=!text; }
// Task mail loads only when the Operator opens the section, oldest first, in pages that follow the mail read cursor.
const mailToggle=el('button','mail-toggle'),mailThread=el('div','mail-thread'),mailList=el('ol','mail-list'),mailStatus=el('p','detail-footnote mail-status'),mailMore=el('button','plain-button mail-more','Load more');
{const section=el('section','detail-section mail-section'),h=el('h3');mailThread.id='mail-thread';mailToggle.setAttribute('aria-controls',mailThread.id);mailToggle.append(iconNode('chevron'),el('span'));h.append(mailToggle);mailThread.append(mailList,mailStatus,mailMore);section.append(h,mailThread);body.append(section,detailLoading);}
const mail={task:null,open:false,cursor:'',more:false,loaded:0,busy:false,epoch:0,ids:new Set()};
function resetMail(id) {Object.assign(mail,{task:id,open:false,cursor:'',more:false,loaded:0,busy:false,ids:new Set()});mail.epoch++;mailList.replaceChildren();setText(mailStatus,'');mailMore.hidden=true;mailThread.hidden=true;mailToggle.setAttribute('aria-expanded','false');}
resetMail(null);
// Mentions of Agents in the Session are styled; everything stays text, never HTML.
function mailBody(text) {
  const names=new Set(model.agents.map(a=>a.name)), frag=document.createDocumentFragment();let last=0;
  for(const m of text.matchAll(/(^|[^A-Za-z0-9_-])@([A-Za-z0-9][A-Za-z0-9_-]{0,63})/g)){if(!names.has(m[2]))continue;const start=m.index+m[1].length;frag.append(text.slice(last,start),el('span','mention','@'+m[2]));last=start+1+m[2].length;}
  frag.append(text.slice(last));return frag;
}
function mailMessage(m) {
  const li=el('li','mail-message'),meta=el('div','mail-meta'),who=el('strong','mail-sender',m.sender_name || m.sender_id),when=el('time','now-age'),text=el('div','mail-body');
  stamp(when,m.received_at);when.title=new Date(m.received_at).toLocaleString();meta.append(who,when);text.append(mailBody(m.body));li.append(meta,text);return li;
}
async function fetchMail() {
  if(mail.busy||!mail.task)return;mail.busy=true;const epoch=mail.epoch,task=mail.task;mailMore.disabled=true;if(!mail.loaded)setText(mailStatus,'Loading Task mail…');
  try{
    const r=await request('/v1/mail/read',{task_id:task,cursor:mail.cursor,limit:50});
    if(epoch!==mail.epoch)return;
    for(const m of r.data.messages)if(!mail.ids.has(m.id)){mail.ids.add(m.id);mailList.append(mailMessage(m));}
    mail.cursor=r.data.cursor;mail.more=r.data.more;mail.loaded=mail.ids.size;setText(mailStatus,mail.loaded?'':'No Task mail yet.');
  }catch{if(epoch===mail.epoch)setText(mailStatus,'Task mail unavailable · retrying with the next refresh.');}
  finally{if(epoch===mail.epoch){mail.busy=false;mailMore.disabled=false;mailMore.hidden=!mail.more;labelMore();}}
}
// "Load 13 more": the next page, from the Task's mail count, or "Load more" when the count is behind.
function labelMore() { const left=(taskByID.get(mail.task)?.mail_count || 0)-mail.loaded;setText(mailMore,left>0?`Load ${Math.min(50,left)} more`:'Load more'); }
// On a poll, an open thread fetches the messages after its cursor, so the scroll position stays.
function patchMail(t) {
  setText(mailToggle.lastChild,`Task mail (${t.mail_count || 0})`);if(mail.more)labelMore();
  if(mail.open&&!mail.more&&!mail.busy&&(t.mail_count || 0)>mail.loaded)fetchMail();
}
mailToggle.onclick=()=>{
  mail.open=!mail.open;mailThread.hidden=!mail.open;mailToggle.setAttribute('aria-expanded',String(mail.open));
  const t=model?.tasks.find(t=>t.id===mail.task);
  if(mail.open&&!mail.loaded){if(t&&!t.mail_count)setText(mailStatus,'No Task mail yet.');else fetchMail();}
};
mailMore.onclick=()=>fetchMail();
dialog.append(top,body);
dialog.addEventListener('close',()=>{detailEpoch++;state.selected=null;detailKey='';render();if(returnFocus?.isConnected&&!returnFocus.closest('[inert]')&&!returnFocus.closest('[hidden]'))returnFocus.focus();else $(state.view+'-tab').focus();});
function setFocus(id) {state.focus=id;render();}
// Escape clears the focus once no dialog is open; an open dialog takes the first Escape.
document.addEventListener('keydown',e=>{if(e.key==='Escape'&&state.focus&&state.view==='graph'&&!document.querySelector('dialog[open]')){e.preventDefault();setFocus(null);}});
$('archived').addEventListener('click',()=>{state.archived=!state.archived;save('a-stack-archived',state.archived?'1':'0');render();});
function openTask(id,focus) {if(state.view==='graph')state.focus=id;if(state.selected!==id){resetMail(id);for(const name of ['description','report','reason','result','question','references'])detailText(name,'');delete detailFields.references.dataset.key;}returnFocus=focus || returnFocus;state.selected=id;detailKey='';detailEpoch++;render();if(!dialog.open){dialog.showModal();close.focus();}}
const archiveReasons={command:'Archived with task archive.',chain:'Archived: every Task that depends on it is finished or cancelled.',age:'Archived: it ended more than 24 hours ago.'};
function patchDetailMetadata(t) {
  setText(detailTitle,t.title);status(detailStatus,t);setText(detailArchive,t.archived?archiveReasons[t.archived_reason] || 'Archived.':'');detailArchive.hidden=!t.archived;
  setText(detailFields.ticket,t.ticket_ref || '');propRows.ticket.hidden=!t.ticket_ref;setText(detailFields.owners,owners(t).join(', ') || 'Unassigned');patchMail(t);
  // Both directions, each with the other Task's status icon: open Tasks first, then by title.
  index();const order=(a,b)=>done(a)-done(b)||(a.title<b.title?-1:a.title>b.title?1:0);
  const requires=model.dependencies.filter(e=>e.dependent_id===t.id).map(e=>taskByID.get(e.prerequisite_id)).filter(Boolean).sort(order);
  const requiredBy=model.dependencies.filter(e=>e.prerequisite_id===t.id).map(e=>taskByID.get(e.dependent_id)).filter(Boolean).sort(order);
  for(const [key,list] of [['requires',requires],['requiredBy',requiredBy]]){
    const dd=detailFields[key], signature=t.id+JSON.stringify(list.map(o=>[o.id,o.title,o.progress,o.archived]));propRows[key].hidden=!list.length;
    if(dd.dataset.key===signature)continue;dd.dataset.key=signature;
    dd.replaceChildren(...list.map(o=>{const b=el('button','dep-link'),mark=el('span','status '+o.progress);mark.append(iconNode(o.progress==='cancelled'?'idle':o.progress));
      b.append(mark,el('span','dependency-name',o.title));if(o.archived)b.append(el('span','dep-note','archived'));
      b.setAttribute('aria-label',`${o.title}, ${labels[o.progress]}${o.archived?', archived':''}`);b.onclick=()=>openTask(o.id,returnFocus);return b;}));
  }
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
    for(const name of ['description','report','reason','result'])detailText(name,detail.data[name]);
    const q=detail.data.question;detailText('question',q?`${q.question}\nAsked by ${model.agents.find(a=>a.id===q.asker_id)?.name || q.asker_id} at ${new Date(q.asked_at).toLocaleString()}${q.note?'\n'+q.note:''}`:'');
    const referencesKey=JSON.stringify(refs);
    if(detailFields.references.dataset.key!==t.id+referencesKey){detailFields.references.dataset.key=t.id+referencesKey;detailFields.references.replaceChildren(...refs.map(ref=>{const row=el('div','reference'),text=el('code','',ref.location+(ref.revision?' @ '+ref.revision:'')),copy=el('button','plain-button','Copy');copy.setAttribute('aria-label','Copy reference '+ref.location);copy.onclick=async()=>{try{await navigator.clipboard.writeText(ref.location);copy.textContent='Copied';}catch{copy.textContent='Select text to copy';}};row.append(text,copy);return row;}));
    }
    detailFields.references.parentElement.hidden=!refs.length;
    setText(detailLoading,`Read-only · viewing does not resolve requests. Read revision ${revision}${t.report_at?' · report received '+new Date(t.report_at).toLocaleString():''}.`);
  }catch{if(epoch===detailEpoch){detailKey='';setText(detailLoading,'Details unavailable · saved content may be stale. Retrying with the next refresh.');}}
}
async function request(endpoint,payload,headers={},signal) {
  const response=await fetch(endpoint,{method:'POST',headers:{'Content-Type':'application/json','Authorization':'Bearer '+token,'X-A-Stack-Instance':instance,...headers},body:JSON.stringify({...context,...payload}),credentials:'omit',cache:'no-store',redirect:'error',signal});
  const low=response.headers.get('X-A-Stack-Low-Disk')==='1';
  if(response.status===304)return {unchanged:true,etag:response.headers.get('ETag'),low};
  if(!response.ok){const error=new Error('Read unavailable');error.status=response.status;throw error;}
  const result=await response.json();if(!result.data)throw new Error('Invalid read response');return {...result,etag:response.headers.get('ETag'),low};
}
async function pages(kind,revision,task_id,signal) {
  const items=[];let after='';
  do{const r=await request('/v1/session/page',{kind,read_revision:revision,limit:100,after,...(task_id?{task_id}:{})},{},signal);if(r.data.read_revision!==revision)throw new Error('Read revision changed');items.push(...r.data.items);after=r.data.next || '';}while(after);
  return items;
}
async function refresh() {
  if(busy||!token)return;busy=true;controller=new AbortController();
  try{
    for(let attempt=0;attempt<3;attempt++) {
      try{
        const manifest=await request('/v1/session/snapshot',{known_revision:model?.snapshot.session.read_revision || 0},etag?{'If-None-Match':etag}:{},controller.signal);
        if(!manifest.unchanged&&!manifest.data.unchanged){
          const revision=manifest.data.session.read_revision;
          // Dependencies change only with graph_revision (a new Task or dependency), so the page keeps them otherwise.
          // It reads only their IDs; the progress fields in a kept edge may be old.
          const keep=model&&manifest.data.session.graph_revision===model.snapshot.session.graph_revision&&model.dependencies.length===manifest.data.counts.dependencies;
          const [tasks,agents,assignments,fresh]=await Promise.all(['tasks','agents','assignments',...(keep?[]:['dependencies'])].map(k=>pages(k,revision,null,controller.signal)));
          const dependencies=keep?model.dependencies:fresh;
          for(const [key,items]of Object.entries({tasks,agents,assignments,dependencies}))if(items.length!==manifest.data.counts[key])throw new Error('Incomplete collection');
          model={snapshot:manifest.data,tasks:tasks.sort((a,b)=>a.id<b.id?-1:a.id>b.id?1:0),agents:agents.sort((a,b)=>a.name<b.name?-1:a.name>b.name?1:0),assignments,dependencies:keep?dependencies:dependencies.sort((a,b)=>a.id<b.id?-1:a.id>b.id?1:0)};etag=manifest.etag || '';render();notifyNew();
        }
        lastSuccess=Date.now();stale=false;failures=0;lowDisk=manifest.low;updateSource();if(state.selected&&!document.hidden)loadDetail();break;
      }catch(error){
        if(error.status===409&&attempt<2){const shell=await fetch(location.pathname,{cache:'no-store',credentials:'omit',redirect:'error',signal:controller.signal});if(shell.ok){instance=shell.headers.get('X-A-Stack-Instance') || instance;version=shell.headers.get('X-A-Stack-Version') || version;updated(shell.headers.get('X-A-Stack-Assets') || pageAssets);}continue;}throw error;
      }
    }
  }catch(error){if(error.name!=='AbortError'){stale=true;failures++;answered=!(error instanceof TypeError)&&error.status!==401&&error.status!==403;if(error.status===401||error.status===403){token='';try{sessionStorage.removeItem(storageKey);}catch{}noAccess(accessEnded);}else if(model)updateSource();else{render();updateSource('Local service unavailable · retrying');}}}
  finally{busy=false;controller=null;schedule();}
}
// A hidden tab polls every 30 s, so its title, icon and notification still learn about Needs you; a 304 costs little.
// While the service does not answer at all, a visible tab tries every 4 s, so it soon learns that the service is
// back, or that this view lost access; an error answer backs off up to 30 s.
function schedule(){clearTimeout(timer);if(token)timer=setTimeout(refresh,document.hidden?30000:Math.min(answered?30000:4000,2000*2**Math.min(failures,4)));}
// After a restart, the service may run another version or other view files than this page: say so, with a Reload
// button. A reload keeps the view token, which is in the tab's session storage.
function updated(assets) {
  const same=version===pageVersion&&assets===pageAssets;$('update-note').hidden=same;
  if(!same)setText($('update-text'),`a-stack was updated${version!==pageVersion?' to '+version:''}. Reload to get the new view.`);
}
$('reload').onclick=()=>location.reload();
document.addEventListener('visibilitychange',()=>{document.documentElement.classList.toggle('tab-hidden',document.hidden);if(document.hidden){if(!busy)schedule();}else{clearTimeout(timer);render();refresh();}});
setInterval(()=>{if(!document.hidden&&model){updateSource();updateRuntimeAges();updateNowAges();}},1000);
patchNotify();
render();
connect();

})();
