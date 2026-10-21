package vt

import (
	"encoding/binary"
	"errors"
	"math"
	"time"
	"unicode/utf8"
)

// Binary snapshot of the full state of a terminal: grids, history, cursors,
// modes and parser state. It is used to reattach a window to a session
// running in the daemon, and to restore the history after a restart. Lines
// reuse the compact scrollback encoding.
//
// The format only ever grows: each section added later goes at the end, an
// older snapshot stops before it, and an older version ignores what it does
// not know. That is how the daemon's hot restart reads back the snapshots of
// the previous version.

const snapMagic = "NMX1"

// Bounds for a snapshot being read back. A snapshot comes from the daemon or
// from disk, but damaged data must neither panic nor allocate gigabytes: 16
// bytes once announced 10 million cells.
const (
	maxCols  = 4096 // also the maximum width of a history line
	maxRows  = 4096
	maxCells = 1 << 22
)

type enc struct {
	b   []byte
	tmp []byte // line being encoded
}

func (e *enc) u(v uint64)     { e.b = binary.AppendUvarint(e.b, v) }
func (e *enc) i(v int64)      { e.b = binary.AppendVarint(e.b, v) }
func (e *enc) u32(v uint32)   { e.b = binary.LittleEndian.AppendUint32(e.b, v) }
func (e *enc) byte1(v byte)   { e.b = append(e.b, v) }
func (e *enc) bytes(p []byte) { e.u(uint64(len(p))); e.b = append(e.b, p...) }
func (e *enc) str(s string)   { e.u(uint64(len(s))); e.b = append(e.b, s...) }
func (e *enc) flag(v bool) {
	if v {
		e.byte1(1)
	} else {
		e.byte1(0)
	}
}

type dec struct {
	b   []byte
	err error
}

var errSnap = errors.New("invalid terminal snapshot")

func (d *dec) u() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.err = errSnap
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *dec) i() int64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Varint(d.b)
	if n <= 0 {
		d.err = errSnap
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *dec) u32() uint32 {
	if d.err != nil || len(d.b) < 4 {
		d.err = errSnap
		return 0
	}
	v := binary.LittleEndian.Uint32(d.b)
	d.b = d.b[4:]
	return v
}

func (d *dec) byte1() byte {
	if d.err != nil || len(d.b) < 1 {
		d.err = errSnap
		return 0
	}
	v := d.b[0]
	d.b = d.b[1:]
	return v
}

func (d *dec) bytes() []byte {
	n := d.u()
	if d.err != nil || uint64(len(d.b)) < n {
		d.err = errSnap
		return nil
	}
	v := d.b[:n:n]
	d.b = d.b[n:]
	return v
}

func (d *dec) str() string { return string(d.bytes()) }
func (d *dec) flag() bool  { return d.byte1() != 0 }

func (e *enc) cursor(c cursor) {
	e.u(uint64(c.x))
	e.u(uint64(c.y))
	e.u32(uint32(c.fg))
	e.u32(uint32(c.bg))
	e.u(uint64(c.attr))
	var f byte
	if c.wrapNext {
		f |= 1
	}
	if c.origin {
		f |= 2
	}
	if c.charsets[0] {
		f |= 4
	}
	if c.charsets[1] {
		f |= 8
	}
	e.byte1(f)
	e.byte1(byte(c.gl))
}

func (d *dec) cursor() cursor {
	var c cursor
	c.x, c.y = int(min(d.u(), maxCols)), int(min(d.u(), maxRows))
	c.fg, c.bg = Color(d.u32()), Color(d.u32())
	c.attr = Attr(d.u())
	f := d.byte1()
	c.wrapNext, c.origin = f&1 != 0, f&2 != 0
	c.charsets = [2]bool{f&4 != 0, f&8 != 0}
	c.gl = int(d.byte1()) & 1
	return c
}

func (e *enc) grid(g *grid) {
	for y := range g.lines {
		e.tmp = encodeLine(e.tmp[:0], g.lines[y], g.wrapped[y])
		e.bytes(e.tmp)
	}
}

// grid reads back a grid of rows lines of at most cols cells, into a grid
// of gcols columns (wider when cols is 1, see LoadSnapshot).
func (d *dec) grid(cols, gcols, rows int) *grid {
	if d.err != nil {
		return nil
	}
	g := newGrid(gcols, rows)
	var buf []Cell
	for y := 0; y < rows && d.err == nil; y++ {
		raw := d.bytes()
		if d.err != nil {
			break
		}
		var err error
		buf, g.wrapped[y], err = decodeLine(raw, buf, cols)
		if err != nil {
			d.err = errSnap
			break
		}
		copy(g.lines[y], buf)
	}
	return g
}

