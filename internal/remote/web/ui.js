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

// shortPath writes a home directory as ~.
export function shortPath(p) {
  return typeof p === 'string' ? p.replace(/^\/(?:home|Users)\/[^/]+(?=\/|$)/, '~') : '';
}

// waitText says how long an agent has been waiting since an ISO date, or ''.
export function waitText(since, now) {
  const t = Date.parse(since);
  if (!Number.isFinite(t) || t <= 0) return '';
  const min = Math.floor((now - t) / 60000);
  if (min < 1) return 'just now';
  if (min < 60) return min + ' min';
  const h = Math.floor(min / 60);
  return h < 48 ? h + ' h' : Math.floor(h / 24) + ' d';
}

const agentKind = a => ({ permission: 'permission', question: 'question', auth: 'login' }[a.kind] || 'waiting');

export function agentBadge(a, now = Date.now()) {
  if (!a || typeof a !== 'object') return null;
  const msg = typeof a.msg === 'string' ? a.msg : '';
  switch (a.state) {
    case 'blocked': {
      const k = agentKind(a);
      const wait = waitText(a.since, now);
      return { text: (wait ? wait + ' · ' : '') + (msg ? k + ': ' + msg : k), cls: 'blocked' };
    }
    case 'working': return { text: msg || 'working', cls: 'working' };
    case 'done': return { text: msg || 'done', cls: 'done' };
    case 'error': return { text: msg || 'error', cls: 'error' };
    default: return { text: 'ready', cls: 'idle' };
  }
}

export function sortedSessions(list) {
  if (!Array.isArray(list)) return [];
  const blocked = s => (s && s.agent && s.agent.state === 'blocked' ? 0 : 1);
  return list.map((s, i) => [s, i]).sort((a, b) => blocked(a[0]) - blocked(b[0]) || a[1] - b[1]).map(x => x[0]);
}

// agentClass is the state of an agent as a class of the title's dot.
export function agentClass(a) {
  const s = a && a.state;
  return ['blocked', 'working', 'done', 'error'].includes(s) ? s : a ? 'idle' : '';
}

// panePosition says where current is in the order of the double tap, as
// "2/3", or '' with a single terminal.
export function panePosition(list, current) {
  const sorted = sortedSessions(list);
  const i = sorted.findIndex(s => s && s.key === current);
  return i < 0 || sorted.length < 2 ? '' : `${i + 1}/${sorted.length}`;
}

// blockedElsewhere is the banner above the terminal: the first agent waiting
// in another terminal, and how many more do. null when there is none.
export function blockedElsewhere(list, current) {
  const others = sortedSessions(list).filter(s => s && s.key !== current && s.agent && s.agent.state === 'blocked');
  if (!others.length) return null;
  const s = others[0];
  const more = others.length > 1 ? ` (+${others.length - 1})` : '';
  return { key: s.key, text: `${s.name || 'A terminal'} is waiting: ${agentKind(s.agent)}${more}` };
}

const MAX_REPLY = 22; // characters of a reply button

// quickReplies finds on the screen what a waiting agent can be answered with:
// the numbered menu closest to the bottom ("❯ 1. Yes", "  2. No"), else a
// "(y/n)". Each entry is what the button shows and the text it sends. A menu
// needs no enter. Nothing is guessed: no menu on screen, no buttons.
export function quickReplies(agent, text) {
  if (!agent || agent.state !== 'blocked' || typeof text !== 'string') return [];
  const lines = text.split('\n').slice(-24);
  const item = /^[\s│|]*(?:[❯>›▶]\s*)?(\d)[.)]\s+(\S.*?)[\s│|]*$/;
  for (let i = lines.length - 1; i >= 0; i--) {
    const m = item.exec(lines[i]);
    if (!m) continue;
    // The last item of a menu: walk up to its first.
    const found = [{ n: Number(m[1]), label: m[2] }];
    for (let j = i - 1; j >= 0 && found[0].n > 1; j--) {
      const u = item.exec(lines[j]);
      if (!u) continue; // a long item may wrap over several lines
      if (Number(u[1]) !== found[0].n - 1) break;
      found.unshift({ n: Number(u[1]), label: u[2] });
    }
    if (found.length < 2 || found[0].n !== 1) break;
    return found.slice(0, 6).map(f => ({
      label: `${f.n} ${f.label.length > MAX_REPLY ? f.label.slice(0, MAX_REPLY - 1) + '…' : f.label}`,
      send: String(f.n),
    }));
  }
  const tail = lines.slice(-6).join('\n');
  if (/[(\[]\s*y(es)?\s*\/\s*n(o)?\s*[)\]]/i.test(tail)) {
    return [{ label: 'y', send: 'y\n' }, { label: 'n', send: 'n\n' }];
  }
  return [];
}

