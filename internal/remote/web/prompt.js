// The prompter's player: the queue the daemon sends (internal/prompt), the
// word on the anchor, the clock. Same behavior as internal/ui/prompter.go:
// it never moves to the next track by itself. No DOM here: node runs this
// module too (selftest.mjs).

export const MIN_WPM = 100;
export const MAX_WPM = 1000;
export const STEP_WPM = 25;

const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi);

export class Player {
  constructor(wpm) {
    this.tracks = [];
    this.curSeq = 0;      // 0: no track
    this.pos = 0;         // word on the anchor
    this.playing = false;
    this.ended = false;   // the last word went by: play replays the track
    this.acc = 0;         // ms spent on the current word
    this.wpm = clamp(Math.round(Number(wpm) || 300), MIN_WPM, MAX_WPM);
    this.dismissed = new Set(); // left here, until the daemon's queue drops them
  }

  // setQueue takes the daemon's queue; the current track and its position
  // stay if it is still there.
  setQueue(q) {
    const list = q && Array.isArray(q.tracks) ? q.tracks : [];
    const present = new Set(list.map(t => t.seq));
    for (const s of [...this.dismissed]) if (!present.has(s)) this.dismissed.delete(s);
    this.tracks = list.filter(t => t && !this.dismissed.has(t.seq) && Array.isArray(t.words) && t.words.length);
    if (this.index() >= 0) return;
    this.pos = 0; this.playing = false; this.ended = false; this.acc = 0;
    this.curSeq = this.tracks.length ? this.tracks[0].seq : 0;
  }

  index() {
    return this.curSeq ? this.tracks.findIndex(t => t.seq === this.curSeq) : -1;
  }

  cur() {
    const i = this.index();
    return i >= 0 ? this.tracks[i] : { seq: 0, words: [] };
  }

  toggle() {
    if (this.index() < 0) return;
    if (this.ended) {
      this.pos = 0; this.ended = false; this.acc = 0; this.playing = true;
      return;
    }
    this.playing = !this.playing;
  }

  step(d) {
    const ws = this.cur().words;
    if (!ws.length) return;
    this.pos = clamp(this.pos + d, 0, ws.length - 1);
    this.acc = 0;
    if (d < 0) this.ended = false;
  }

  // seek puts the anchor at a fraction (0 to 1) of the track.
  seek(f) {
    const ws = this.cur().words;
    if (!ws.length) return;
    this.pos = clamp(Math.round(clamp(f, 0, 1) * (ws.length - 1)), 0, ws.length - 1);
    this.acc = 0;
    this.ended = false;
  }

  speed(d) {
    this.wpm = clamp(this.wpm + d * STEP_WPM, MIN_WPM, MAX_WPM);
  }

  // next leaves the current track and plays the one after it; it returns
  // the seq to dismiss, 0 if there was no track.
  next() {
    const i = this.index();
    if (i < 0) return 0;
    const seq = this.curSeq;
    this.dismissed.add(seq);
    this.tracks.splice(i, 1);
    this.pos = 0; this.ended = false; this.acc = 0;
    const nxt = this.tracks[i] || this.tracks[0];
    this.curSeq = nxt ? nxt.seq : 0;
    this.playing = !!nxt;
    return seq;
  }

  dur(w) {
    const weight = w && w.w > 0 ? w.w : 1;
    return 60000 / this.wpm * weight;
  }

  // advance moves the clock by dt ms; true if the display changed.
  advance(dt) {
    const ws = this.cur().words;
    if (!this.playing || !ws.length) return false;
    this.acc += dt;
    let changed = false;
    while (this.acc >= this.dur(ws[this.pos]) - 1e-9) {
      if (this.pos === ws.length - 1) {
        this.ended = true; this.playing = false; this.acc = 0;
        return true;
      }
      this.acc -= this.dur(ws[this.pos]);
      this.pos++;
      changed = true;
    }
    return changed;
  }

  untilNext() {
    const ws = this.cur().words;
    if (!this.playing || !ws.length) return 0;
    return this.dur(ws[this.pos]) - this.acc;
  }

  view(ctx) {
    const ws = this.cur().words;
    if (!ws.length) return { before: [], cur: null, after: [], progress: 0 };
    const progress = ws.length > 1 && !this.ended ? this.pos / (ws.length - 1) : 1;
    return {
      before: ws.slice(Math.max(0, this.pos - ctx), this.pos),
      cur: ws[this.pos],
      after: ws.slice(this.pos + 1, this.pos + 1 + ctx),
      progress,
    };
  }
}
