// Box drawing (U+2500 to U+257F) and blocks (U+2580 to U+259F) drawn with
// fillRect so the lines join from one cell to the next, whatever the font.
// A port of internal/gfx/boxdraw.go (smooth mode): keep the two in step.
// No DOM here: node runs the coverage check (selftest.mjs).

// up, right, down, left arms: 0 none, 1 light, 2 heavy, 3 double.
const ARMS = {
  '─': '0101', '━': '0202', '│': '1010', '┃': '2020',
  '┌': '0110', '┍': '0210', '┎': '0120', '┏': '0220',
  '┐': '0011', '┑': '0012', '┒': '0021', '┓': '0022',
  '└': '1100', '┕': '1200', '┖': '2100', '┗': '2200',
  '┘': '1001', '┙': '1002', '┚': '2001', '┛': '2002',
  '├': '1110', '┝': '1210', '┞': '2110', '┟': '1120', '┠': '2120', '┡': '2210', '┢': '1220', '┣': '2220',
  '┤': '1011', '┥': '1012', '┦': '2011', '┧': '1021', '┨': '2021', '┩': '2012', '┪': '1022', '┫': '2022',
  '┬': '0111', '┭': '0112', '┮': '0211', '┯': '0212', '┰': '0121', '┱': '0122', '┲': '0221', '┳': '0222',
  '┴': '1101', '┵': '1102', '┶': '1201', '┷': '1202', '┸': '2101', '┹': '2102', '┺': '2201', '┻': '2202',
  '┼': '1111', '┽': '1112', '┾': '1211', '┿': '1212', '╀': '2111', '╁': '1121', '╂': '2121',
  '╃': '2112', '╄': '2211', '╅': '1122', '╆': '1221', '╇': '2212', '╈': '1222', '╉': '2122', '╊': '2221', '╋': '2222',
  '═': '0303', '║': '3030',
  '╒': '0310', '╓': '0130', '╔': '0330', '╕': '0013', '╖': '0031', '╗': '0033',
  '╘': '1300', '╙': '3100', '╚': '3300', '╛': '1003', '╜': '3001', '╝': '3003',
  '╞': '1310', '╟': '3130', '╠': '3330', '╡': '1013', '╢': '3031', '╣': '3033',
  '╤': '0313', '╥': '0131', '╦': '0333', '╧': '1303', '╨': '3101', '╩': '3303',
  '╪': '1313', '╫': '3131', '╬': '3333',
  '╴': '0001', '╵': '1000', '╶': '0100', '╷': '0010', '╸': '0002', '╹': '2000', '╺': '0200', '╻': '0020',
  '╼': '0201', '╽': '1020', '╾': '0102', '╿': '2010',
};

// Dashed lines: number of dashes, weight, vertical.
const DASHES = {
  '┄': [3, 1, 0], '┅': [3, 2, 0], '┆': [3, 1, 1], '┇': [3, 2, 1],
  '┈': [4, 1, 0], '┉': [4, 2, 0], '┊': [4, 1, 1], '┋': [4, 2, 1],
  '╌': [2, 1, 0], '╍': [2, 2, 0], '╎': [2, 1, 1], '╏': [2, 2, 1],
};

const half = n => Math.floor(n / 2);

// isBoxRune reports whether drawBoxRune handles code point cp.
export function isBoxRune(cp) {
  return (cp >= 0x2500 && cp <= 0x257f) || (cp >= 0x2580 && cp <= 0x259f);
}

// drawBoxRune draws cp in the w×h cell at (ox, oy) with the current
// fillStyle and returns whether it handled it. Every fill stays inside the
// cell.
export function drawBoxRune(ctx, cp, ox, oy, w, h) {
  const fill = (x, y, fw, fh) => {
    const x0 = Math.max(0, x), y0 = Math.max(0, y);
    const x1 = Math.min(w, x + fw), y1 = Math.min(h, y + fh);
    if (x1 > x0 && y1 > y0) ctx.fillRect(ox + x0, oy + y0, x1 - x0, y1 - y0);
  };
  if (cp >= 0x2580 && cp <= 0x259f) {
    drawBlock(ctx, fill, cp, w, h);
    return true;
  }
  if (cp < 0x2500 || cp > 0x257f) return false;
  return drawBox(ctx, fill, String.fromCodePoint(cp), ox, oy, w, h);
}

const thickness = w => Math.max(1, Math.round(w / 8));
const armThick = (weight, t) => (weight === 1 || weight === 3 ? t : weight === 2 ? 2 * t : 0);

