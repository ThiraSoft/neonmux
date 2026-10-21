package vt

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Mode holds the DEC/ANSI modes the rest of the app cares about.
type Mode uint32

const (
	ModeAppCursor Mode = 1 << iota
	ModeAppKeypad
	ModeAutoWrap
	ModeOrigin
	ModeInsert
	ModeCursorVisible
	ModeBracketedPaste
	ModeMouseX10
	ModeMouseNormal
	ModeMouseButton
	ModeMouseAny
	ModeMouseSGR
	ModeFocus
	ModeAltScreen
	ModeSync
	ModeCursorBlink
	ModeLNM
	ModeAltScroll
)

// ModeMouseMask groups the mouse tracking modes.
const ModeMouseMask = ModeMouseX10 | ModeMouseNormal | ModeMouseButton | ModeMouseAny

// EventKind identifies an event the program passes up to the terminal.
type EventKind int

const (
	EvBell      EventKind = iota
	EvNotify              // OSC 9 / OSC 777
	EvClipboard           // OSC 52
	EvFX                  // OSC 7777: in-house visual effects protocol
)

// Event is produced by the parser and consumed by the UI.
type Event struct {
	Kind  EventKind
	Title string
	Body  string
}

type cursor struct {
	x, y     int
	fg, bg   Color
	attr     Attr
	wrapNext bool
	origin   bool
	charsets [2]bool // true = DEC graphics set
	gl       int
}

type grid struct {
	lines   [][]Cell
	wrapped []bool
}

func newGrid(cols, rows int) *grid {
	g := &grid{lines: make([][]Cell, rows), wrapped: make([]bool, rows)}
	back := make([]Cell, cols*rows)
	for y := range g.lines {
		g.lines[y] = back[y*cols : (y+1)*cols : (y+1)*cols]
	}
	return g
}

// Terminal is the full state of a virtual terminal.
//
// Write is called by the PTY reader goroutine. The renderer must wrap its
// reads in Lock/Unlock.
type Terminal struct {
	mu sync.Mutex
	p  parser

	cols, rows int
	main, alt  *grid
	g          *grid
	sb         *Scrollback

	cur        cursor
	saved      [2]cursor // DECSC for the main screen and the alternate screen
	saved1049  cursor
	top, bot   int
	tabs       []bool
	modes      Mode
	modesA     atomic.Uint32
	cursorShap int

	lastRune rune

	title    string
	cwd      string
	progress map[string]*ProgStatus // Program Status Protocol (OSC 7501)
	progGen  uint64                 // state changes (ProgStatus.Gen)
	progTick uint64                 // reports received
	events   []Event
	evBytes  int // size of the queued texts (Title + Body)
	replyBuf []byte
	reply    func([]byte)

	reportFG, reportBG, reportCursor [3]uint8
	palette                          func(i int) (r, g, b uint8)
	pxW, pxH                         int

	dirty    []bool
	dirtyAll bool
	seq      atomic.Uint64
	syncAt   time.Time
}

const (
	// feedChunk is the chunk of output processed under the lock: the
	// renderer never waits for more than one chunk, even under a flood of REP.
	feedChunk = 8 << 10
	// maxReply caps the replies Write sends back to the program: 64 KB of
	// DA1 used to ask for 196 KB.
	maxReply = 4 << 10
	// maxEvents and maxEventBytes bound the event queue between two calls
	// to TakeEvents. Anything beyond that is lost.
	maxEvents     = 256
	maxEventBytes = 4 << 20
)

// New creates a cols×rows terminal with a history of scrollback lines.
// It has at least two columns so that a wide character always fits.
func New(cols, rows, scrollback int) *Terminal {
	if cols < 2 {
		cols = 2
	}
	if rows < 1 {
		rows = 1
	}
	t := &Terminal{
		cols: cols, rows: rows,
		main: newGrid(cols, rows),
		alt:  newGrid(cols, rows),
		sb:   newScrollback(scrollback),
	}
	t.g = t.main
	t.reset()
	return t
}