// detectLimit scans the terminal screen text for Claude Code or AGY/Antigravity
// usage limits. If found, returns an object with app info, reset time, suggested
// resume time (reset + 1 min), and raw text.
export function detectLimit(text, now = new Date()) {
  if (typeof text !== 'string' || !text.trim()) return null;
  const lines = text.split('\n');

  // 1. "resets in 1 hour 45 minutes" or "resets in 45m" or "resets in 2h"
  const reResetIn = /resets?\s+in\s+(?:(\d+)\s*h(?:ours?)?)?\s*(?:(\d+)\s*m(?:in(?:utes?)?)?)?/i;
  // 2. "retry after 60s" or "try again in 10 minutes" or "retry in 5m"
  const reRetryAfter = /(?:retry after|try again in|retry in)\s+(\d+)\s*(s(?:ec(?:onds?)?)?|m(?:in(?:utes?)?)?|h(?:ours?)?)/i;
  // 3. "resets 5:20pm (Europe/Paris)" or "resets 17:30" or "reset at 5:20 PM" or "resets 5pm"
  const reResetTime = /(?:resets?|reset at)\s+(\d{1,2}(?::\d{2})?\s*(?:am|pm)?)/i;
  // 4. Status bar clock: "↻ 17:20" or "↻ 5:20pm" or "reset: 17:20"
  const reResetClock = /(?:↻|reset:)\s*(\d{1,2}:\d{2}\s*(?:am|pm)?)/i;

  const detectApp = () => {
    const lower = text.toLowerCase();
    if (lower.includes('claude') || lower.includes('5-hour limit')) return { app: 'claude', appLabel: 'Claude' };
    if (lower.includes('antigravity') || lower.includes('agy') || lower.includes('gemini')) return { app: 'agy', appLabel: 'Antigravity' };
    return { app: 'agent', appLabel: 'Agent' };
  };

  const pad2 = n => String(n).padStart(2, '0');

  // Scan from bottom to top (most recent output first)
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = lines[i].trim();
    if (!line) continue;

    // 1. Relative "resets in Xh Ym"
    const mIn = reResetIn.exec(line);
    if (mIn && (mIn[1] || mIn[2])) {
      const hours = Number(mIn[1] || 0);
      const mins = Number(mIn[2] || 0);
      const ms = (hours * 60 + mins) * 60 * 1000;
      if (ms > 0) {
        const resumeDate = new Date(now.getTime() + ms + 60 * 1000);
        const resumeTime = `${pad2(resumeDate.getHours())}:${pad2(resumeDate.getMinutes())}`;
        const rawTime = mIn[0].replace(/^resets?\s+/i, '');
        const { app, appLabel } = detectApp();
        return {
          app,
          appLabel,
          rawTime,
          resumeTime,
          message: line,
          command: 'continue',
        };
      }
    }

    // 2. "retry after / try again in X s/m/h"
    const mRetry = reRetryAfter.exec(line);
    if (mRetry) {
      const val = Number(mRetry[1]);
      const unit = mRetry[2].toLowerCase();
      let ms = 0;
      if (unit.startsWith('s')) ms = val * 1000;
      else if (unit.startsWith('m')) ms = val * 60 * 1000;
      else if (unit.startsWith('h')) ms = val * 60 * 60 * 1000;
      if (ms > 0) {
        const resumeDate = new Date(now.getTime() + ms + 60 * 1000);
        const resumeTime = `${pad2(resumeDate.getHours())}:${pad2(resumeDate.getMinutes())}`;
        const rawTime = mRetry[0];
        const { app, appLabel } = detectApp();
        return {
          app,
          appLabel,
          rawTime,
          resumeTime,
          message: line,
          command: 'continue',
        };
      }
    }

    // 3. Absolute clock time: "resets 5:20pm" or "reset at 17:30"
    const mTime = reResetTime.exec(line);
    if (mTime) {
      const raw = mTime[1].trim();
      const resumeTime = parseAndAddMinute(raw);
      if (resumeTime) {
        const { app, appLabel } = detectApp();
        return {
          app,
          appLabel,
          rawTime: raw,
          resumeTime,
          message: line,
          command: 'continue',
        };
      }
    }

    // 4. Status clock: "↻ 17:20"
    const mClock = reResetClock.exec(line);
    if (mClock) {
      const raw = mClock[1].trim();
      const resumeTime = parseAndAddMinute(raw);
      if (resumeTime) {
        const { app, appLabel } = detectApp();
        return {
          app,
          appLabel,
          rawTime: raw,
          resumeTime,
          message: line,
          command: 'continue',
        };
      }
    }
  }

  return null;
}

function parseAndAddMinute(timeStr) {
  let clean = timeStr.toLowerCase().trim();
  const isPM = clean.endsWith('pm');
  const isAM = clean.endsWith('am');
  clean = clean.replace(/am|pm/, '').trim();
  const parts = clean.split(':').map(Number);
  let h = parts[0];
  let min = parts.length > 1 ? parts[1] : 0;
  if (isNaN(h) || isNaN(min)) return null;
  if (isPM && h < 12) h += 12;
  if (isAM && h === 12) h = 0;
  min += 1;
  if (min >= 60) {
    min = 0;
    h = (h + 1) % 24;
  }
  return `${String(h).padStart(2, '0')}:${String(min).padStart(2, '0')}`;
}

export function nextSessionKey(list, current) {
  const sorted = sortedSessions(list);
  if (sorted.length <= 1) return null;
  const idx = sorted.findIndex(s => s && s.key === current);
  const next = sorted[(idx + 1) % sorted.length];
  return (next && next.key) || null;
}

export function prevSessionKey(list, current) {
  const sorted = sortedSessions(list);
  if (sorted.length <= 1) return null;
  const idx = sorted.findIndex(s => s && s.key === current);
  const prevIdx = idx <= 0 ? sorted.length - 1 : idx - 1;
  const prev = sorted[prevIdx];
  return (prev && prev.key) || null;
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
  const sorted = sortedSessions(list);
  for (const s of sorted) {
    const li = el('li', 'session' + (s.key === current ? ' current' : ''));
    const head = el('div', 'head');
    if (s.garage) head.append(el('span', 'garage', 'garage'));
    head.append(el('span', 'name', s.name || s.key));
    li.append(head);
    if (s.cwd) li.append(el('div', 'sub', shortPath(s.cwd)));
    const b = agentBadge(s.agent);
    if (b) li.append(el('div', 'badge ' + b.cls, b.text));
    li.addEventListener('click', () => onPick(s.key));
    ul.append(li);
  }
}