function drawBox(ctx, fill, r, ox, oy, w, h) {
  const t = thickness(w);
  const cx = half(w), cy = half(h);

  const dash = DASHES[r];
  if (dash) {
    drawDashes(fill, dash[0], armThick(dash[1], t), dash[2] === 1, w, h);
    return true;
  }
  if (r === '╭' || r === '╮' || r === '╯' || r === '╰') {
    drawRounded(ctx, fill, r, t, ox, oy, w, h);
    return true;
  }
  if (r === '╱' || r === '╲' || r === '╳') {
    drawDiagonal(ctx, r, t, ox, oy, w, h);
    return true;
  }
  const spec = ARMS[r];
  if (!spec) return false;
  const u = +spec[0], rt = +spec[1], d = +spec[2], l = +spec[3];
  const single = wt => wt === 1 || wt === 2;
  const dd = t;
  const ht = half(t);

  const hDouble = l === 3 || rt === 3;
  const vDouble = u === 3 || d === 3;
  const tv = Math.max(armThick(u, t), armThick(d, t));
  const th = Math.max(armThick(l, t), armThick(rt, t));
  if (single(l)) {
    const tt = armThick(l, t);
    const T = Math.max(tv, tt);
    let x1 = cx - half(T) + T;
    if (vDouble && rt === 0) {
      x1 = cx + dd - ht + t;
      if (u === 3 && d === 3) x1 = cx - ht;
    }
    fill(0, cy - half(tt), x1, tt);
  }
  if (single(rt)) {
    const tt = armThick(rt, t);
    const T = Math.max(tv, tt);
    let x0 = cx - half(T);
    if (vDouble && l === 0) {
      x0 = cx - dd - ht;
      if (u === 3 && d === 3) x0 = cx + dd - ht;
    }
    fill(x0, cy - half(tt), w, tt);
  }
  if (single(u)) {
    const tt = armThick(u, t);
    const T = Math.max(th, tt);
    let y1 = cy - half(T) + T;
    if (hDouble && d === 0) {
      y1 = cy + dd - ht + t;
      if (l === 3 && rt === 3) y1 = cy - ht;
    }
    fill(cx - half(tt), 0, tt, y1);
  }
  if (single(d)) {
    const tt = armThick(d, t);
    const T = Math.max(th, tt);
    let y0 = cy - half(T);
    if (hDouble && u === 0) {
      y0 = cy - dd - ht;
      if (l === 3 && rt === 3) y0 = cy + dd - ht;
    }
    fill(cx - half(tt), y0, tt, h);
  }

  // Double lines stop on the nearest perpendicular line when an arm exists
  // on their side, otherwise they go to the farthest one (boxdraw.go).
  let sv = 0, sh = 0;
  for (const wt of [u, d]) if (single(wt)) sv = Math.max(sv, armThick(wt, t));
  for (const wt of [l, rt]) if (single(wt)) sh = Math.max(sh, armThick(wt, t));
  for (const s of [-1, 1]) {
    if (hDouble) {
      const y = cy + s * dd - ht;
      const blocked = (s < 0 && u !== 0) || (s > 0 && d !== 0);
      if (l === 3) {
        let x1 = cx + dd - ht + t;
        if (blocked) x1 = cx - dd - ht + t;
        else if (sv > 0) x1 = cx - half(sv) + sv;
        fill(0, y, x1, t);
      }
      if (rt === 3) {
        let x0 = cx - dd - ht;
        if (blocked) x0 = cx + dd - ht;
        else if (sv > 0) x0 = cx - half(sv);
        fill(x0, y, w - x0, t);
      }
    }
    if (vDouble) {
      const x = cx + s * dd - ht;
      const blocked = (s < 0 && l !== 0) || (s > 0 && rt !== 0);
      if (u === 3) {
        let y1 = cy + dd - ht + t;
        if (blocked) y1 = cy - dd - ht + t;
        else if (sh > 0) y1 = cy - half(sh) + sh;
        fill(x, 0, t, y1);
      }
      if (d === 3) {
        let y0 = cy - dd - ht;
        if (blocked) y0 = cy + dd - ht;
        else if (sh > 0) y0 = cy - half(sh);
        fill(x, y0, t, h - y0);
      }
    }
  }
  return true;
}