func (t *Terminal) reset() {
	t.cur = cursor{}
	t.saved = [2]cursor{}
	t.top, t.bot = 0, t.rows-1
	t.modes = ModeAutoWrap | ModeCursorVisible
	t.modesA.Store(uint32(t.modes))
	t.cursorShap = 0
	t.g = t.main
	t.resetTabs()
	for _, g := range []*grid{t.main, t.alt} {
		for y := range g.lines {
			clearCells(g.lines[y], 0)
			g.wrapped[y] = false
		}
	}
	t.dirty = make([]bool, t.rows)
	t.dirtyAll = true
}

func (t *Terminal) resetTabs() {
	t.tabs = make([]bool, t.cols)
	for x := 8; x < t.cols; x += 8 {
		t.tabs[x] = true
	}
}

// SetReply sets the function that sends bytes back to the program.
func (t *Terminal) SetReply(f func([]byte)) { t.reply = f }

// SetReportColors sets the colors reported to OSC 10/11/12 queries.
func (t *Terminal) SetReportColors(fg, bg, cur [3]uint8, palette func(i int) (r, g, b uint8)) {
	t.mu.Lock()
	t.reportFG, t.reportBG, t.reportCursor, t.palette = fg, bg, cur, palette
	t.dirtyAll = true
	t.mu.Unlock()
	t.seq.Add(1)
}

// SetPixelSize records the pixel size (CSI 14 t / 16 t queries).
func (t *Terminal) SetPixelSize(w, h int) {
	t.mu.Lock()
	t.pxW, t.pxH = w, h
	t.mu.Unlock()
}

// Write consumes the program's output in chunks of feedChunk bytes: the
// lock is released between two chunks so the renderer can get through.
// Write is still not atomic with respect to another Write: the caller
// serializes them. Replies to the program go out in a
// single call at the end, at most maxReply bytes.
func (t *Terminal) Write(data []byte) (int, error) {
	var out []byte
	for rest := data; ; {
		chunk := rest[:min(len(rest), feedChunk)]
		rest = rest[len(chunk):]
		t.mu.Lock()
		t.feed(chunk)
		if len(rest) == 0 && len(t.replyBuf) > 0 {
			out = append(out, t.replyBuf...)
			t.replyBuf = t.replyBuf[:0]
		}
		t.mu.Unlock()
		t.seq.Add(1)
		if len(rest) == 0 {
			break
		}
	}
	if out != nil && t.reply != nil {
		t.reply(out)
	}
	return len(data), nil
}

// Lock and Unlock protect the renderer's reads.
func (t *Terminal) Lock()   { t.mu.Lock() }
func (t *Terminal) Unlock() { t.mu.Unlock() }

// Seq changes on every state change (read without the lock).
func (t *Terminal) Seq() uint64 { return t.seq.Load() }

// Modes returns the current modes (read without the lock).
func (t *Terminal) Modes() Mode { return Mode(t.modesA.Load()) }

// Size returns the size in cells. Lock required.
func (t *Terminal) Size() (cols, rows int) { return t.cols, t.rows }

// ScreenLine returns line y of the active screen. Lock required.
func (t *Terminal) ScreenLine(y int) []Cell { return t.g.lines[y] }

// ScreenText returns the text of the visible screen, one string per line,
// without trailing spaces (neonmux capture).
func (t *Terminal) ScreenText() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := make([]string, t.rows)
	var b strings.Builder
	for y := 0; y < t.rows; y++ {
		b.Reset()
		for _, c := range t.g.lines[y] {
			if c.Attr()&AttrWideCont != 0 {
				continue
			}
			r := c.Rune()
			if r == 0 {
				r = ' '
			}
			b.WriteRune(r)
		}
		lines[y] = strings.TrimRight(b.String(), " ")
	}
	return lines
}

// HistoryLen returns the number of history lines available. Lock required.
func (t *Terminal) HistoryLen() int {
	if t.g == t.alt {
		return 0
	}
	return t.sb.Len()
}

// HistoryBytes returns the memory used by the compressed history. Lock required.
func (t *Terminal) HistoryBytes() int { return t.sb.Bytes() }

