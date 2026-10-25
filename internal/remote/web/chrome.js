// The app's interface (bars, lists, buttons, notices) in the colors of the
// terminal's theme: the Theme message (fg, bg, cursor, 16 base colors) as
// CSS custom properties. No DOM here but applyChrome: node tests the rest.

// CHROME_DEFAULTS are app.css's :root colors, kept when no theme is known.
export const CHROME_DEFAULTS = Object.freeze({
  '--bg': '#0b0a1a', '--panel': '#151230', '--line': '#2c2558', '--fg': '#e8e0ff', '--dim': '#8a80b0',
  '--pink': '#ff2e88', '--on-pink': '#ffffff', '--cyan': '#00f0ff',
  '--ok': '#4ade80', '--warn': '#fbbf24', '--ko': '#f87171', '--on-ko': '#000000',
});

// rgb returns a valid [r, g, b] of integers 0..255, or null.
function rgb(c) {
  if (!Array.isArray(c) || c.length !== 3) return null;
  for (const v of c) if (!Number.isInteger(v) || v < 0 || v > 255) return null;
  return c;
}

const hex = c => '#' + c.map(v => v.toString(16).padStart(2, '0')).join('');
const mix = (a, b, t) => a.map((v, i) => Math.round(v * t + b[i] * (1 - t)));

function lum(c) {
  const [r, g, b] = c.map(v => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

const parse = h => [1, 3, 5].map(i => parseInt(h.slice(i, i + 2), 16));

// contrast is the WCAG contrast ratio of two #rrggbb colors.
export function contrast(a, b) {
  const x = lum(parse(a)), y = lum(parse(b));
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

// onColor is black or white, whichever reads better on c.
function onColor(c) {
  return contrast(hex(c), '#000000') >= contrast(hex(c), '#ffffff') ? '#000000' : '#ffffff';
}

// chromeVars turns a Theme message into the CSS variables of app.css, or
// null when its colors are not valid.
export function chromeVars(t) {
  if (!t || typeof t !== 'object') return null;
  const fg = rgb(t.fg), bg = rgb(t.bg), cur = rgb(t.cursor);
  if (!fg || !bg || !cur) return null;
  const ansi = i => (Array.isArray(t.ansi) ? rgb(t.ansi[i]) : null);
  const pick = (i, name) => (ansi(i) ? hex(ansi(i)) : CHROME_DEFAULTS[name]);
  const ko = pick(1, '--ko');
  return {
    '--bg': hex(bg),
    '--panel': hex(mix(fg, bg, 0.07)),
    '--line': hex(mix(fg, bg, 0.2)),
    '--fg': hex(fg),
    '--dim': hex(mix(fg, bg, 0.6)),
    '--pink': hex(cur),
    '--on-pink': onColor(cur),
    '--cyan': pick(6, '--cyan'),
    '--ok': pick(2, '--ok'),
    '--warn': pick(3, '--warn'),
    '--ko': ko,
    '--on-ko': onColor(parse(ko)),
  };
}

// applyChrome sets the variables on :root (style.setProperty only, every
// value a #rrggbb built above) and the status bar color. Without vars, the
// defaults come back.
export function applyChrome(vars) {
  const v = vars || CHROME_DEFAULTS;
  const root = document.documentElement.style;
  for (const [k, val] of Object.entries(v)) {
    if (k in CHROME_DEFAULTS && /^#[0-9a-f]{6}$/.test(val)) root.setProperty(k, val);
  }
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta && /^#[0-9a-f]{6}$/.test(v['--bg'])) meta.setAttribute('content', v['--bg']);
}
