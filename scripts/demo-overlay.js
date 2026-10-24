// Annotations for the README demo. scripts/record-demo.sh injects this file
// into its recording browser with agent-browser eval; it is not part of the
// a-stack view. It adds window.demo:
//
//   demo.mark(target, text)  outline target, point a callout at it
//   demo.clear()             remove the outline and the callout
//   demo.zoom(target, scale) zoom the page so target is in view; null zooms out
//   demo.tab(name)           click a tab with a ripple; answers the tab it chose
//   demo.ready(target, text) true when target is on screen and shows text
//
// A target is {task: 'Task title'} for a Task card on the open tab, or
// {css: 'selector'} for any other element. The outline and the callout follow
// the element on every frame, so they stay on a card that moves.
(() => {
  if (window.demo) return 'ok';
  const accent = '#f25f1c';
  const style = document.createElement('style');
  style.textContent = `
    html, body { overflow: hidden !important; }
    .app { transform-origin: 0 0; transition: transform .5s cubic-bezier(.45, 0, .2, 1); }
    #demo-layer { position: fixed; inset: 0; z-index: 2147483647; pointer-events: none; }
    #demo-layer svg { position: absolute; inset: 0; width: 100%; height: 100%; overflow: visible; }
    .demo-ring { position: absolute; border: 4px solid ${accent}; border-radius: 14px;
      animation: demo-pulse 1s ease-out 2; }
    @keyframes demo-pulse {
      from { box-shadow: 0 0 0 0 ${accent}aa; }
      to { box-shadow: 0 0 0 18px ${accent}00; }
    }
    .demo-bubble { position: absolute; width: max-content; max-width: 440px; padding: 10px 16px; border-radius: 10px;
      background: #fff; color: #1d2125; border: 3px solid ${accent};
      font: 600 22px/1.3 Inter, ui-sans-serif, system-ui, sans-serif; letter-spacing: -.2px;
      box-shadow: 0 8px 28px #0005; animation: demo-in .35s ease-out; }
    @keyframes demo-in { from { opacity: 0; transform: translateY(6px); } }
    .demo-ripple { position: absolute; width: 56px; height: 56px; margin: -28px 0 0 -28px;
      border-radius: 50%; border: 4px solid ${accent}; animation: demo-ripple .7s ease-out forwards; }
    @keyframes demo-ripple { from { transform: scale(.3); opacity: 1; } to { transform: scale(1.4); opacity: 0; } }
  `;
  document.head.append(style);
  const layer = document.createElement('div');
  layer.id = 'demo-layer';
  const svgNS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(svgNS, 'svg');
  svg.innerHTML = `<defs><marker id="demo-head" viewBox="0 0 10 10" refX="7" refY="5"
    markerWidth="5" markerHeight="5" orient="auto-start-reverse">
    <path d="M0 0L10 5L0 10z" fill="${accent}"/></marker></defs>`;
  const arrow = document.createElementNS(svgNS, 'path');
  arrow.setAttribute('fill', 'none');
  arrow.setAttribute('stroke', accent);
  arrow.setAttribute('stroke-width', '5');
  arrow.setAttribute('stroke-linecap', 'round');
  arrow.setAttribute('marker-end', 'url(#demo-head)');
  svg.append(arrow);
  layer.append(svg);
  document.body.append(layer);

  const app = document.querySelector('.app');
  let mark = null;
  let view = { scale: 1, x: 0, y: 0 };

  function find(target) {
    if (!target) return null;
    if (target.css) return document.querySelector(target.css);
    for (const card of document.querySelectorAll('.view:not([hidden]) .task-card')) {
      const title = card.querySelector('.task-title');
      if (title && title.textContent.trim() === target.task) return card;
    }
    return null;
  }

  function clear() {
    if (mark) {
      mark.ring.remove();
      mark.bubble.remove();
    }
    mark = null;
    arrow.setAttribute('d', '');
  }

  // Place the callout on the side of the element with the most room, a gap
  // away, and draw a curved arrow from the callout to the element.
  function place() {
    if (mark) {
      const el = find(mark.target);
      const show = el ? '' : 'none';
      mark.ring.style.display = show;
      mark.bubble.style.display = show;
      if (!el) {
        arrow.setAttribute('d', '');
      } else {
        const r = el.getBoundingClientRect();
        const pad = 7;
        Object.assign(mark.ring.style, {
          left: `${r.left - pad}px`, top: `${r.top - pad}px`,
          width: `${r.width + 2 * pad}px`, height: `${r.height + 2 * pad}px`,
        });
        const w = innerWidth, h = innerHeight, gap = 70;
        const b = mark.bubble.getBoundingClientRect();
        const room = { below: h - r.bottom, above: r.top, right: w - r.right, left: r.left };
        const side = mark.side || Object.keys(room).sort((p, q) => room[q] - room[p])[0];
        let bx, by, ax, ay, tx, ty;
        if (side === 'below' || side === 'above') {
          bx = Math.min(Math.max(r.left + r.width / 2 - b.width / 2 + 60, 12), w - b.width - 12);
          by = side === 'below' ? r.bottom + gap : r.top - gap - b.height;
          ax = bx + Math.min(40, b.width / 4);
          ay = side === 'below' ? by : by + b.height;
          tx = Math.min(Math.max(ax + 30, r.left + 20), r.right - 20);
          ty = side === 'below' ? r.bottom + pad + 8 : r.top - pad - 8;
        } else {
          bx = side === 'right' ? r.right + gap : r.left - gap - b.width;
          by = Math.min(Math.max(r.top + r.height / 2 - b.height / 2 + 40, 12), h - b.height - 12);
          ax = side === 'right' ? bx : bx + b.width;
          ay = by + b.height / 2;
          tx = side === 'right' ? r.right + pad + 8 : r.left - pad - 8;
          ty = Math.min(Math.max(ay - 30, r.top + 14), r.bottom - 14);
        }
        mark.bubble.style.left = `${bx}px`;
        mark.bubble.style.top = `${by}px`;
        const mx = (ax + tx) / 2 + (ty - ay) * 0.25, my = (ay + ty) / 2 - (tx - ax) * 0.25;
        arrow.setAttribute('d', `M${ax} ${ay}Q${mx} ${my} ${tx} ${ty}`);
      }
    }
    requestAnimationFrame(place);
  }
  requestAnimationFrame(place);

  window.demo = {
    mark(target, text, side) {
      clear();
      const ring = document.createElement('div');
      ring.className = 'demo-ring';
      const bubble = document.createElement('div');
      bubble.className = 'demo-bubble';
      bubble.textContent = text;
      layer.append(ring, bubble);
      mark = { target, ring, bubble, side };
      return 'ok';
    },
    clear() { clear(); return 'ok'; },
    zoom(target, scale) {
      const el = find(target);
      if (!el || !scale || scale <= 1) {
        view = { scale: 1, x: 0, y: 0 };
      } else {
        const r = el.getBoundingClientRect();
        // The element's place without the current zoom.
        const cx = (r.left + r.width / 2 - view.x) / view.scale;
        const cy = (r.top + r.height / 2 - view.y) / view.scale;
        const w = innerWidth, h = innerHeight;
        const clamp = (v, low) => Math.min(0, Math.max(low, v));
        view = {
          scale,
          x: clamp(w / 2 - cx * scale, w - w * scale),
          y: clamp(h / 2 - cy * scale, h - h * scale),
        };
      }
      app.style.transform = `translate(${view.x}px, ${view.y}px) scale(${view.scale})`;
      return 'ok';
    },
    tab(name) {
      const tab = document.getElementById(`${name}-tab`) || document.getElementById('graph-tab');
      if (tab.getAttribute('aria-selected') !== 'true') {
        const r = tab.getBoundingClientRect();
        const ripple = document.createElement('div');
        ripple.className = 'demo-ripple';
        ripple.style.left = `${r.left + r.width / 2}px`;
        ripple.style.top = `${r.top + r.height / 2}px`;
        layer.append(ripple);
        setTimeout(() => ripple.remove(), 800);
        tab.click();
      }
      return tab.dataset.view;
    },
    ready(target, text) {
      const el = find(target);
      return Boolean(el && el.getBoundingClientRect().width > 0 &&
        (!text || el.textContent.includes(text)));
    },
  };
  return 'ok';
})();