// ViewLine returns line y of the view scrolled back by offset lines. Lock required.
func (t *Terminal) ViewLine(y, offset int, buf []Cell) []Cell {
	line, _ := t.ViewLineBuf(y, offset, buf)
	return line
}

// ViewLineBuf works like ViewLine and also returns the buffer to pass to
// the next call. A history line is decoded into buf, grown if needed, and
// next refers to it. A screen line is returned without a copy and next is
// buf unchanged. So next is never a screen line: keeping it and writing to
// it is safe, and in steady state the renderer no longer allocates
// anything. line is only valid under the lock and until the next call with
// the same buffer. Lock required.
//
//	line, r.buf = t.ViewLineBuf(y, offset, r.buf)
func (t *Terminal) ViewLineBuf(y, offset int, buf []Cell) (line, next []Cell) {
	i := y - offset
	if i >= 0 || t.g == t.alt {
		return t.g.lines[max(i, 0)], buf
	}
	h := t.sb.Len() + i
	if h < 0 {
		return buf[:0], buf
	}
	cells, _ := t.sb.line(h, buf)
	return cells, cells
}

// AbsBase returns the absolute index of the first history line still
// available and that of the first screen line. Lock required.
func (t *Terminal) AbsBase() (first, screen int64) {
	if t.g == t.alt {
		return 0, 0
	}
	return t.sb.total - int64(t.sb.Len()), t.sb.total
}

// AbsLine returns a line by absolute index (see AbsBase). Lock required.
func (t *Terminal) AbsLine(a int64, buf []Cell) ([]Cell, bool) {
	first, screen := t.AbsBase()
	switch {
	case a >= screen && a < screen+int64(t.rows):
		y := int(a - screen)
		return t.g.lines[y], t.g.wrapped[y]
	case a >= first && a < screen:
		return t.sb.line(int(a-first), buf)
	}
	return buf[:0], false
}

// Cursor returns the cursor position and style. Lock required.
func (t *Terminal) Cursor() (x, y int, visible bool, shape int) {
	return t.cur.x, t.cur.y, t.modes&ModeCursorVisible != 0, t.cursorShap
}

// DirtyAll and RowDirty expose the tracking of modified lines. Lock required.
func (t *Terminal) DirtyAll() bool      { return t.dirtyAll }
func (t *Terminal) RowDirty(y int) bool { return t.dirtyAll || t.dirty[y] }

// ClearDirty resets the line tracking. Lock required.
func (t *Terminal) ClearDirty() {
	t.dirtyAll = false
	clear(t.dirty)
}

// Synchronizing reports a synchronized update in progress (mode 2026).
// The renderer waits for the end of the block to avoid half-drawn frames.
func (t *Terminal) Synchronizing() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.modes&ModeSync != 0 && time.Since(t.syncAt) < 150*time.Millisecond
}

// Title returns the title set by OSC 0/2.
func (t *Terminal) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title
}

// Cwd returns the directory reported by OSC 7.
func (t *Terminal) Cwd() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cwd
}

// TakeEvents drains the event queue.
func (t *Terminal) TakeEvents() []Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	ev := t.events
	t.events, t.evBytes = nil, 0
	return ev
}

// pushEvent queues an event. Consecutive bells count as one, and the queue
// is bounded in count and in size: 64 KB of BEL used to give 65,536
// events.
func (t *Terminal) pushEvent(ev Event) {
	n := len(t.events)
	if ev.Kind == EvBell && n > 0 && t.events[n-1].Kind == EvBell {
		return
	}
	size := len(ev.Title) + len(ev.Body)
	if n >= maxEvents || t.evBytes+size > maxEventBytes {
		return
	}
	t.events = append(t.events, ev)
	t.evBytes += size
}

func (t *Terminal) markDirty(y int) {
	if y >= 0 && y < len(t.dirty) {
		t.dirty[y] = true
	}
}

func (t *Terminal) markRange(y0, y1 int) {
	for y := y0; y <= y1; y++ {
		t.markDirty(y)
	}
}