// Snapshot serializes the full terminal state.
func (t *Terminal) Snapshot() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked()
}

func (t *Terminal) snapshotLocked() []byte {
	// Reserved in one go: the history is copied in as is, and the grids
	// rarely take more than one byte per cell.
	size := 64*1024 + t.sb.bytes + 3*t.sb.n + 2*t.rows*(t.cols+32)
	e := &enc{b: make([]byte, 0, size)}
	e.b = append(e.b, snapMagic...)
	e.u(uint64(t.cols))
	e.u(uint64(t.rows))
	e.flag(t.g == t.alt)
	e.grid(t.main)
	e.grid(t.alt)

	sb := t.sb
	e.u(uint64(sb.max))
	e.i(sb.total)
	e.u(uint64(sb.n))
	for i := 0; i < sb.n; i++ {
		e.bytes(sb.ring[(sb.start+i)%len(sb.ring)])
	}

	e.cursor(t.cur)
	e.cursor(t.saved[0])
	e.cursor(t.saved[1])
	e.cursor(t.saved1049)
	e.u(uint64(t.top))
	e.u(uint64(t.bot))
	tabs := make([]byte, len(t.tabs))
	for i, on := range t.tabs {
		if on {
			tabs[i] = 1
		}
	}
	e.bytes(tabs)
	e.u32(uint32(t.modes))
	e.u(uint64(t.cursorShap))
	e.u(uint64(t.lastRune))
	e.str(t.title)
	e.str(t.cwd)

	// The parser may be in the middle of a sequence: the stream that follows
	// the snapshot must be interpreted exactly as on the daemon side.
	p := &t.p
	e.byte1(p.state)
	e.u(uint64(p.np))
	for i := 0; i < p.np; i++ {
		e.i(int64(p.params[i]))
	}
	e.u32(p.sub)
	e.i(int64(p.cur))
	e.flag(p.hasCur)
	e.byte1(p.inter)
	e.byte1(p.private)
	e.bytes(p.osc)
	e.i(int64(p.uacc))
	e.byte1(byte(p.uneed))
	// Section added later: an older snapshot stops before it.
	e.progress(t)
	// Added after that: the pixel size.
	e.pixelSize(t)
	return e.b
}

// pixelSize writes the pixel size (CSI 14 t and 16 t), without which these
// queries answered 0 after a hot restart.
func (e *enc) pixelSize(t *Terminal) {
	e.u(uint64(max(t.pxW, 0)))
	e.u(uint64(max(t.pxH, 0)))
}