function drawDashes(fill, n, t, vertical, w, h) {
  const length = vertical ? h : w;
  const seg = Math.floor(length / n);
  const gap = Math.max(1, Math.floor(seg / 3));
  for (let i = 0; i < n; i++) {
    const a = i * seg + half(gap);
    const b = (i + 1) * seg - (gap - half(gap));
    if (vertical) fill(half(w) - half(t), a, t, b - a);
    else fill(a, half(h) - half(t), b - a, t);
  }
}

// clipCell limits strokes to the cell, like the Go mask.
function clipCell(ctx, ox, oy, w, h, draw) {
  ctx.save();
  ctx.beginPath();
  ctx.rect(ox, oy, w, h);
  ctx.clip();
  draw();
  ctx.restore();
}

function drawRounded(ctx, fill, r, t, ox, oy, w, h) {
  const cx = half(w), cy = half(h);
  const fcx = cx - half(t) + t / 2;
  const fcy = cy - half(t) + t / 2;
  const R = Math.min(fcx, fcy);
  let sx = 1, sy = 1; // direction of the arms
  if (r === '╮') sx = -1;
  else if (r === '╯') { sx = -1; sy = -1; } else if (r === '╰') sy = -1;
  const ax = fcx + sx * R, ay = fcy + sy * R;
  // The quarter of the circle around (ax, ay) that faces the cell center.
  const a1 = Math.atan2(0, -sx), a2 = Math.atan2(-sy, 0);
  let sweep = a2 - a1;
  if (sweep > Math.PI) sweep -= 2 * Math.PI;
  if (sweep <= -Math.PI) sweep += 2 * Math.PI;
  clipCell(ctx, ox, oy, w, h, () => {
    ctx.beginPath();
    ctx.lineWidth = t;
    ctx.strokeStyle = ctx.fillStyle;
    ctx.arc(ox + ax, oy + ay, R, a1, a2, sweep < 0);
    ctx.stroke();
  });
  // Straight extensions up to the edges.
  if (sy > 0) fill(cx - half(t), Math.floor(ay), t, h);
  else fill(cx - half(t), 0, t, Math.ceil(ay));
  if (sx > 0) fill(Math.floor(ax), cy - half(t), w, t);
  else fill(0, cy - half(t), Math.ceil(ax), t);
}

function drawDiagonal(ctx, r, t, ox, oy, w, h) {
  clipCell(ctx, ox, oy, w, h, () => {
    ctx.beginPath();
    ctx.lineWidth = t;
    ctx.strokeStyle = ctx.fillStyle;
    // Lines run past the cell (clipped) so they meet their neighbors.
    if (r !== '╲') { ctx.moveTo(ox - w, oy + 2 * h); ctx.lineTo(ox + 2 * w, oy - h); }
    if (r !== '╱') { ctx.moveTo(ox - w, oy - h); ctx.lineTo(ox + 2 * w, oy + 2 * h); }
    ctx.stroke();
  });
}

const QUADS = [0b0100, 0b1000, 0b0001, 0b1101, 0b1001, 0b0111, 0b1011, 0b0010, 0b0110, 0b1110];

function drawBlock(ctx, fill, cp, w, h) {
  const eighthH = n => Math.floor((h * n + 4) / 8);
  const eighthW = n => Math.floor((w * n + 4) / 8);
  if (cp === 0x2580) fill(0, 0, w, half(h));
  else if (cp >= 0x2581 && cp <= 0x2588) {
    const n = cp - 0x2580;
    fill(0, h - eighthH(n), w, eighthH(n));
  } else if (cp >= 0x2589 && cp <= 0x258f) fill(0, 0, eighthW(0x2590 - cp), h);
  else if (cp === 0x2590) fill(half(w), 0, w - half(w), h);
  else if (cp >= 0x2591 && cp <= 0x2593) {
    const a = ctx.globalAlpha;
    ctx.globalAlpha = a * (64 * (cp - 0x2591 + 1)) / 255;
    fill(0, 0, w, h);
    ctx.globalAlpha = a;
  } else if (cp === 0x2594) fill(0, 0, w, eighthH(1));
  else if (cp === 0x2595) fill(w - eighthW(1), 0, eighthW(1), h);
  else {
    const q = QUADS[cp - 0x2596];
    const hw = half(w), hh = half(h);
    if (q & 1) fill(0, 0, hw, hh);
    if (q & 2) fill(hw, 0, w - hw, hh);
    if (q & 4) fill(0, hh, hw, h - hh);
    if (q & 8) fill(hw, hh, w - hw, h - hh);
  }
}