func (t *Terminal) setMode(m Mode, on bool) {
	if on {
		t.modes |= m
	} else {
		t.modes &^= m
	}
	t.modesA.Store(uint32(t.modes))
}

// replyString queues a reply. A reply that no longer fits under maxReply
// is dropped whole: the program never receives a truncated sequence.
func (t *Terminal) replyString(s string) {
	if len(t.replyBuf)+len(s) > maxReply {
		return
	}
	t.replyBuf = append(t.replyBuf, s...)
}

// Resize resizes both screens. When the width changes, the main screen
// and the history are rewrapped (see reflowMain); the alternate screen is
// cut, its program redraws it. Lines that no longer fit vertically go to
// the history so that the cursor stays visible.
func (t *Terminal) Resize(cols, rows int) {
	if cols < 2 {
		cols = 2
	}
	if rows < 1 {
		rows = 1
	}
	t.mu.Lock()
	defer t.seq.Add(1)
	defer t.mu.Unlock()
	if cols == t.cols && rows == t.rows {
		return
	}
	altActive := t.g == t.alt

	mainCur := &t.cur
	if altActive {
		mainCur = &t.saved1049
	}
	reflowed := cols != t.cols
	if reflowed {
		t.main = t.reflowMain(cols, rows, mainCur)
	} else {
		t.main = t.resizeGrid(t.main, cols, rows, &mainCur.y, true)
	}
	altCur := &t.cur.y
	if !altActive {
		altCur = new(int)
	}
	t.alt = t.resizeGrid(t.alt, cols, rows, altCur, false)
	if altActive {
		t.g = t.alt
	} else {
		t.g = t.main
	}

	t.cols, t.rows = cols, rows
	t.top, t.bot = 0, rows-1
	t.resetTabs()
	t.dirty = make([]bool, rows)
	t.dirtyAll = true
	clampCursor := func(c *cursor) {
		c.x = min(max(c.x, 0), cols-1)
		c.y = min(max(c.y, 0), rows-1)
		// The reflow already decided whether the wrap stays pending.
		if !reflowed || c != mainCur {
			c.wrapNext = false
		}
	}
	clampCursor(&t.cur)
	clampCursor(&t.saved[0])
	clampCursor(&t.saved[1])
	clampCursor(&t.saved1049)
}

func (t *Terminal) resizeGrid(old *grid, cols, rows int, cy *int, isMain bool) *grid {
	oldRows := len(old.lines)
	srcStart := 0
	if rows < oldRows {
		excess := oldRows - rows
		// First the empty lines below the cursor.
		for excess > 0 && oldRows-1 > *cy && lineEmpty(old.lines[oldRows-1]) {
			oldRows--
			excess--
		}
		// Then the top of the screen, which goes to the history.
		if excess > 0 {
			if isMain {
				for y := 0; y < excess; y++ {
					t.sb.push(old.lines[y], old.wrapped[y])
				}
			}
			srcStart = excess
			*cy -= excess
		}
	}
	g := newGrid(cols, rows)
	for y := 0; y < rows && srcStart+y < oldRows; y++ {
		src := old.lines[srcStart+y]
		dst := g.lines[y]
		n := copy(dst, src)
		// Do not leave half of a wide character at the edge of the line.
		if n > 0 && n < len(src) && dst[n-1].Attr()&AttrWide != 0 {
			dst[n-1] = blankCell(dst[n-1].BG)
		}
		g.wrapped[y] = old.wrapped[srcStart+y]
	}
	return g
}

func lineEmpty(l []Cell) bool {
	for _, c := range l {
		if !c.IsBlank() {
			return false
		}
	}
	return true
}

func clearCells(cells []Cell, bg Color) { fillCells(cells, blankCell(bg)) }

// fillCells fills cells with c, doubling the copied area on each pass.
func fillCells(cells []Cell, c Cell) {
	if len(cells) == 0 {
		return
	}
	cells[0] = c
	for i := 1; i < len(cells); i *= 2 {
		copy(cells[i:], cells[:i])
	}
}
