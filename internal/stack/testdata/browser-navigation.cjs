// Run the production navigation code, not a second implementation of its decisions.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('browser/app.js', 'utf8');
const listeners = {}, elements = new Map(), cards = new Map();
const document = {
  activeElement: null, modal: false,
  addEventListener: (type, fn) => { (listeners[type] ||= []).push(fn); },
  querySelector: () => document.modal ? {} : null,
};
function node(id, root = '', kind = '') {
  const n = {
    id, root, kind, dataset: { openTask: id }, isContentEditable: false,
    contains: other => other === n || other.root === id,
    closest(selector) {
      if (selector.includes('input')) return n.field ? n : null;
      if (selector.includes('[inert]')) return cards.get(id)?.article.inert ? n : null;
      return selector.includes('.task-card') && kind === 'card' || selector.includes('.task-row') && kind === 'row' ? n : null;
    },
    querySelector: () => n.dataset.status === 'needs-you' ? n : null,
    focus() { document.activeElement = n; for (const fn of listeners.focusin || []) fn({ target: n }); },
    scrollIntoView(options) { n.scroll = options; },
    click() { n.clicks = (n.clicks || 0) + 1; },
    showModal() { n.opens = (n.opens || 0) + 1; },
  };
  elements.set(id, n);
  return n;
}
const $ = id => elements.get(id) || node(id);
const panel = node('graph-panel'), agentsPanel = node('agents-panel'), nowPanel = node('now-panel'), peekBox = node('peek');
for (const id of ['a', 'b', 'c', 'd', 'e']) cards.set(id, { button: node(id, panel.id, 'card'), article: { inert: false } });
const list = [...cards.values()].map(c => c.button);
const rows = [node('r1', agentsPanel.id, 'row'), node('r2', agentsPanel.id, 'row')];
rows.forEach(r => { r.dataset.status = 'needs-you'; });
agentsPanel.querySelectorAll = () => rows;
const state = { view: 'graph', focus: null }, barNames = { top: '', session: '', footer: '' }, collapsedBars = { top: false, session: false, footer: false };
let toggles = 0;
const env = {
  $, document, panel, agentsPanel, nowPanel, peekBox, cards, state, barNames, collapsedBars,
  model: { dependencies: [['a','c'], ['b','c'], ['c','d'], ['c','e'], ['a','d'], ['missing','c']].map(([prerequisite_id, dependent_id]) => ({ prerequisite_id, dependent_id })) },
  positions: new Map([['a',{x:0,y:0}], ['b',{x:0,y:160}], ['c',{x:400,y:140}], ['d',{x:800,y:170}], ['e',{x:800,y:0}]]),
  ports: new Map([['a',100], ['b',10], ['c',10], ['d',10], ['e',10]]),
  layoutCards: () => list.filter(b => !cards.get(b.id).article.inert),
  render() {}, peekID: null, peek() {}, closePeek() {}, mapOn: true, setMinimap() {},
  requestAnimationFrame: fn => fn(),
  toggleBars(names, hidden) { toggles++; for (const name of names) collapsedBars[name] = hidden; },
};
const context = vm.createContext(env);
vm.runInContext(source.match(/function setFocus\(id\)\s*\{[^{}]*\}/)[0] + '\n' + source.slice(source.indexOf('function goTo(id)'), source.indexOf('async function loadDetail()')), context);
function focus(id) { $(id).focus(); }
function key(key, extra = {}) {
  let prevented = false;
  const event = { key, target: document.activeElement, preventDefault: () => { prevented = true; }, ...extra };
  for (const fn of listeners.keydown) fn(event);
  return prevented;
}
const active = () => document.activeElement.id;
focus('c'); key('h'); assert.equal(active(), 'b', 'h chooses nearest linked port');
focus('c'); key('H'); assert.equal(active(), 'b');
key('Shift'); key('H'); assert.equal(active(), 'a', 'H keeps the original anchor');
key('H'); assert.equal(active(), 'b', 'H wraps');
key('l'); assert.equal(active(), 'c', 'unshifted motion resets the cycle');
key('L'); assert.equal(active(), 'd'); key('L'); assert.equal(active(), 'e'); key('L'); assert.equal(active(), 'd');
key('h'); assert.equal(active(), 'c');
focus('d'); cards.get('c').article.inert = true; key('h'); assert.equal(active(), 'a', 'use implied direct links, not just the reduced graph');
cards.get('c').article.inert = false;
env.positions.get('b').y = 220; focus('c'); key('h'); assert.equal(active(), 'a', 'nearest uses card centres, not unit tops'); env.positions.get('b').y = 160;
focus('c'); cards.get('b').article.inert = true; key('h'); assert.equal(active(), 'a', 'skip filtered cards');
key('h'); assert.equal(active(), 'a'); assert.match($('navigation-hint').textContent, /No visible prerequisite/);
cards.get('b').article.inert = false;
focus('e'); key('l'); assert.equal(active(), 'e'); assert.match($('navigation-hint').textContent, /No visible dependent/);
focus('c'); key('G'); assert.equal(active(), 'e'); key('g'); key('g'); assert.equal(active(), 'a');
key('k'); assert.equal(active(), 'a'); key('j'); assert.equal(active(), 'b');
key('z'); key('z'); assert.equal($('b').scroll.block, 'center'); key('c'); assert.equal($('b').scroll.inline, 'center');
$('b').dataset.status = $('d').dataset.status = 'needs-you';
focus('a'); key(']'); assert.equal(active(), 'b'); key('u'); assert.equal(active(), 'd'); key(']'); assert.equal(active(), 'b'); key('['); assert.equal(active(), 'd');
key('t'); assert.equal(toggles, 1); assert.ok(Object.values(collapsedBars).every(Boolean));
key('t', { repeat: true }); assert.equal(toggles, 1, 'holding t does not toggle repeatedly');
key('/'); assert.equal(active(), 'search'); assert.equal(collapsedBars.session, false, '/ reveals hidden search');
focus('c'); key('g'); key('v'); assert.equal(active(), 'graph-panel'); assert.equal($('graph-tab').clicks, 1);
key('g'); key('a'); assert.equal(active(), 'agents-panel'); key(']'); assert.equal(active(), 'r1'); key('['); assert.equal(active(), 'r2');
key('g'); key('n'); assert.equal(active(), 'now-panel');
for (const root of ['now-panel', 'search', 'keys-button', 'footer-content']) {
  focus(root); for (const k of ['h','l','H','L','g','G','z','c','t','[',']','?']) assert.equal(key(k), false, `no key outside panels: ${root} ${k}`);
}
for (const prop of ['field', 'isContentEditable']) {
  const input = node(prop, panel.id); input[prop] = true; input.focus();
  for (const k of ['h','l','H','L','g','G','z','c','t','[',']','?']) assert.equal(key(k), false, `no key while editing: ${k}`);
}
focus('c');
for (const flag of ['ctrlKey','altKey','metaKey','isComposing']) for (const k of ['h','d','u','t','g']) assert.equal(key(k, { [flag]: true }), false);
document.modal = true; assert.equal(key('t'), false); document.modal = false;
key('g'); focus('b'); key('g'); assert.equal(active(), 'b', 'a focus change cancels a sequence');
console.log('Browser navigation: nearest links, anchored cycles, filters, boundaries, sequences, Needs you, bars and key scope passed.');