// LoadSnapshot replaces the terminal state with the one in a snapshot.
// The Terminal object is kept: the renderer that references it keeps working.
// Every line is validated and values are brought back within their bounds:
// no snapshot, even a damaged one, causes a panic here or later.
func (t *Terminal) LoadSnapshot(data []byte) error {
	if len(data) < len(snapMagic) || string(data[:len(snapMagic)]) != snapMagic {
		return errSnap
	}
	d := &dec{b: data[len(snapMagic):]}
	cols64, rows64 := d.u(), d.u()
	// Each grid line takes at least 4 bytes: do not reserve two grids that
	// the data cannot fill.
	if d.err != nil || cols64 < 1 || rows64 < 1 || cols64 > maxCols || rows64 > maxRows ||
		cols64*rows64 > maxCells || uint64(len(d.b)) < 2*4*rows64 {
		return errSnap
	}
	cols, rows := int(cols64), int(rows64)
	// A single column is no longer allowed (a wide character does not fit
	// in it): a snapshot from an older version is widened to two.
	gcols := max(cols, 2)
	altActive := d.flag()
	main := d.grid(cols, gcols, rows)
	alt := d.grid(cols, gcols, rows)

	sb := newScrollback(0)
	if m := d.u(); m <= math.MaxInt32 {
		sb.max = int(m)
	}
	total := d.i()
	// A line takes at least 4 bytes: a line count the data cannot hold is
	// wrong, and the ring can then safely be reserved in one go.
	n := d.u()
	if n > uint64(len(d.b)/4) {
		return errSnap
	}
	sb.ring = make([][]byte, 0, n)
	size := 0
	for i := uint64(0); i < n && d.err == nil; i++ {
		raw := d.bytes()
		if d.err == nil && checkLine(raw, maxCols) != nil {
			d.err = errSnap
		}
		sb.ring = append(sb.ring, raw)
		size += len(raw)
	}
	// No more lines than the maximum: keep the most recent ones. They are
	// copied into a single block, each one capped to its own slot.
	sb.ring = sb.ring[len(sb.ring)-min(len(sb.ring), sb.max):]
	block := make([]byte, 0, size)
	for i, raw := range sb.ring {
		at := len(block)
		block = append(block, raw...)
		sb.ring[i] = block[at:len(block):len(block)]
		sb.bytes += len(raw)
	}
	sb.n = len(sb.ring)
	sb.total = max(total, int64(sb.n))

	cur, s0, s1, s1049 := d.cursor(), d.cursor(), d.cursor(), d.cursor()
	top, bot := d.u(), d.u()
	rawTabs := d.bytes()
	modes := Mode(d.u32())
	shape := int(min(d.u(), math.MaxInt32))
	last := rune(min(d.u(), math.MaxInt32))
	if !utf8.ValidRune(last) {
		last = 0
	}
	title, cwd := d.str(), d.str()

	var p parser
	p.state = d.byte1()
	p.np = int(min(d.u(), maxParams))
	for i := 0; i < p.np; i++ {
		p.params[i] = int32(max(min(d.i(), math.MaxInt32), math.MinInt32))
	}
	p.sub = d.u32()
	p.cur = int32(max(min(d.i(), math.MaxInt32), math.MinInt32))
	p.hasCur = d.flag()
	p.inter = d.byte1()
	p.private = d.byte1()
	p.osc = append([]byte(nil), d.bytes()...)
	p.uacc = rune(max(min(d.i(), math.MaxInt32), math.MinInt32))
	p.uneed = int(d.byte1())
	p.sanitize()
	var progress map[string]*ProgStatus
	var progGen, progTick uint64
	pxW, pxH := -1, -1
	if d.err == nil && len(d.b) > 0 {
		progGen, progTick, progress = d.progress()
		if d.err == nil && len(d.b) > 0 {
			pxW, pxH = int(min(d.u(), 1<<20)), int(min(d.u(), 1<<20))
		}
	}
	if d.err != nil {
		return d.err
	}

	t.mu.Lock()
	t.cols, t.rows = gcols, rows
	t.main, t.alt = main, alt
	t.g = main
	if altActive {
		t.g = alt
	}
	t.sb = sb
	clamp := func(c cursor) cursor {
		c.x, c.y = min(max(c.x, 0), gcols-1), min(max(c.y, 0), rows-1)
		c.attr &^= AttrWide | AttrWideCont
		return c
	}
	t.cur, t.saved[0], t.saved[1], t.saved1049 = clamp(cur), clamp(s0), clamp(s1), clamp(s1049)
	bot = min(bot, uint64(rows-1))
	if top < bot {
		t.top, t.bot = int(top), int(bot)
	} else {
		t.top, t.bot = 0, rows-1
	}
	t.tabs = make([]bool, gcols)
	for i := range t.tabs {
		t.tabs[i] = i < len(rawTabs) && rawTabs[i] != 0
	}
	t.modes = modes
	t.modesA.Store(uint32(modes))
	t.cursorShap, t.lastRune, t.title, t.cwd = shape, last, title, cwd
	t.progress, t.progGen, t.progTick = progress, progGen, progTick
	if pxW >= 0 {
		// An older snapshot has no pixel size: keep the one the terminal
		// already has.
		t.pxW, t.pxH = pxW, pxH
	}
	t.p = p
	t.dirty = make([]bool, rows)
	t.dirtyAll = true
	t.mu.Unlock()
	t.seq.Add(1)
	return nil
}

// RestoreAfterRestart prepares a terminal reloaded from disk for a new
// shell. The old screen stays on display, a "session restored" line is
// printed below it and the new prompt comes right after, as if the terminal
// had kept going. The interactive state (modes, an editor's alternate
// screen, a sequence in progress) starts over from scratch.
func (t *Terminal) RestoreAfterRestart(at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.p = parser{}
	t.g = t.main
	clearAll(t.alt, 0)
	t.saved = [2]cursor{}
	t.top, t.bot = 0, t.rows-1
	t.modes = ModeAutoWrap | ModeCursorVisible
	t.modesA.Store(uint32(t.modes))
	t.cursorShap = 0
	t.title = ""
	t.progress = nil // the programs that set them are gone

	last := -1
	for y, l := range t.main.lines {
		if !lineEmpty(l) {
			last = y
		}
	}
	t.cur = cursor{y: max(last, 0)}
	if last >= 0 {
		t.index()
	}
	t.cur.fg, t.cur.attr = Indexed(8), AttrFaint
	sep := "── session restored on " + at.Format("Jan 2 at 15:04") + " ──"
	for _, r := range sep {
		if t.cur.x >= t.cols-1 {
			break
		}
		t.print(r)
	}
	t.cur = cursor{y: t.cur.y}
	t.index()
	t.dirtyAll = true
	t.seq.Add(1)
}
