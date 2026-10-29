// DOM helpers. Text from the daemon (session names, directories, agent
// messages, machine names) only ever goes through textContent.

export const $ = id => document.getElementById(id);

export function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined && text !== null) e.textContent = String(text);
  return e;
}

const SCREENS = ['home', 'pair', 'term'];

export function show(name) {
  for (const s of SCREENS) $(s).hidden = s !== name;
}

// notice shows a message with buttons; each action is {label, fn, primary}.
// The button handlers run inside the tap, so they may start WebAuthn.
export function notice(text, actions = []) {
  $('notice-text').textContent = text;
  const box = $('notice-actions');
  box.replaceChildren();
  for (const a of actions) {
    const b = el('button', a.primary ? 'primary' : '', a.label);
    b.type = 'button';
    b.addEventListener('click', () => { hideNotice(); a.fn(); });
    box.append(b);
  }
  $('notice').hidden = false;
}

export function hideNotice() { $('notice').hidden = true; }

export function status(id, text) {
  const s = $(id);
  s.textContent = text || '';
  s.hidden = !text;
}

// ---- machine list: name, dot (result of the last real connection),
// details button, swipe left to forget

export function renderMachines(list, { onOpen, onForget, onDetails }) {
  const ul = $('machines');
  ul.replaceChildren();
  for (const m of list) {
    const li = el('li', 'machine');
    const forget = el('button', 'forget');
    forget.append(el('span', 'bin'), el('span', 'lbl', 'Forget'));
    forget.addEventListener('click', () => onForget(m));
    const row = el('div', 'row');
    const r = m.reach;
    row.append(el('span', 'dot ' + (r ? (r.ok ? 'up' : 'down') : 'unknown')));
    const txt = el('div', 'txt');
    txt.append(el('div', 'name', m.name || '?'), el('div', 'sub', (m.addrs || []).join('  ')));
    row.append(txt);
    const info = el('button', 'info', 'ⓘ');
    info.type = 'button';
    info.setAttribute('aria-label', 'Details');
    info.addEventListener('click', e => { e.stopPropagation(); onDetails(m); });
    row.append(info);
    li.append(forget, row);
    swipe(li, row, () => onOpen(m));
    ul.append(li);
  }
}

// OPEN is how far an open row slides to show Forget (.machine --open in
// app.css). Past it the row follows the finger with resistance.
const OPEN = 104;

function swipe(li, row, onTap) {
  let x0 = null, y0 = 0, dx = 0, dir = '';
  const set = x => {
    row.style.transform = `translateX(${x}px)`;
    li.style.setProperty('--p', Math.min(1, -x / OPEN).toFixed(3));
  };
  row.addEventListener('touchstart', e => {
    x0 = e.touches[0].clientX; y0 = e.touches[0].clientY; dx = 0; dir = '';
  }, { passive: true });
  row.addEventListener('touchmove', e => {
    if (x0 === null) return;
    const mx = e.touches[0].clientX - x0, my = e.touches[0].clientY - y0;
    if (!dir && Math.hypot(mx, my) > 8) dir = Math.abs(mx) > Math.abs(my) ? 'x' : 'y';
    if (dir !== 'x') return;
    li.classList.add('dragging');
    dx = mx;
    let x = Math.min(0, mx + (li.classList.contains('open') ? -OPEN : 0));
    if (x < -OPEN) x = -OPEN - (-x - OPEN) / 3;
    set(x);
  }, { passive: true });
  row.addEventListener('touchend', () => {
    if (x0 === null) return;
    x0 = null;
    li.classList.remove('dragging');
    row.style.transform = '';
    li.style.removeProperty('--p');
    if (dir !== 'x') return;
    const open = li.classList.contains('open');
    li.classList.toggle('open', open ? dx < 40 : dx < -50);
  });
  row.addEventListener('click', () => {
    if (li.classList.contains('open')) { li.classList.remove('open'); return; }
    if (dir !== 'x') onTap();
  });
}

// renderDetails fills the details sheet with rows [label, value].
export function renderDetails(name, rows) {
  $('mdetail-title').textContent = name || '?';
  const dl = $('mdetail-rows');
  dl.replaceChildren();
  for (const [k, v] of rows) dl.append(el('dt', '', k), el('dd', '', v));
}

// ---- theme pickers (home screen and session sheet): "Follow the
// desktop", then the app's themes

export function renderThemes(selectId, rowId, themes, current) {
  const sel = $(selectId);
  const opts = [el('option', '', 'Follow the desktop')];
  opts[0].value = '';
  for (const t of themes) {
    const o = el('option', '', t.label);
    o.value = t.name;
    opts.push(o);
  }
  sel.replaceChildren(...opts);
  sel.value = current;
  $(rowId).hidden = !themes.length;
}

// ---- session list: garage mark, agent state badge, blocked agents first

export function agentBadge(a) {
  if (!a || typeof a !== 'object') return null;
  const msg = typeof a.msg === 'string' ? a.msg : '';
  switch (a.state) {
    case 'blocked': {
      const k = { permission: 'permission', question: 'question', auth: 'login' }[a.kind] || 'waiting';
      return { text: msg ? k + ': ' + msg : k, cls: 'blocked' };
    }
    case 'working': return { text: msg || 'working', cls: 'working' };
    case 'done': return { text: msg || 'done', cls: 'done' };
    case 'error': return { text: msg || 'error', cls: 'error' };
    default: return { text: 'ready', cls: 'idle' };
  }
}

// onNew, when set, adds a "New terminal" entry first; pending: one is
// being opened.
export function renderSessions(list, current, onPick, onNew, pending) {
  const ul = $('sessions');
  ul.replaceChildren();
  if (onNew) {
    const li = el('li', 'session new' + (pending ? ' pending' : ''));
    li.append(el('div', 'name', pending ? 'Opening a terminal…' : '+ New terminal'));
    li.addEventListener('click', () => { if (!pending) onNew(); });
    ul.append(li);
  }
  const blocked = s => (s.agent && s.agent.state === 'blocked' ? 0 : 1);
  const sorted = list.map((s, i) => [s, i]).sort((a, b) => blocked(a[0]) - blocked(b[0]) || a[1] - b[1]).map(x => x[0]);
  for (const s of sorted) {
    const li = el('li', 'session' + (s.key === current ? ' current' : ''));
    const head = el('div', 'head');
    if (s.garage) head.append(el('span', 'garage', 'garage'));
    head.append(el('span', 'name', s.name || s.key));
    li.append(head);
    if (s.cwd) li.append(el('div', 'sub', s.cwd));
    const b = agentBadge(s.agent);
    if (b) li.append(el('div', 'badge ' + b.cls, b.text));
    li.addEventListener('click', () => onPick(s.key));
    ul.append(li);
  }
}
